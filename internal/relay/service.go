package relay

import (
	"context"
	"errors"
	"log/slog"
	"math/rand"
	"sync"
	"time"

	"offlinepay/internal/domain"
	"offlinepay/internal/observability"
	"offlinepay/internal/repository"
)

type SettlementClient interface {
	Settle(ctx context.Context, env *domain.EncryptedEnvelope, hopCount int) (string, error)
}

type Service struct {
	repo       *repository.Repository
	client     SettlementClient
	relayID    string
	retryLimit int
	dedupe     map[string]time.Time
	mu         sync.Mutex
}

func NewService(repo *repository.Repository, client SettlementClient, relayID string, retryLimit int) *Service {
	if retryLimit == 0 {
		retryLimit = 5
	}
	s := &Service{
		repo:       repo,
		client:     client,
		relayID:    relayID,
		retryLimit: retryLimit,
		dedupe:     make(map[string]time.Time),
	}

	// Periodically clean memory dedupe cache
	go s.cleanDedupeCacheLoop(2 * time.Minute)

	return s
}

// ReceiveAndRelay processes a incoming envelope, deduplicates it, and kicks off asynchronous forwarding
func (s *Service) ReceiveAndRelay(ctx context.Context, env *domain.EncryptedEnvelope, hopCount int) error {
	s.mu.Lock()
	_, exists := s.dedupe[env.TxnID]
	if !exists {
		_, exists = s.dedupe[env.Nonce]
	}
	if exists {
		s.mu.Unlock()
		slog.Info("relay service dropped duplicate envelope", "relay_id", s.relayID, "txn_id", env.TxnID)
		observability.RelayFailureTotal.Inc() // Count as a dropped/failure delivery to relay
		return errors.New("duplicate transaction or nonce detected by relay")
	}

	// Add to dedupe cache
	s.dedupe[env.TxnID] = time.Now()
	s.dedupe[env.Nonce] = time.Now()
	s.mu.Unlock()

	// Increment hop count
	hopCount++

	// Record attempt in database
	attempt := &domain.RelayAttempt{
		RelayID:  s.relayID,
		TxnID:    env.TxnID,
		HopCount: hopCount,
		Status:   "RECEIVED",
	}
	_ = s.repo.RecordRelayAttempt(ctx, attempt)

	// Async forward with backoff and retry budget
	go s.forwardWithRetry(context.Background(), env, hopCount)

	return nil
}

func (s *Service) forwardWithRetry(ctx context.Context, env *domain.EncryptedEnvelope, hopCount int) {
	backoff := 100 * time.Millisecond
	maxBackoff := 5 * time.Second

	for attempt := 1; attempt <= s.retryLimit; attempt++ {
		slog.Info("relay forwarding attempt starting", 
			"relay_id", s.relayID, 
			"txn_id", env.TxnID, 
			"attempt", attempt,
		)

		// Record in DB
		dbAttempt := &domain.RelayAttempt{
			RelayID:  s.relayID,
			TxnID:    env.TxnID,
			HopCount: hopCount,
			Status:   "FORWARDING",
		}
		_ = s.repo.RecordRelayAttempt(ctx, dbAttempt)

		settlementStatus, err := s.client.Settle(ctx, env, hopCount)
		if err == nil {
			slog.Info("relay forwarded successfully and received ACK", 
				"relay_id", s.relayID, 
				"txn_id", env.TxnID, 
				"settlement_status", settlementStatus,
			)
			dbAttempt.Status = "ACK_" + settlementStatus
			_ = s.repo.RecordRelayAttempt(ctx, dbAttempt)
			observability.RelaySuccessTotal.Inc()
			return
		}

		slog.Warn("relay forward attempt failed", 
			"relay_id", s.relayID, 
			"txn_id", env.TxnID, 
			"attempt", attempt, 
			"error", err,
		)

		if attempt == s.retryLimit {
			dbAttempt.Status = "NACK_FAILED"
			_ = s.repo.RecordRelayAttempt(ctx, dbAttempt)
			observability.RelayFailureTotal.Inc()
			return
		}

		// Calculate exponential backoff with random jitter (full jitter formula)
		jitter := time.Duration(rand.Int63n(int64(backoff)))
		sleepTime := backoff + jitter
		if sleepTime > maxBackoff {
			sleepTime = maxBackoff
		}

		time.Sleep(sleepTime)
		backoff *= 2
	}
}

func (s *Service) cleanDedupeCacheLoop(interval time.Duration) {
	ticker := time.NewTicker(interval)
	for range ticker.C {
		s.mu.Lock()
		now := time.Now()
		for key, addedAt := range s.dedupe {
			if now.Sub(addedAt) > 10*time.Minute {
				delete(s.dedupe, key)
			}
		}
		s.mu.Unlock()
	}
}
