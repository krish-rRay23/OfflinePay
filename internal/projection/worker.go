package projection

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"strconv"
	"time"

	"offlinepay/internal/domain"
	"offlinepay/internal/eventbus"
	"offlinepay/internal/observability"
	"offlinepay/internal/repository"
)

type ProjectionWorker struct {
	repo     *repository.Repository
	eventBus *eventbus.EventBus
}

func NewProjectionWorker(repo *repository.Repository, eventBus *eventbus.EventBus) *ProjectionWorker {
	return &ProjectionWorker{
		repo:     repo,
		eventBus: eventBus,
	}
}

func (w *ProjectionWorker) Start(ctx context.Context) {
	slog.InfoContext(ctx, "starting projection worker")

	w.eventBus.Subscribe(ctx, "payment.settled", "projections-group", "projection-worker-1", w.HandlePaymentSettled)
	w.eventBus.Subscribe(ctx, "payment.failed", "projections-group", "projection-worker-1", w.HandlePaymentFailed)

	go w.runLagExporterLoop(ctx)
}

func (w *ProjectionWorker) HandlePaymentSettled(eventID string, eventType string, payload string) error {
	ctx := context.Background()
	ctx = observability.WithTraceID(ctx, "proj-settled-"+eventID)

	var data struct {
		TxnID     string    `json:"txn_id"`
		Sender    string    `json:"sender"`
		Receiver  string    `json:"receiver"`
		Amount    int64     `json:"amount"`
		TokenID   string    `json:"token_id"`
		SettledAt time.Time `json:"settled_at"`
		RelayHops int       `json:"relay_hops"`
	}

	if err := json.Unmarshal([]byte(payload), &data); err != nil {
		slog.ErrorContext(ctx, "failed to unmarshal payment.settled payload", "event_id", eventID, "error", err)
		return err
	}

	ctx = observability.WithTxnID(ctx, data.TxnID)
	ctx = observability.WithTokenID(ctx, data.TokenID)

	return w.repo.WithTx(ctx, func(tx *sql.Tx) error {
		processed, err := w.repo.IsEventProcessed(ctx, eventID)
		if err != nil {
			return err
		}
		if processed {
			slog.DebugContext(ctx, "payment.settled event already processed", "event_id", eventID)
			return nil
		}

		proj, err := w.repo.GetReadProjection(ctx, data.TxnID)
		if err != nil {
			proj = &domain.PaymentReadProjection{
				TxnID:      data.TxnID,
				SenderID:   data.Sender,
				ReceiverID: data.Receiver,
				Amount:     data.Amount,
			}
		}

		proj.Status = domain.StateSettled
		proj.RelayHops = data.RelayHops
		proj.SettledAt = &data.SettledAt
		proj.UpdatedAt = time.Now()

		err = w.repo.CreateOrUpdateReadProjection(ctx, tx, proj)
		if err != nil {
			return err
		}

		err = w.repo.MarkEventProcessed(ctx, tx, eventID)
		if err != nil {
			return err
		}

		if id, errParse := strconv.ParseInt(eventID, 10, 64); errParse == nil {
			err = w.repo.UpdateProjectionCheckpoint(ctx, tx, "payment-projection", id)
			if err != nil {
				return err
			}
		}

		slog.InfoContext(ctx, "processed payment.settled read projection", "txn_id", data.TxnID, "event_id", eventID)
		return nil
	})
}

func (w *ProjectionWorker) HandlePaymentFailed(eventID string, eventType string, payload string) error {
	ctx := context.Background()
	ctx = observability.WithTraceID(ctx, "proj-failed-"+eventID)

	var data struct {
		TxnID     string `json:"txn_id"`
		Status    string `json:"status"`
		Reason    string `json:"reason"`
		RelayHops int    `json:"relay_hops"`
	}

	if err := json.Unmarshal([]byte(payload), &data); err != nil {
		slog.ErrorContext(ctx, "failed to unmarshal payment.failed payload", "event_id", eventID, "error", err)
		return err
	}

	ctx = observability.WithTxnID(ctx, data.TxnID)

	return w.repo.WithTx(ctx, func(tx *sql.Tx) error {
		processed, err := w.repo.IsEventProcessed(ctx, eventID)
		if err != nil {
			return err
		}
		if processed {
			slog.DebugContext(ctx, "payment.failed event already processed", "event_id", eventID)
			return nil
		}

		proj, err := w.repo.GetReadProjection(ctx, data.TxnID)
		if err != nil {
			proj = &domain.PaymentReadProjection{
				TxnID:      data.TxnID,
				SenderID:   "UNKNOWN",
				ReceiverID: "UNKNOWN",
				Amount:     0,
			}
		}

		proj.Status = data.Status
		proj.RelayHops = data.RelayHops
		proj.UpdatedAt = time.Now()

		err = w.repo.CreateOrUpdateReadProjection(ctx, tx, proj)
		if err != nil {
			return err
		}

		err = w.repo.MarkEventProcessed(ctx, tx, eventID)
		if err != nil {
			return err
		}

		if id, errParse := strconv.ParseInt(eventID, 10, 64); errParse == nil {
			err = w.repo.UpdateProjectionCheckpoint(ctx, tx, "payment-projection", id)
			if err != nil {
				return err
			}
		}

		slog.InfoContext(ctx, "processed payment.failed read projection", "txn_id", data.TxnID, "event_id", eventID)
		return nil
	})
}

func (w *ProjectionWorker) runLagExporterLoop(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.exportLag(ctx)
		}
	}
}

func (w *ProjectionWorker) exportLag(ctx context.Context) {
	maxID, err := w.repo.GetLatestOutboxEventID(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "failed to get latest outbox event ID for lag calculation", "error", err)
		return
	}

	checkpointID, err := w.repo.GetProjectionCheckpoint(ctx, "payment-projection")
	if err != nil {
		slog.ErrorContext(ctx, "failed to get projection checkpoint for lag calculation", "error", err)
		return
	}

	lag := maxID - checkpointID
	if lag < 0 {
		lag = 0
	}

	observability.ProjectionLag.Set(float64(lag))
}
