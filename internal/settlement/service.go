package settlement

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"offlinepay/internal/crypto"
	"offlinepay/internal/domain"
	"offlinepay/internal/eventsource"
	"offlinepay/internal/intent"
	"offlinepay/internal/observability"
	"offlinepay/internal/repository"
	"offlinepay/internal/risk"
	"offlinepay/internal/saga"
)

type Service struct {
	repo             *repository.Repository
	bankPrivateKey   *ecdsa.PrivateKey
	bankPublicKey    *ecdsa.PublicKey
	riskEngine       *risk.RiskEngine
	sagaOrchestrator *saga.Orchestrator
	eventSourceMgr   *eventsource.EventSourceManager
}

type staticReader struct{}

func (staticReader) Read(p []byte) (n int, err error) {
	for i := range p {
		p[i] = byte(i % 256)
	}
	return len(p), nil
}

func NewService(repo *repository.Repository, bankKey *ecdsa.PrivateKey, riskEng *risk.RiskEngine) *Service {
	if bankKey == nil {
		mockKey, err := crypto.GenerateKeyPair()
		if err != nil {
			slog.Warn("crypto.GenerateKeyPair failed, falling back to deterministic key generation", "error", err)
			mockKey, _ = ecdsa.GenerateKey(elliptic.P256(), staticReader{})
		}
		bankKey = mockKey
	}

	return &Service{
		repo:             repo,
		bankPrivateKey:   bankKey,
		bankPublicKey:    &bankKey.PublicKey,
		riskEngine:       riskEng,
		sagaOrchestrator: saga.NewOrchestrator(repo),
		eventSourceMgr:   eventsource.NewEventSourceManager(repo, 5), // Snapshot every 5 events
	}
}

