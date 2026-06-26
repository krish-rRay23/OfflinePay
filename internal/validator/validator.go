package validator

import (
	"context"
	"log/slog"
	"time"

	"offlinepay/internal/observability"
	"offlinepay/internal/repository"
)

type FinancialValidator struct {
	repo     *repository.Repository
	interval time.Duration
}

func NewFinancialValidator(repo *repository.Repository, interval time.Duration) *FinancialValidator {
	if interval == 0 {
		interval = 5 * time.Second
	}
	return &FinancialValidator{
		repo:     repo,
		interval: interval,
	}
}

func (fv *FinancialValidator) Start(ctx context.Context) {
	slog.InfoContext(ctx, "starting continuous financial integrity validator worker", "interval", fv.interval)
	ticker := time.NewTicker(fv.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.InfoContext(ctx, "stopping financial validator worker")
			return
		case <-ticker.C:
			fv.RunAudit(ctx)
		}
	}
}

// RunAudit scans ledger and token tables to detect financial inconsistencies
func (fv *FinancialValidator) RunAudit(ctx context.Context) {
	ctx = observability.WithTraceID(ctx, "financial-validator-audit-"+time.Now().Format("150405"))
	slog.DebugContext(ctx, "financial integrity audit cycle started")

	balanced := 1.0

	// 1. Audit Check: SUM(Debits) == SUM(Credits)
	imbalances, err := fv.repo.CheckLedgerInconsistencies(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "financial validator query failed", "error", err)
		return
	}

	for txnID, imbalance := range imbalances {
		balanced = 0.0
		txnCtx := observability.WithTxnID(ctx, txnID)
		slog.ErrorContext(txnCtx, "ALERT: Ledger imbalance detected! Financial invariant broken!",
			"imbalance_amount", imbalance,
		)
	}

	// 2. Audit Check: Account Available Balance Parity
	discrepancies, err := fv.repo.AuditAccountBalances(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "financial validator failed to audit account balances", "error", err)
		return
	}

	for _, d := range discrepancies {
		balanced = 0.0
		accountCtx := observability.WithTraceID(ctx, "validator-account-"+d.AccountID)
		slog.ErrorContext(accountCtx, "ALERT: Account balance discrepancy detected! Financial invariant broken!",
			"account_id", d.AccountID,
			"expected_available", d.ExpectedAvailable,
			"actual_available", d.ActualAvailable,
			"expected_reserved", d.ExpectedReserved,
			"actual_reserved", d.ActualReserved,
			"discrepancy_amount", d.DiscrepancyAmount,
		)
	}

	observability.LedgerValidationStatus.Set(balanced)
}
