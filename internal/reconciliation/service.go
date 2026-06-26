package reconciliation

import (
	"context"
	"log/slog"
	"time"

	"offlinepay/internal/domain"
	"offlinepay/internal/observability"
	"offlinepay/internal/repository"
	"offlinepay/internal/token"
)

type Service struct {
	repo         *repository.Repository
	tokenService *token.Service
	interval     time.Duration
}

func NewService(repo *repository.Repository, tokenService *token.Service, interval time.Duration) *Service {
	if interval == 0 {
		interval = 5 * time.Second
	}
	return &Service{
		repo:         repo,
		tokenService: tokenService,
		interval:     interval,
	}
}

func (s *Service) Start(ctx context.Context) {
	slog.InfoContext(ctx, "starting reconciliation background worker", "interval", s.interval)
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.InfoContext(ctx, "stopping reconciliation worker")
			return
		case <-ticker.C:
			slog.InfoContext(ctx, "reconciliation scan cycle started")
			s.runScan(ctx)
		}
	}
}

func (s *Service) runScan(ctx context.Context) {
	ctx = observability.WithTraceID(ctx, "reconciliation-scan-"+time.Now().Format("150405"))

	// 1. Release expired tokens
	releasedCount, err := s.tokenService.ReleaseExpiredTokens(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "reconciliation failed to release expired tokens", "error", err)
	} else if releasedCount > 0 {
		slog.InfoContext(ctx, "reconciliation released expired tokens", "count", releasedCount)
		observability.ReconciliationRepairsTotal.Add(float64(releasedCount))
	}

	// 2. Scan for stuck transactions
	cutoff := time.Now().Add(-10 * time.Second)
	stuckIntents, err := s.repo.GetStuckPaymentIntents(ctx, cutoff)
	if err != nil {
		slog.ErrorContext(ctx, "reconciliation failed to scan for stuck intents", "error", err)
	} else {
		for _, intent := range stuckIntents {
			intentCtx := observability.WithTxnID(ctx, intent.TxnID)
			intentCtx = observability.WithDeviceID(intentCtx, intent.DeviceID)
			intentCtx = observability.WithTokenID(intentCtx, intent.TokenID)

			slog.WarnContext(intentCtx, "reconciliation detected stuck payment intent",
				"status", intent.Status,
				"age_seconds", time.Since(intent.UpdatedAt).Seconds(),
			)
			
			if time.Now().After(intent.Expiry) {
				slog.InfoContext(intentCtx, "reconciliation auto-failing stuck expired transaction")
				reason := "expired_during_relay"
				err = s.repo.UpdatePaymentIntentStatus(intentCtx, nil, intent.TxnID, domain.StateExpired, &reason)
				if err != nil {
					slog.ErrorContext(intentCtx, "failed to update status of stuck intent", "error", err)
				}
				observability.ReconciliationRepairsTotal.Inc()
			}
		}
	}

	// 3. Scan for ledger inconsistencies (Debits vs Credits mismatch)
	if err := s.verifyLedgerConsistency(ctx); err != nil {
		slog.ErrorContext(ctx, "CRITICAL: ledger consistency check failed", "error", err)
	}

	// 4. Update queue metrics
	dlqCount, err := s.repo.GetDeadLetterEventsCount(ctx)
	if err == nil {
		observability.DLQSize.Set(float64(dlqCount))
	}
	outboxCount, err := s.repo.GetPendingOutboxEventsCount(ctx)
	if err == nil {
		observability.OutboxQueueSize.Set(float64(outboxCount))
	}
}

func (s *Service) verifyLedgerConsistency(ctx context.Context) error {
	imbalances, err := s.repo.CheckLedgerInconsistencies(ctx)
	if err != nil {
		return err
	}
	for txnID, imbalance := range imbalances {
		txnCtx := observability.WithTxnID(ctx, txnID)
		slog.ErrorContext(txnCtx, "CRITICAL: Ledger entry imbalance detected!", "imbalance_amount", imbalance)
		observability.ReconciliationRepairsTotal.Inc()
	}
	return nil
}