// Settle decrypts, validates, and atomically settles the offline payment envelope using a Saga orchestrator
func (s *Service) Settle(ctx context.Context, env *domain.EncryptedEnvelope, hopCount int) (string, error) {
	startTime := time.Now()
	defer func() {
		observability.SettlementLatency.Observe(float64(time.Since(startTime).Milliseconds()))
	}()

	slog.Info("settlement request received", "txn_id", env.TxnID, "nonce", env.Nonce, "hop_count", hopCount)

	// 1. Idempotency Check / In-Progress Lock
	existing, err := s.repo.GetPaymentIntent(ctx, env.TxnID)
	if err == nil {
		if existing.Status == domain.StateSettled || existing.Status == domain.StateRejected || existing.Status == domain.StateFailed || existing.Status == domain.StateDuplicate {
			slog.Info("idempotency match: transaction already processed", "txn_id", env.TxnID, "status", existing.Status)
			observability.SettlementsTotal.WithLabelValues("IDEMPOTENT_FAIL").Inc()
			return domain.StateDuplicate, fmt.Errorf("transaction already processed with status: %s", existing.Status)
		}
	}

	// 2. Decrypt Envelope
	decryptedBytes, err := crypto.DecryptECIES(s.bankPrivateKey, env.EphemeralPublicKey, env.Ciphertext, env.IV, env.AuthTag)
	if err != nil {
		slog.Error("ECIES decryption failed", "txn_id", env.TxnID, "error", err)
		s.saveFailedIntent(ctx, env, domain.StateRejected, "decryption_failure", hopCount)
		observability.SettlementsTotal.WithLabelValues("DECRYPTION_FAILED").Inc()
		return domain.StateRejected, fmt.Errorf("decryption failure: %w", err)
	}

	// 3. Unmarshal Payload
	var signedIntent intent.SignedPaymentIntent
	if err := json.Unmarshal(decryptedBytes, &signedIntent); err != nil {
		slog.Error("failed to unmarshal decrypted payment intent", "txn_id", env.TxnID, "error", err)
		s.saveFailedIntent(ctx, env, domain.StateRejected, "malformed_json_payload", hopCount)
		return domain.StateRejected, fmt.Errorf("malformed json payload: %w", err)
	}

	payPayload := &signedIntent.Payload

	// 4. Device Attestation Check
	att, err := s.repo.GetDeviceAttestation(ctx, payPayload.DeviceID)
	if err == nil && att != nil {
		if att.TrustLevel == "UNTRUSTED" {
			slog.Warn("rejected transaction: device attestation trust level is UNTRUSTED", "device_id", payPayload.DeviceID, "txn_id", env.TxnID)
			s.saveFailedIntent(ctx, env, domain.StateRejected, "device_attestation_untrusted", hopCount)
			return domain.StateRejected, errors.New("device attestation verification failed: untrusted level")
		}
	}

	// 5. Validate Device Identity and Status
	device, err := s.repo.GetDevice(ctx, payPayload.DeviceID)
	if err != nil {
		slog.Error("device lookup failed", "device_id", payPayload.DeviceID, "txn_id", env.TxnID, "error", err)
		s.saveFailedIntent(ctx, env, domain.StateRejected, "device_not_found", hopCount)
		return domain.StateRejected, fmt.Errorf("device not found: %w", err)
	}

	if device.Status == domain.DeviceRevoked || device.Status == domain.DeviceCompromised {
		slog.Warn("rejected transaction from revoked/compromised device", "device_id", device.DeviceID, "status", device.Status, "txn_id", env.TxnID)
		s.saveFailedIntent(ctx, env, domain.StateRejected, "device_revoked_or_compromised", hopCount)
		return domain.StateRejected, fmt.Errorf("device is revoked/compromised (status: %s)", device.Status)
	}

	// 6. Verify Device Signature on Payload
	devicePub, err := crypto.ParsePEMToPublicKey(device.PublicKey)
	if err != nil {
		slog.Error("failed to parse device public key PEM", "device_id", device.DeviceID, "error", err)
		s.saveFailedIntent(ctx, env, domain.StateRejected, "invalid_device_public_key", hopCount)
		return domain.StateRejected, err
	}

	canonicalIntentData := intent.GetCanonicalPayloadString(payPayload)
	if !crypto.Verify(devicePub, []byte(canonicalIntentData), signedIntent.DeviceSignature) {
		slog.Warn("invalid device signature", "txn_id", env.TxnID, "device_id", device.DeviceID)
		s.saveFailedIntent(ctx, env, domain.StateRejected, "invalid_device_signature", hopCount)
		return domain.StateRejected, errors.New("invalid device signature")
	}

	// 7. Verify Transaction Expiry
	if time.Now().After(payPayload.Expiry) {
		slog.Warn("transaction expired", "txn_id", env.TxnID, "expiry", payPayload.Expiry)
		s.saveFailedIntent(ctx, env, domain.StateExpired, "transaction_expired", hopCount)
		return domain.StateExpired, errors.New("transaction expired")
	}

	// 8. Verify Offline Spending Token
	if payPayload.TokenID == "" {
		slog.Warn("transaction rejected: missing offline spending token", "txn_id", env.TxnID)
		s.saveFailedIntent(ctx, env, domain.StateRejected, "missing_offline_token", hopCount)
		return domain.StateRejected, errors.New("offline transactions require a spending token")
	}

	token, err := s.repo.GetToken(ctx, payPayload.TokenID)
	if err != nil {
		slog.Error("offline token lookup failed", "token_id", payPayload.TokenID, "txn_id", env.TxnID, "error", err)
		s.saveFailedIntent(ctx, env, domain.StateRejected, "token_not_found", hopCount)
		return domain.StateRejected, fmt.Errorf("token not found: %w", err)
	}

	// Verify bank signature on token fields
	canonicalTokenData := fmt.Sprintf("token:%s:owner:%s:value:%d:expiry:%d", token.TokenID, token.OwnerID, token.Value, token.Expiry.Unix())
	if !crypto.Verify(s.bankPublicKey, []byte(canonicalTokenData), token.TokenSignature) {
		slog.Warn("invalid bank signature on offline token", "token_id", token.TokenID, "txn_id", env.TxnID)
		s.saveFailedIntent(ctx, env, domain.StateRejected, "invalid_token_signature", hopCount)
		return domain.StateRejected, errors.New("invalid bank signature on token")
	}

	// Verify token owner matches sender
	if token.OwnerID != payPayload.SenderID {
		slog.Warn("token owner mismatch", "token_owner", token.OwnerID, "sender_id", payPayload.SenderID, "txn_id", env.TxnID)
		s.saveFailedIntent(ctx, env, domain.StateRejected, "token_owner_mismatch", hopCount)
		return domain.StateRejected, errors.New("token owner does not match sender")
	}

	// Verify token expiry
	if time.Now().After(token.Expiry) {
		slog.Warn("token expired", "token_id", token.TokenID, "expiry", token.Expiry)
		s.saveFailedIntent(ctx, env, domain.StateRejected, "token_expired", hopCount)
		return domain.StateRejected, errors.New("spending token expired")
	}

	// Verify token is not already consumed
	if token.Consumed {
		slog.Warn("token double spend attempt detected", "token_id", token.TokenID, "txn_id", env.TxnID)
		s.saveFailedIntent(ctx, env, domain.StateDuplicate, "token_already_consumed", hopCount)
		observability.ReplayRejectionsTotal.Inc()
		return domain.StateDuplicate, errors.New("spending token already consumed")
	}

	// Verify token value covers intent amount
	if token.Value < payPayload.Amount {
		slog.Warn("insufficient token value", "token_value", token.Value, "amount", payPayload.Amount, "txn_id", env.TxnID)
		s.saveFailedIntent(ctx, env, domain.StateRejected, "insufficient_token_value", hopCount)
		return domain.StateRejected, fmt.Errorf("token value %d is less than payment amount %d", token.Value, payPayload.Amount)
	}

	// 9. Risk Engine Score
	recentFailures := 0
	assessment := s.riskEngine.Assess(ctx, device, payPayload, hopCount, recentFailures, 0, 0)
	if assessment.Action == risk.ActionReject {
		slog.Warn("risk engine rejected transaction", "txn_id", env.TxnID, "reason", assessment.Reason)
		s.saveFailedIntent(ctx, env, domain.StateRejected, "risk_rejection: "+assessment.Reason, hopCount)
		return domain.StateRejected, fmt.Errorf("transaction rejected by risk engine: %s", assessment.Reason)
	}

	// 10. Build and Run Saga Orchestration Steps
	changeAmount := token.Value - payPayload.Amount

	steps := []saga.SagaStep{
		{
			Name: saga.StepReserve,
			Action: func(ctx context.Context, tx *sql.Tx) error {
				// Lock accounts in alphabetical order to avoid deadlocks
				accounts := []string{payPayload.SenderID, payPayload.ReceiverID}
				sort.Strings(accounts)

				accountBalances := make(map[string]*domain.AccountBalance)
				for _, accID := range accounts {
					bal, err := s.repo.GetBalanceForUpdate(ctx, tx, accID)
					if err != nil {
						return fmt.Errorf("failed to lock balance for account %s: %w", accID, err)
					}
					accountBalances[accID] = bal
				}

				senderBal := accountBalances[payPayload.SenderID]

				// Assert that the reservation exists (since the token was pre-reserved)
				if senderBal.ReservedBalance < token.Value {
					return fmt.Errorf("insufficient reserved balance: has %d, token reserved %d", senderBal.ReservedBalance, token.Value)
				}

				// Create reserved intent row first to satisfy foreign key constraint on nonce_registry
				signatureHash := "sig-hash-" + payPayload.TxnID
				intentModel := &domain.PaymentIntent{
					TxnID:         payPayload.TxnID,
					SenderID:      payPayload.SenderID,
					ReceiverID:    payPayload.ReceiverID,
					Amount:        payPayload.Amount,
					Currency:      payPayload.Currency,
					Nonce:         payPayload.Nonce,
					Expiry:        payPayload.Expiry,
					Status:        domain.StateReserved,
					DeviceID:      payPayload.DeviceID,
					TokenID:       payPayload.TokenID,
					RelayHops:     hopCount,
					SignatureHash: signatureHash,
					CreatedAt:     time.Now(),
					UpdatedAt:     time.Now(),
				}
				err = s.repo.CreatePaymentIntent(ctx, tx, intentModel)
				if err != nil {
					return fmt.Errorf("failed to create reservation intent: %w", err)
				}

				// Register Nonce (anti-replay check)
				err = s.repo.RegisterNonce(ctx, tx, payPayload.Nonce, env.TxnID, payPayload.Expiry)
				if err != nil {
					return fmt.Errorf("nonce_reused: %w", err)
				}

				return nil
			},
			Compensate: func(ctx context.Context, tx *sql.Tx) error {
				// Remove the registered nonce and the reserved payment intent
				_, err1 := tx.ExecContext(ctx, "DELETE FROM nonce_registry WHERE nonce = $1", payPayload.Nonce)
				var currentStatus string
				errStatus := tx.QueryRowContext(ctx, "SELECT status FROM payment_intents WHERE txn_id = $1 FOR UPDATE", payPayload.TxnID).Scan(&currentStatus)
				if errStatus == nil && currentStatus == domain.StateReserved {
					_, _ = tx.ExecContext(ctx, "DELETE FROM payment_intents WHERE txn_id = $1", payPayload.TxnID)
				}
				return err1
			},
		},
		{
			Name: saga.StepConsumeToken,
			Action: func(ctx context.Context, tx *sql.Tx) error {
				// Lock token row
				tokLocked, err := s.repo.GetTokenForUpdate(ctx, tx, payPayload.TokenID)
				if err != nil {
					return err
				}
				if tokLocked.Status != "ISSUED" {
					return fmt.Errorf("token state mismatch: expected ISSUED, got %s", tokLocked.Status)
				}

				// Transition to HELD
				return s.repo.HoldToken(ctx, tx, payPayload.TokenID)
			},
			Compensate: func(ctx context.Context, tx *sql.Tx) error {
				// Transition to INVALIDATED if the token is still in HELD status
				var status string
				err := tx.QueryRowContext(ctx, "SELECT status FROM offline_tokens WHERE token_id = $1 FOR UPDATE", payPayload.TokenID).Scan(&status)
				if err == nil && status == "HELD" {
					return s.repo.InvalidateToken(ctx, tx, payPayload.TokenID)
				}
				return nil
			},
		},
		{
			Name: saga.StepLedgerCommit,
			Action: func(ctx context.Context, tx *sql.Tx) error {
				// Consume token (HELD -> CONSUMED)
				err := s.repo.ConsumeToken(ctx, tx, payPayload.TokenID)
				if err != nil {
					return err
				}

				// Fetch current locked balances in alphabetical order to avoid deadlocks
				accounts := []string{payPayload.SenderID, payPayload.ReceiverID}
				sort.Strings(accounts)
				accountBalances := make(map[string]*domain.AccountBalance)
				for _, accID := range accounts {
					bal, err := s.repo.GetBalanceForUpdate(ctx, tx, accID)
					if err != nil {
						return err
					}
					accountBalances[accID] = bal
				}
				senderBal := accountBalances[payPayload.SenderID]
				receiverBal := accountBalances[payPayload.ReceiverID]

				// Move money: decrease reserved balance, return change to sender, add payment to receiver available
				newSenderReserved := senderBal.ReservedBalance - token.Value
				newSenderAvailable := senderBal.AvailableBalance + changeAmount
				err = s.repo.UpdateBalance(ctx, tx, payPayload.SenderID, newSenderAvailable, newSenderReserved)
				if err != nil {
					return err
				}

				newReceiverAvailable := receiverBal.AvailableBalance + payPayload.Amount
				err = s.repo.UpdateBalance(ctx, tx, payPayload.ReceiverID, newReceiverAvailable, receiverBal.ReservedBalance)
				if err != nil {
					return err
				}

				// Update payment intent status to settled
				err = s.repo.UpdatePaymentIntentStatus(ctx, tx, payPayload.TxnID, domain.StateSettled, nil)
				if err != nil {
					return err
				}

				// Write Double-Entry Ledger
				senderLedger := &domain.LedgerEntry{
					TxnID:        payPayload.TxnID,
					AccountID:    payPayload.SenderID,
					Direction:    domain.DirectionDebit,
					Amount:       payPayload.Amount,
					EntryType:    domain.EntryTypeSettlement,
					BalanceAfter: newSenderAvailable,
				}
				err = s.repo.CreateLedgerEntry(ctx, tx, senderLedger)
				if err != nil {
					return err
				}

				receiverLedger := &domain.LedgerEntry{
					TxnID:        payPayload.TxnID,
					AccountID:    payPayload.ReceiverID,
					Direction:    domain.DirectionCredit,
					Amount:       payPayload.Amount,
					EntryType:    domain.EntryTypeSettlement,
					BalanceAfter: newReceiverAvailable,
				}
				err = s.repo.CreateLedgerEntry(ctx, tx, receiverLedger)
				if err != nil {
					return err
				}

				// Write balancing ledger entry for the pre-reserved token resolution
				releaseLedger := &domain.LedgerEntry{
					TxnID:        "tok-res-" + payPayload.TokenID,
					AccountID:    payPayload.SenderID,
					Direction:    domain.DirectionCredit,
					Amount:       token.Value,
					EntryType:    domain.EntryTypeRelease,
					BalanceAfter: newSenderAvailable,
				}
				err = s.repo.CreateLedgerEntry(ctx, tx, releaseLedger)
				if err != nil {
					return err
				}

				return nil
			},
			Compensate: func(ctx context.Context, tx *sql.Tx) error {
				// Revert balances, locking in alphabetical order to avoid deadlocks
				accounts := []string{payPayload.SenderID, payPayload.ReceiverID}
				sort.Strings(accounts)
				accountBalances := make(map[string]*domain.AccountBalance)
				for _, accID := range accounts {
					bal, err := s.repo.GetBalanceForUpdate(ctx, tx, accID)
					if err != nil {
						return err
					}
					accountBalances[accID] = bal
				}
				senderBal := accountBalances[payPayload.SenderID]
				receiverBal := accountBalances[payPayload.ReceiverID]

				// Move money back: increase reserved balance, subtract change from sender available, subtract payment from receiver
				newSenderReserved := senderBal.ReservedBalance + token.Value
				newSenderAvailable := senderBal.AvailableBalance - changeAmount
				err = s.repo.UpdateBalance(ctx, tx, payPayload.SenderID, newSenderAvailable, newSenderReserved)
				if err != nil {
					return err
				}

				newReceiverAvailable := receiverBal.AvailableBalance - payPayload.Amount
				err = s.repo.UpdateBalance(ctx, tx, payPayload.ReceiverID, newReceiverAvailable, receiverBal.ReservedBalance)
				if err != nil {
					return err
				}

				// Delete payment intent and ledger entries
				_, err = tx.ExecContext(ctx, "DELETE FROM ledger_entries WHERE txn_id = $1 OR (txn_id = $2 AND entry_type = 'RELEASE')", payPayload.TxnID, "tok-res-"+payPayload.TokenID)
				if err != nil {
					return err
				}
				_, err = tx.ExecContext(ctx, "DELETE FROM payment_intents WHERE txn_id = $1", payPayload.TxnID)
				return err
			},
		},
		{
			Name: saga.StepPublish,
			Action: func(ctx context.Context, tx *sql.Tx) error {
				// Audit Log
				auditPayload, _ := json.Marshal(map[string]interface{}{
					"intent":        payPayload,
					"token_value":   token.Value,
					"change_amount": changeAmount,
				})
				audit := &domain.AuditEvent{
					TxnID:     payPayload.TxnID,
					EventType: "PaymentSettled",
					Payload:   string(auditPayload),
				}
				err = s.repo.CreateAuditEvent(ctx, tx, audit)
				if err != nil {
					return err
				}

				// Transactional Outbox
				outboxPayload, _ := json.Marshal(map[string]interface{}{
					"txn_id":     payPayload.TxnID,
					"sender":     payPayload.SenderID,
					"receiver":   payPayload.ReceiverID,
					"amount":     payPayload.Amount,
					"token_id":   payPayload.TokenID,
					"settled_at": time.Now(),
					"relay_hops": hopCount,
				})
				outbox := &domain.OutboxEvent{
					StreamName: "payment.settled",
					EventType:  "PaymentSettled",
					Payload:    string(outboxPayload),
				}
				err = s.repo.CreateOutboxEvent(ctx, tx, outbox)
				if err != nil {
					return err
				}

				// Versioned Event Sourcing Logging (Event Version 1)
				intentModel, err := s.repo.GetPaymentIntent(ctx, payPayload.TxnID)
				if err == nil {
					// We save events and snap them using the raw map object to avoid double serialization (marshal/unmarshal failure)
					err = s.eventSourceMgr.SaveEventAndSnapshot(ctx, tx, payPayload.TxnID, "IntentSettled", 1, map[string]interface{}{
						"txn_id":     payPayload.TxnID,
						"sender":     payPayload.SenderID,
						"receiver":   payPayload.ReceiverID,
						"amount":     payPayload.Amount,
						"token_id":   payPayload.TokenID,
						"settled_at": time.Now(),
						"relay_hops": hopCount,
					}, intentModel)
					if err != nil {
						return err
					}
				}

				return nil
			},
			Compensate: func(ctx context.Context, tx *sql.Tx) error {
				// Delete audit logs, outbox events, and event store records
				_, err = tx.ExecContext(ctx, "DELETE FROM audit_events WHERE txn_id = $1", payPayload.TxnID)
				if err != nil {
					return err
				}
				_, err = tx.ExecContext(ctx, "DELETE FROM outbox_events WHERE payload LIKE $1", "%"+payPayload.TxnID+"%")
				if err != nil {
					return err
				}
				_, err = tx.ExecContext(ctx, "DELETE FROM payment_events WHERE txn_id = $1", payPayload.TxnID)
				return err
			},
		},
	}

	// 11. Run Saga
	err = s.sagaOrchestrator.Execute(ctx, payPayload.TxnID, steps)
	if err != nil {
		slog.Error("saga execution failed, transaction rolled back", "txn_id", env.TxnID, "error", err)

		failStatus := domain.StateFailed
		reason := err.Error()

		errStr := err.Error()
		if errors.Is(err, sql.ErrNoRows) || errStr == "nonce_reused" || strings.Contains(errStr, "nonce_reused") {
			failStatus = domain.StateDuplicate
			reason = "nonce_reused"
		} else if errStr == "token_already_consumed_concurrent" || strings.Contains(errStr, "token state mismatch") || strings.Contains(errStr, "insufficient reserved balance") {
			failStatus = domain.StateDuplicate
			reason = "token_already_consumed"
			err = errors.New("spending token already consumed")
		} else if strings.Contains(errStr, "duplicate key") || strings.Contains(errStr, "unique constraint") {
			failStatus = domain.StateDuplicate
			reason = "transaction_already_processed"
			err = errors.New("transaction already processed")
		}

		s.saveFailedIntent(ctx, env, failStatus, reason, hopCount)
		observability.SettlementsTotal.WithLabelValues("ATOMIC_SAGA_FAILED").Inc()
		return failStatus, err
	}

	slog.Info("settlement completed successfully via Saga Orchestrator", "txn_id", env.TxnID)
	observability.SettlementsTotal.WithLabelValues("SUCCESS").Inc()
	observability.TokenConsumptionTotal.Inc()

	return domain.StateSettled, nil
}

