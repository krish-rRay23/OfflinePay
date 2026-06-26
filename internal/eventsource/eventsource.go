package eventsource

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"offlinepay/internal/domain"
	"offlinepay/internal/repository"
)

type EventSourceManager struct {
	repo             *repository.Repository
	snapshotInterval int
}

func NewEventSourceManager(repo *repository.Repository, snapshotInterval int) *EventSourceManager {
	if snapshotInterval <= 0 {
		snapshotInterval = 5 // smaller default for simulation visibility
	}
	return &EventSourceManager{
		repo:             repo,
		snapshotInterval: snapshotInterval,
	}
}

// SaveEventAndSnapshot saves a versioned state change event. Every N events, it creates an aggregate state snapshot.
func (esm *EventSourceManager) SaveEventAndSnapshot(
	ctx context.Context,
	tx *sql.Tx,
	txnID string,
	eventType string,
	eventVersion int,
	payload interface{},
	currentAggregate *domain.PaymentIntent,
) error {
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal event payload: %w", err)
	}

	event := &domain.PaymentEvent{
		TxnID:        txnID,
		EventType:    eventType,
		EventVersion: eventVersion,
		Payload:      string(payloadJSON),
		CreatedAt:    time.Now(),
	}

	// 1. Save Event
	err = esm.repo.CreatePaymentEvent(ctx, tx, event)
	if err != nil {
		return fmt.Errorf("failed to save versioned event: %w", err)
	}

	// 2. Snapshot every N events
	if eventVersion%esm.snapshotInterval == 0 && currentAggregate != nil {
		snapshotData, err := json.Marshal(currentAggregate)
		if err != nil {
			return fmt.Errorf("failed to marshal snapshot: %w", err)
		}

		snap := &domain.PaymentSnapshot{
			AggregateID:      txnID,
			AggregateVersion: eventVersion,
			SnapshotData:     string(snapshotData),
			CreatedAt:        time.Now(),
		}

		err = esm.repo.CreatePaymentSnapshot(ctx, tx, snap)
		if err != nil {
			return fmt.Errorf("failed to save aggregate snapshot: %w", err)
		}
		slog.Info("saved aggregate snapshot", "aggregate_id", txnID, "version", eventVersion)
	}

	return nil
}

// Replay loads the latest snapshot and replays subsequent events to reconstruct the payment intent state
func (esm *EventSourceManager) Replay(ctx context.Context, txnID string) (*domain.PaymentIntent, error) {
	// 1. Try to load the latest snapshot
	snap, err := esm.repo.GetPaymentSnapshot(ctx, txnID)
	if err != nil {
		return nil, fmt.Errorf("failed to load snapshot: %w", err)
	}

	var intent domain.PaymentIntent
	startVersion := 0

	if snap != nil {
		err = json.Unmarshal([]byte(snap.SnapshotData), &intent)
		if err != nil {
			return nil, fmt.Errorf("failed to unmarshal snapshot data: %w", err)
		}
		startVersion = snap.AggregateVersion
		slog.Debug("replaying from snapshot", "aggregate_id", txnID, "version", startVersion)
	}

	// 2. Retrieve all events after the snapshot version
	events, err := esm.repo.GetPaymentEvents(ctx, txnID)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve events: %w", err)
	}

	slog.Debug("replaying events", "aggregate_id", txnID, "total_events", len(events), "after_version", startVersion)

	for _, ev := range events {
		if ev.EventVersion <= startVersion {
			continue
		}

		// Apply the event to update the state
		if err := esm.applyEvent(&intent, ev); err != nil {
			return nil, fmt.Errorf("failed to apply event version %d: %w", ev.EventVersion, err)
		}
	}

	// If no events or snapshots existed, returning a zero struct might mean not found
	if intent.TxnID == "" && len(events) == 0 {
		return nil, sql.ErrNoRows
	}

	return &intent, nil
}

func (esm *EventSourceManager) applyEvent(intent *domain.PaymentIntent, ev *domain.PaymentEvent) error {
	// Reconstruct state aggregate changes based on event types
	intent.UpdatedAt = ev.CreatedAt

	switch ev.EventType {
	case "IntentCreated":
		var p domain.PaymentIntentPayload
		if err := json.Unmarshal([]byte(ev.Payload), &p); err != nil {
			return err
		}
		intent.TxnID = p.TxnID
		intent.SenderID = p.SenderID
		intent.ReceiverID = p.ReceiverID
		intent.Amount = p.Amount
		intent.Currency = p.Currency
		intent.Nonce = p.Nonce
		intent.Expiry = p.Expiry
		intent.DeviceID = p.DeviceID
		intent.TokenID = p.TokenID
		intent.Status = domain.StateCreated
		intent.CreatedAt = ev.CreatedAt

	case "IntentSigned":
		intent.Status = domain.StateSigned

	case "IntentEncrypted":
		intent.Status = domain.StateEncrypted

	case "IntentRelayed":
		var m map[string]interface{}
		_ = json.Unmarshal([]byte(ev.Payload), &m)
		if hops, ok := m["hop_count"].(float64); ok {
			intent.RelayHops = int(hops)
		}
		intent.Status = domain.StateRelayed

	case "IntentValidated":
		intent.Status = domain.StateValidated

	case "IntentReserved":
		intent.Status = domain.StateReserved

	case "IntentSettled":
		now := ev.CreatedAt
		intent.SettledAt = &now
		intent.Status = domain.StateSettled

	case "IntentRejected":
		var m map[string]interface{}
		_ = json.Unmarshal([]byte(ev.Payload), &m)
		if reason, ok := m["reason"].(string); ok {
			intent.FailureReason = &reason
		}
		now := ev.CreatedAt
		intent.RejectedAt = &now
		intent.Status = domain.StateRejected

	case "IntentFailed":
		var m map[string]interface{}
		_ = json.Unmarshal([]byte(ev.Payload), &m)
		if reason, ok := m["reason"].(string); ok {
			intent.FailureReason = &reason
		}
		now := ev.CreatedAt
		intent.RejectedAt = &now
		intent.Status = domain.StateFailed
	}

	return nil
}
