package outbox

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand"
	"sync"
	"time"

	"offlinepay/internal/domain"
	"offlinepay/internal/eventbus"
	"offlinepay/internal/observability"
	"offlinepay/internal/repository"
)

type OutboxWorker struct {
	repo        *repository.Repository
	eventBus    *eventbus.EventBus
	interval    time.Duration
	workerCount int
	maxRetries  int
}

func NewOutboxWorker(repo *repository.Repository, eventBus *eventbus.EventBus, interval time.Duration) *OutboxWorker {
	if interval == 0 {
		interval = 200 * time.Millisecond
	}
	return &OutboxWorker{
		repo:        repo,
		eventBus:    eventBus,
		interval:    interval,
		workerCount: 3, // pool of 3 concurrent workers
		maxRetries:  5,
	}
}

func (w *OutboxWorker) Start(ctx context.Context) {
	slog.Info("starting concurrent transactional outbox worker pool", "workers", w.workerCount, "interval", w.interval)

	var wg sync.WaitGroup
	for i := 1; i <= w.workerCount; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			w.runWorkerLoop(ctx, fmt.Sprintf("worker-%d", workerID))
		}(i)
	}

	wg.Wait()
	slog.Info("outbox worker pool stopped")
}

func (w *OutboxWorker) runWorkerLoop(ctx context.Context, workerID string) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.DebugContext(ctx, "outbox worker shutting down", "worker_id", workerID)
			return
		case <-ticker.C:
			w.processOutboxBatch(ctx, workerID)
		}
	}
}

func (w *OutboxWorker) processOutboxBatch(ctx context.Context, workerID string) {
	var events []*domain.OutboxEvent
	var err error

	// Step 1: Lock and acquire events using SKIP LOCKED in a short transaction
	err = w.repo.WithTx(ctx, func(tx *sql.Tx) error {
		events, err = w.repo.GetPendingOutboxEventsForUpdate(ctx, tx, 5, workerID)
		return err
	})

	if err != nil {
		slog.ErrorContext(ctx, "failed to acquire pending outbox events", "worker_id", workerID, "error", err)
		return
	}

	if len(events) == 0 {
		return
	}

	slog.DebugContext(ctx, "acquired outbox events batch", "worker_id", workerID, "count", len(events))

	// Step 2: Process events outside DB transaction to avoid blocking DB connections during Redis I/O
	for _, ev := range events {
		w.processSingleEvent(ctx, ev)
	}
}

func (w *OutboxWorker) processSingleEvent(ctx context.Context, ev *domain.OutboxEvent) {
	// Inject outbox event parameters into context for context-aware structured logging
	ctx = observability.WithTxnID(ctx, fmt.Sprintf("%d", ev.EventID))
	slog.DebugContext(ctx, "publishing event from outbox", "stream", ev.StreamName)

	// Publish to Redis
	err := w.eventBus.Publish(ctx, ev.StreamName, ev.EventType, ev.Payload, fmt.Sprintf("%d", ev.EventID))
	if err == nil {
		// Step 3a: Success -> Mark published in database
		err = w.repo.MarkOutboxEventPublished(ctx, nil, ev.EventID)
		if err != nil {
			slog.ErrorContext(ctx, "failed to mark outbox event as published", "event_id", ev.EventID, "error", err)
		}
		return
	}

	// Step 3b: Failure -> Backoff, increment retry count or route to DLQ
	slog.WarnContext(ctx, "failed to publish outbox event", "event_id", ev.EventID, "stream", ev.StreamName, "error", err)

	err = w.repo.WithTx(ctx, func(tx *sql.Tx) error {
		errFailed := w.repo.MarkOutboxEventFailed(ctx, tx, ev.EventID, err.Error(), w.maxRetries)
		if errFailed != nil {
			return errFailed
		}

		var status string
		var retryCount int
		row := tx.QueryRowContext(ctx, "SELECT status, retry_count FROM outbox_events WHERE event_id = $1", ev.EventID)
		if errScan := row.Scan(&status, &retryCount); errScan == nil {
			if status == "FAILED" {
				slog.ErrorContext(ctx, "outbox event exceeded retry limits, routing to dead letter queue (DLQ)", "event_id", ev.EventID, "retries", retryCount)
				
				dlqEvent := &domain.DeadLetterEvent{
					Payload:       ev.Payload,
					FailureReason: fmt.Sprintf("failed after %d retries: %s", retryCount, err.Error()),
					RetryCount:    retryCount,
					Timestamp:     time.Now(),
				}
				
				errDLQ := w.repo.CreateDeadLetterEvent(ctx, tx, dlqEvent)
				if errDLQ != nil {
					return fmt.Errorf("failed to write DLQ event: %w", errDLQ)
				}

				// Remove from outbox
				_, errDel := tx.ExecContext(ctx, "DELETE FROM outbox_events WHERE event_id = $1", ev.EventID)
				if errDel != nil {
					return fmt.Errorf("failed to delete failed outbox event: %w", errDel)
				}
			}
		}
		return nil
	})

	if err != nil {
		slog.ErrorContext(ctx, "failed to handle outbox event failure state", "event_id", ev.EventID, "error", err)
	}
}

// ReplayDLQEvent pulls an event from the DLQ and puts it back into the outbox
func (w *OutboxWorker) ReplayDLQEvent(ctx context.Context, eventID int64) error {
	return w.repo.WithTx(ctx, func(tx *sql.Tx) error {
		dlq, err := w.repo.GetDeadLetterEvent(ctx, eventID)
		if err != nil {
			return err
		}

		var metadata struct {
			StreamName string `json:"stream_name"`
			EventType  string `json:"event_type"`
		}
		_ = json.Unmarshal([]byte(dlq.Payload), &metadata)
		if metadata.StreamName == "" {
			metadata.StreamName = "payment.replayed"
			metadata.EventType = "PaymentReplayed"
		}

		query := `
			INSERT INTO outbox_events (stream_name, event_type, payload, status, retry_count)
			VALUES ($1, $2, $3, 'PENDING', 0)
		`
		_, err = tx.ExecContext(ctx, query, metadata.StreamName, metadata.EventType, dlq.Payload)
		if err != nil {
			return err
		}

		_, err = tx.ExecContext(ctx, "DELETE FROM dead_letter_events WHERE event_id = $1", eventID)
		return err
	})
}

// FullJitterBackoff sleep utility
func FullJitterBackoff(attempt int, base, max time.Duration) {
	backoff := base * time.Duration(1<<uint(attempt))
	if backoff > max {
		backoff = max
	}
	sleep := time.Duration(rand.Float64() * float64(backoff))
	time.Sleep(sleep)
}