func (s *Service) saveFailedIntent(ctx context.Context, env *domain.EncryptedEnvelope, status string, reason string, hopCount int) {
	intentModel := &domain.PaymentIntent{
		TxnID:         env.TxnID,
		SenderID:      "UNKNOWN",
		ReceiverID:    "UNKNOWN",
		Amount:        0,
		Currency:      "USD",
		Nonce:         env.Nonce,
		Expiry:        time.Now().Add(1 * time.Hour),
		Status:        status,
		DeviceID:      "UNKNOWN",
		TokenID:       "UNKNOWN",
		FailureReason: &reason,
		RelayHops:     hopCount,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}

	_ = s.repo.CreatePaymentIntent(ctx, nil, intentModel)

	// Create Outbox Event for failed intent
	failedPayload, _ := json.Marshal(map[string]interface{}{
		"txn_id":     env.TxnID,
		"status":     status,
		"reason":     reason,
		"relay_hops": hopCount,
	})
	outbox := &domain.OutboxEvent{
		StreamName: "payment.failed",
		EventType:  "PaymentFailed",
		Payload:    string(failedPayload),
	}
	_ = s.repo.CreateOutboxEvent(ctx, nil, outbox)

	// Save Event Sourcing fail event with full parameters
	payloadES := map[string]interface{}{
		"reason":     reason,
		"status":     status,
		"relay_hops": hopCount,
	}
	_ = s.eventSourceMgr.SaveEventAndSnapshot(ctx, nil, env.TxnID, "IntentFailed", 1, payloadES, intentModel)
}
