package token

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"offlinepay/internal/crypto"
	"offlinepay/internal/domain"
	"offlinepay/internal/repository"

	"github.com/google/uuid"
)

type Service struct {
	repo          *repository.Repository
	bankPrivateKey *ecdsa.PrivateKey
}

type staticReader struct{}

func (staticReader) Read(p []byte) (n int, err error) {
	for i := range p {
		p[i] = byte(i % 256)
	}
	return len(p), nil
}

func NewService(repo *repository.Repository, bankKey *ecdsa.PrivateKey) *Service {
	// If bankKey is nil, generate a mock key to ensure the system is self-contained and runnable
	if bankKey == nil {
		mockKey, err := crypto.GenerateKeyPair()
		if err != nil {
			slog.Warn("crypto.GenerateKeyPair failed, falling back to deterministic key generation", "error", err)
			mockKey, _ = ecdsa.GenerateKey(elliptic.P256(), staticReader{})
		}
		bankKey = mockKey
		slog.Warn("no bank private key supplied, generated a temporary ECDSA key pair for simulation")
	}

	return &Service{
		repo:           repo,
		bankPrivateKey: bankKey,
	}
}

// SetBankPrivateKey (useful for tests)
func (s *Service) SetBankPrivateKey(key *ecdsa.PrivateKey) {
	s.bankPrivateKey = key
}

func (s *Service) ComputeOfflineCreditLimit(ctx context.Context, ownerID string) (int64, error) {
	devices, err := s.repo.GetDevicesByOwner(ctx, ownerID)
	if err != nil {
		return 0, err
	}

	// Default limit for a new user with no registered device is $2.00 (200 cents)
	if len(devices) == 0 {
		return 200, nil
	}

	bestTrustScore := 0.0
	hasCompromised := false
	hasActive := false

	for _, dev := range devices {
		if dev.Status == domain.DeviceCompromised || dev.Status == domain.DeviceRevoked {
			hasCompromised = true
		} else if dev.Status == domain.DeviceActive {
			hasActive = true
			if dev.TrustScore > bestTrustScore {
				bestTrustScore = dev.TrustScore
			}
		}
	}

	// High-risk/compromised devices block offline spending completely (limit = 0)
	if hasCompromised && !hasActive {
		return 0, nil
	}

	// Base limit = $2.00 (200 cents), Max limit = $50.00 (5000 cents)
	limit := int64(200 + bestTrustScore*4800)
	if limit < 0 {
		limit = 0
	}
	return limit, nil
}

// Issue a bank-signed offline spending token
func (s *Service) IssueToken(ctx context.Context, ownerID string, value int64, duration time.Duration) (*domain.OfflineToken, error) {
	if value <= 0 {
		return nil, errors.New("token value must be greater than zero")
	}

	// Compute and verify dynamic limit
	limit, err := s.ComputeOfflineCreditLimit(ctx, ownerID)
	if err != nil {
		return nil, fmt.Errorf("failed to compute credit limit: %w", err)
	}
	if value > limit {
		return nil, fmt.Errorf("requested token value %d exceeds dynamic limit %d", value, limit)
	}

	tokenID := uuid.New().String()
	expiry := time.Now().Add(duration)

	var tok *domain.OfflineToken

	// Perform in a transaction: check balance, reserve funds, create token, and log outbox event
	err = s.repo.WithTx(ctx, func(tx *sql.Tx) error {
		// Get and lock sender's account balance
		bal, err := s.repo.GetBalanceForUpdate(ctx, tx, ownerID)
		if err != nil {
			return fmt.Errorf("failed to retrieve account balance: %w", err)
		}

		if bal.AvailableBalance < value {
			return fmt.Errorf("insufficient available balance: has %d, requested token of %d", bal.AvailableBalance, value)
		}

		// Reserve funds: move from available to reserved
		newAvailable := bal.AvailableBalance - value
		newReserved := bal.ReservedBalance + value
		err = s.repo.UpdateBalance(ctx, tx, ownerID, newAvailable, newReserved)
		if err != nil {
			return fmt.Errorf("failed to update balances: %w", err)
		}

		// Create bank signature over token fields
		tokenData := fmt.Sprintf("token:%s:owner:%s:value:%d:expiry:%d", tokenID, ownerID, value, expiry.Unix())
		signature, err := crypto.Sign(s.bankPrivateKey, []byte(tokenData))
		if err != nil {
			return fmt.Errorf("failed to sign token: %w", err)
		}

		tok = &domain.OfflineToken{
			TokenID:          tokenID,
			OwnerID:          ownerID,
			Value:            value,
			Expiry:           expiry,
			Consumed:         false,
			TokenSignature:   signature,
			ReservedAt:       time.Now(),
			RiskScoreAtIssue: 0.1, // mock basic risk metric
		}

		err = s.repo.CreateToken(ctx, tx, tok)
		if err != nil {
			return fmt.Errorf("failed to save token: %w", err)
		}

		// Satisfy foreign key constraint on ledger_entries by creating a placeholder intent
		var deviceID string
		devices, errDev := s.repo.GetDevicesByOwner(ctx, ownerID)
		if errDev == nil && len(devices) > 0 {
			deviceID = devices[0].DeviceID
		} else {
			deviceID = "device-auto-" + ownerID
			_ = s.repo.CreateDevice(ctx, &domain.Device{
				DeviceID:   deviceID,
				OwnerID:    ownerID,
				PublicKey:  "dummy-key",
				TrustScore: 1.0,
				Status:     domain.DeviceActive,
				CreatedAt:  time.Now(),
			})
		}

		placeholderIntent := &domain.PaymentIntent{
			TxnID:      "tok-res-" + tokenID,
			SenderID:   ownerID,
			ReceiverID: "BANK",
			Amount:     value,
			Currency:   "USD",
			Nonce:      "nonce-tok-res-" + tokenID,
			Expiry:     expiry,
			Status:     "RESERVED",
			DeviceID:   deviceID,
			TokenID:    tokenID,
			CreatedAt:  time.Now(),
			UpdatedAt:  time.Now(),
		}
		err = s.repo.CreatePaymentIntent(ctx, tx, placeholderIntent)
		if err != nil {
			return fmt.Errorf("failed to create placeholder payment intent: %w", err)
		}

		// Write Ledger Entry for Reservation
		ledgerEntry := &domain.LedgerEntry{
			TxnID:        "tok-res-" + tokenID,
			AccountID:    ownerID,
			Direction:    domain.DirectionDebit, // Debited from available
			Amount:       value,
			EntryType:    domain.EntryTypeReservation,
			BalanceAfter: newAvailable,
		}
		err = s.repo.CreateLedgerEntry(ctx, tx, ledgerEntry)
		if err != nil {
			return fmt.Errorf("failed to create ledger entry: %w", err)
		}

		// Write Outbox Event for token issuance
		payloadBytes, _ := json.Marshal(tok)
		outboxEvent := &domain.OutboxEvent{
			StreamName: "token.issued",
			EventType:  "TokenIssued",
			Payload:    string(payloadBytes),
		}
		err = s.repo.CreateOutboxEvent(ctx, tx, outboxEvent)
		if err != nil {
			return fmt.Errorf("failed to write outbox event: %w", err)
		}

		return nil
	})

	if err != nil {
		return nil, err
	}

	return tok, nil
}

