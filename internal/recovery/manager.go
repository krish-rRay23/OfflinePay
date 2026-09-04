package recovery

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"offlinepay/internal/domain"
	"offlinepay/internal/repository"

	"github.com/google/uuid"
)

// Manager persists a diagnosis before scheduling any recovery work. It publishes
// only through the transactional outbox, so a Redis failure cannot lose a decision.
type Manager struct {
	repo       *repository.Repository
	classifier Classifier
	policy     Policy
	now        func() time.Time
}

func NewManager(repo *repository.Repository, classifier Classifier, policy Policy) (*Manager, error) {
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	if classifier == nil {
		classifier = RulesFirstClassifier{}
	}
	return &Manager{repo: repo, classifier: classifier, policy: policy, now: time.Now}, nil
}

// RecordFailure is idempotent for each transaction/action. The operation row is
// the write-ahead record; no financial side effect is issued from this method.
func (m *Manager) RecordFailure(ctx context.Context, event FailureEvent) (Decision, error) {
	if event.TxnID == "" || event.Amount < 0 {
		return Decision{}, fmt.Errorf("invalid failure event")
	}
	if event.ID == "" {
		event.ID = uuid.NewString()
	}
	if event.OccurredAt.IsZero() {
		event.OccurredAt = m.now()
	}
	classification := m.classifier.Classify(event)
	decision := m.policy.Decide(event, classification, 0, m.now())
	state := stateFor(decision.Action)
	key := IdempotencyKey(event.TxnID, decision.Action)
	return decision, m.repo.WithTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO failure_events (failure_id, txn_id, category, code, message, confidence, classifier_source, occurred_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT (failure_id) DO NOTHING`, event.ID, event.TxnID, classification.Category, event.Code, event.Message, classification.Confidence, classification.Source, event.OccurredAt)
		if err != nil {
			return err
		}
		var recoveryID string
		err = tx.QueryRowContext(ctx, `INSERT INTO recovery_operations (recovery_id, txn_id, idempotency_key, action, state, attempt_count, next_attempt_at) VALUES ($1,$2,$3,$4,$5,0,$6) ON CONFLICT (idempotency_key) DO NOTHING RETURNING recovery_id`, uuid.NewString(), event.TxnID, key, decision.Action, state, scheduledAt(decision.Action, m.policy, m.now())).Scan(&recoveryID)
		if err == sql.ErrNoRows {
			// The write-ahead operation already exists; do not duplicate outbox work.
			return nil
		}
		if err != nil {
			return err
		}
		return m.appendDecision(ctx, tx, event.TxnID, recoveryID, decision, classification)
	})
}
func scheduledAt(action Action, p Policy, now time.Time) any {
	if action == ActionRetry || action == ActionCompensate {
		return p.NextAttempt(1, now)
	}
	return nil
}
func stateFor(action Action) State {
	switch action {
	case ActionRetry, ActionCompensate:
		return StateScheduled
	case ActionExhausted:
		return StateExhausted
	default:
		return StateEscalated
	}
}
func (m *Manager) appendDecision(ctx context.Context, tx *sql.Tx, txnID, recoveryID string, d Decision, c Classification) error {
	payload, _ := json.Marshal(map[string]any{"recovery_id": recoveryID, "action": d.Action, "reason": d.Reason, "classification": c})
	if err := m.repo.CreateAuditEvent(ctx, tx, &domain.AuditEvent{TxnID: txnID, EventType: "RecoveryDiagnosed", Payload: string(payload)}); err != nil {
		return err
	}
	if err := m.repo.CreatePaymentEvent(ctx, tx, &domain.PaymentEvent{TxnID: txnID, EventType: "RecoveryDiagnosed", EventVersion: 1, Payload: string(payload), CreatedAt: m.now()}); err != nil {
		return err
	}
	if err := m.repo.CreateOutboxEvent(ctx, tx, &domain.OutboxEvent{StreamName: "payment.recovery", EventType: "RecoveryDiagnosed", Payload: string(payload)}); err != nil {
		return err
	}
	return nil
}

// RunOnce claims due recovery operations with SKIP LOCKED. It never calls a bank
// API; it emits a request for the normal relay/settlement path, which retains all
// nonce, token, risk, saga, and ledger checks.
func (m *Manager) RunOnce(ctx context.Context, limit int) error {
	if limit <= 0 {
		limit = 50
	}
	return m.repo.WithTx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT recovery_id, txn_id, action, attempt_count FROM recovery_operations WHERE state = 'SCHEDULED' AND next_attempt_at <= CURRENT_TIMESTAMP ORDER BY next_attempt_at FOR UPDATE SKIP LOCKED LIMIT $1`, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id, txnID string
			var action Action
			var attempts int
			if err := rows.Scan(&id, &txnID, &action, &attempts); err != nil {
				return err
			}
			if attempts >= m.policy.MaxAttempts {
				if _, err := tx.ExecContext(ctx, `UPDATE recovery_operations SET state='EXHAUSTED', next_attempt_at=NULL, updated_at=CURRENT_TIMESTAMP WHERE recovery_id=$1`, id); err != nil {
					return err
				}
				continue
			}
			attempts++
			payload, _ := json.Marshal(map[string]any{"recovery_id": id, "txn_id": txnID, "action": action, "attempt": attempts, "idempotency_key": IdempotencyKey(txnID, action)})
			eventType := "RecoveryRetryRequested"
			terminal := "SCHEDULED"
			next := m.policy.NextAttempt(attempts+1, m.now())
			if attempts == m.policy.MaxAttempts {
				terminal = "EXHAUSTED"
				next = time.Time{}
			}
			if action == ActionCompensate {
				eventType = "RecoveryCompensationReviewRequested"
				terminal = "ESCALATED"
				next = time.Time{}
			}
			if err := m.repo.CreateOutboxEvent(ctx, tx, &domain.OutboxEvent{StreamName: "payment.recovery", EventType: eventType, Payload: string(payload)}); err != nil {
				return err
			}
			if terminal == "SCHEDULED" {
				_, err = tx.ExecContext(ctx, `UPDATE recovery_operations SET state=$1,attempt_count=$2,next_attempt_at=$3,updated_at=CURRENT_TIMESTAMP WHERE recovery_id=$4`, terminal, attempts, next, id)
			} else {
				_, err = tx.ExecContext(ctx, `UPDATE recovery_operations SET state=$1,attempt_count=$2,next_attempt_at=NULL,updated_at=CURRENT_TIMESTAMP WHERE recovery_id=$3`, terminal, attempts, id)
			}
			if err != nil {
				return err
			}
		}
		return rows.Err()
	})
}
