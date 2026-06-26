package saga

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"offlinepay/internal/domain"
	"offlinepay/internal/repository"

	"github.com/google/uuid"
)

// Saga Step constants
const (
	StepReserve      = "RESERVE"
	StepConsumeToken = "CONSUME_TOKEN"
	StepLedgerCommit = "LEDGER_COMMIT"
	StepPublish      = "PUBLISH"
)

type StepAction func(ctx context.Context, tx *sql.Tx) error
type CompensateAction func(ctx context.Context, tx *sql.Tx) error

type SagaStep struct {
	Name       string
	Action     StepAction
	Compensate CompensateAction
}

type Orchestrator struct {
	repo *repository.Repository
}

func NewOrchestrator(repo *repository.Repository) *Orchestrator {
	return &Orchestrator{repo: repo}
}

// Execute runs a sequence of steps, executing compensations in reverse order on failure
func (o *Orchestrator) Execute(ctx context.Context, txnID string, steps []SagaStep) error {
	sagaID := uuid.New().String()
	slog.Info("starting saga orchestration", "saga_id", sagaID, "txn_id", txnID)

	state := &domain.SagaState{
		SagaID:    sagaID,
		TxnID:     txnID,
		Status:    domain.SagaStatusStarted,
		Step:      "NONE",
		UpdatedAt: time.Now(),
	}

	err := o.repo.CreateSagaState(ctx, nil, state)
	if err != nil {
		return fmt.Errorf("failed to create saga tracking state: %w", err)
	}

	var executedSteps []SagaStep

	for _, step := range steps {
		slog.Info("executing saga step", "saga_id", sagaID, "step", step.Name)
		
		// Update Saga step tracking
		_ = o.repo.UpdateSagaState(ctx, nil, sagaID, domain.SagaStatusStarted, step.Name)

		// Run step inside a database transaction
		err = o.repo.WithTx(ctx, func(tx *sql.Tx) error {
			return step.Action(ctx, tx)
		})

		if err != nil {
			slog.Error("saga step failed, initiating compensations", "saga_id", sagaID, "failed_step", step.Name, "error", err)
			o.compensate(ctx, sagaID, txnID, executedSteps)
			return fmt.Errorf("saga aborted on step %s: %w", step.Name, err)
		}

		executedSteps = append(executedSteps, step)
	}

	// Update saga state to COMPLETED
	_ = o.repo.UpdateSagaState(ctx, nil, sagaID, domain.SagaStatusCompleted, "DONE")
	slog.Info("saga orchestration completed successfully", "saga_id", sagaID, "txn_id", txnID)
	return nil
}

func (o *Orchestrator) compensate(ctx context.Context, sagaID string, txnID string, executedSteps []SagaStep) {
	slog.Warn("saga entering compensation phase", "saga_id", sagaID, "txn_id", txnID)
	_ = o.repo.UpdateSagaState(ctx, nil, sagaID, domain.SagaStatusCompensating, "ROLLBACK")

	// Compensate in reverse order
	for i := len(executedSteps) - 1; i >= 0; i-- {
		step := executedSteps[i]
		if step.Compensate == nil {
			continue
		}

		slog.Info("compensating saga step", "saga_id", sagaID, "step", step.Name)

		// Compensation must succeed, so we retry with basic backoff if database transient issues occur
		backoff := 50 * time.Millisecond
		maxRetries := 3
		var compErr error

		for attempt := 1; attempt <= maxRetries; attempt++ {
			compErr = o.repo.WithTx(ctx, func(tx *sql.Tx) error {
				return step.Compensate(ctx, tx)
			})

			if compErr == nil {
				break
			}

			slog.Warn("compensation step failed, retrying...", "saga_id", sagaID, "step", step.Name, "attempt", attempt, "error", compErr)
			time.Sleep(backoff)
			backoff *= 2
		}

		if compErr != nil {
			slog.Error("FATAL: saga compensation step failed after retries. Manual intervention required!", "saga_id", sagaID, "step", step.Name, "error", compErr)
			_ = o.repo.UpdateSagaState(ctx, nil, sagaID, domain.SagaStatusFailed, "CRITICAL_"+step.Name)
			return
		}
	}

	_ = o.repo.UpdateSagaState(ctx, nil, sagaID, domain.SagaStatusFailed, "COMPENSATED")
	slog.Info("saga compensation completed successfully", "saga_id", sagaID)
}