// Release funds for expired tokens
func (s *Service) ReleaseExpiredTokens(ctx context.Context) (int, error) {
	tokens, err := s.repo.GetExpiredTokens(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to get expired tokens: %w", err)
	}

	releasedCount := 0
	for _, tok := range tokens {
		err = s.repo.WithTx(ctx, func(tx *sql.Tx) error {
			// Lock token row
			tokLocked, err := s.repo.GetTokenForUpdate(ctx, tx, tok.TokenID)
			if err != nil {
				return err
			}

			// double check states
			if tokLocked.Consumed || tokLocked.ReleasedAt != nil {
				return nil // already processed
			}

			// Lock balance row
			bal, err := s.repo.GetBalanceForUpdate(ctx, tx, tok.OwnerID)
			if err != nil {
				return err
			}

			// Release funds: move from reserved back to available
			if bal.ReservedBalance < tok.Value {
				return fmt.Errorf("inconsistent reserved balance for user %s: has %d, token needs %d", tok.OwnerID, bal.ReservedBalance, tok.Value)
			}

			newAvailable := bal.AvailableBalance + tok.Value
			newReserved := bal.ReservedBalance - tok.Value
			err = s.repo.UpdateBalance(ctx, tx, tok.OwnerID, newAvailable, newReserved)
			if err != nil {
				return err
			}

			err = s.repo.ReleaseToken(ctx, tx, tok.TokenID)
			if err != nil {
				return err
			}

			// Write Ledger Entry for Release
			ledgerEntry := &domain.LedgerEntry{
				TxnID:        "tok-res-" + tok.TokenID,
				AccountID:    tok.OwnerID,
				Direction:    domain.DirectionCredit, // Credited back to available
				Amount:       tok.Value,
				EntryType:    domain.EntryTypeRelease,
				BalanceAfter: newAvailable,
			}
			err = s.repo.CreateLedgerEntry(ctx, tx, ledgerEntry)
			if err != nil {
				return err
			}

			// Outbox Event
			payloadBytes, _ := json.Marshal(map[string]interface{}{
				"token_id": tok.TokenID,
				"owner_id": tok.OwnerID,
				"amount":   tok.Value,
			})
			outboxEvent := &domain.OutboxEvent{
				StreamName: "token.expired",
				EventType:  "TokenExpiredAndReleased",
				Payload:    string(payloadBytes),
			}
			err = s.repo.CreateOutboxEvent(ctx, tx, outboxEvent)
			if err != nil {
				return err
			}

			releasedCount++
			return nil
		})
		if err != nil {
			slog.Error("failed to release token", "token_id", tok.TokenID, "error", err)
		}
	}

	return releasedCount, nil
}
