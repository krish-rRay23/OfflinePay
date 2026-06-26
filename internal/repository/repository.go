package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"offlinepay/internal/chaos"
	"offlinepay/internal/db"
	"offlinepay/internal/domain"

	"github.com/lib/pq"
)

type Repository struct {
	db *db.DB
}

func NewRepository(database *db.DB) *Repository {
	return &Repository{db: database}
}

// Transaction helper
func (r *Repository) WithTx(ctx context.Context, fn func(*sql.Tx) error) error {
	if chaos.GetController().IsPostgresOffline() {
		return errors.New("database connection down (simulated chaos)")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// --- DEVICES ---

func (r *Repository) CreateDevice(ctx context.Context, dev *domain.Device) error {
	query := `
		INSERT INTO devices (device_id, owner_id, public_key, trust_score, status, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`
	_, err := r.db.ExecContext(ctx, query, dev.DeviceID, dev.OwnerID, dev.PublicKey, dev.TrustScore, dev.Status, dev.CreatedAt)
	return err
}

func (r *Repository) GetDevice(ctx context.Context, deviceID string) (*domain.Device, error) {
	query := `
		SELECT device_id, owner_id, public_key, trust_score, status, created_at, revoked_at
		FROM devices WHERE device_id = $1
	`
	row := r.db.QueryRowContext(ctx, query, deviceID)
	var dev domain.Device
	err := row.Scan(&dev.DeviceID, &dev.OwnerID, &dev.PublicKey, &dev.TrustScore, &dev.Status, &dev.CreatedAt, &dev.RevokedAt)
	if err != nil {
		return nil, err
	}
	return &dev, nil
}

func (r *Repository) UpdateDeviceStatus(ctx context.Context, deviceID string, status string) error {
	var query string
	if status == domain.DeviceRevoked || status == domain.DeviceCompromised {
		query = `UPDATE devices SET status = $1, revoked_at = CURRENT_TIMESTAMP WHERE device_id = $2`
	} else {
		query = `UPDATE devices SET status = $1, revoked_at = NULL WHERE device_id = $2`
	}
	_, err := r.db.ExecContext(ctx, query, status, deviceID)
	return err
}

// --- OFFLINE TOKENS ---

func (r *Repository) CreateToken(ctx context.Context, tx *sql.Tx, tok *domain.OfflineToken) error {
	if tok.Status == "" {
		tok.Status = "ISSUED"
	}
	query := `
		INSERT INTO offline_tokens (token_id, owner_id, value, expiry, consumed, consumed_at, token_signature, reserved_at, risk_score_at_issue, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`
	var err error
	if tx != nil {
		_, err = tx.ExecContext(ctx, query, tok.TokenID, tok.OwnerID, tok.Value, tok.Expiry, tok.Consumed, tok.ConsumedAt, tok.TokenSignature, tok.ReservedAt, tok.RiskScoreAtIssue, tok.Status)
	} else {
		_, err = r.db.ExecContext(ctx, query, tok.TokenID, tok.OwnerID, tok.Value, tok.Expiry, tok.Consumed, tok.ConsumedAt, tok.TokenSignature, tok.ReservedAt, tok.RiskScoreAtIssue, tok.Status)
	}
	return err
}

func (r *Repository) GetToken(ctx context.Context, tokenID string) (*domain.OfflineToken, error) {
	query := `
		SELECT token_id, owner_id, value, expiry, consumed, consumed_at, token_signature, reserved_at, released_at, risk_score_at_issue, status
		FROM offline_tokens WHERE token_id = $1
	`
	row := r.db.QueryRowContext(ctx, query, tokenID)
	var tok domain.OfflineToken
	err := row.Scan(&tok.TokenID, &tok.OwnerID, &tok.Value, &tok.Expiry, &tok.Consumed, &tok.ConsumedAt, &tok.TokenSignature, &tok.ReservedAt, &tok.ReleasedAt, &tok.RiskScoreAtIssue, &tok.Status)
	if err != nil {
		return nil, err
	}
	return &tok, nil
}

func (r *Repository) GetTokenForUpdate(ctx context.Context, tx *sql.Tx, tokenID string) (*domain.OfflineToken, error) {
	query := `
		SELECT token_id, owner_id, value, expiry, consumed, consumed_at, token_signature, reserved_at, released_at, risk_score_at_issue, status
		FROM offline_tokens WHERE token_id = $1 FOR UPDATE
	`
	row := tx.QueryRowContext(ctx, query, tokenID)
	var tok domain.OfflineToken
	err := row.Scan(&tok.TokenID, &tok.OwnerID, &tok.Value, &tok.Expiry, &tok.Consumed, &tok.ConsumedAt, &tok.TokenSignature, &tok.ReservedAt, &tok.ReleasedAt, &tok.RiskScoreAtIssue, &tok.Status)
	if err != nil {
		return nil, err
	}
	return &tok, nil
}

func (r *Repository) HoldToken(ctx context.Context, tx *sql.Tx, tokenID string) error {
	var status string
	querySelect := `SELECT status FROM offline_tokens WHERE token_id = $1 FOR UPDATE`
	var err error
	if tx != nil {
		err = tx.QueryRowContext(ctx, querySelect, tokenID).Scan(&status)
	} else {
		err = r.db.QueryRowContext(ctx, querySelect, tokenID).Scan(&status)
	}
	if err != nil {
		return err
	}

	if !domain.IsValidTokenTransition(status, "HELD") {
		return fmt.Errorf("invalid token status transition from %s to HELD", status)
	}

	queryUpdate := `UPDATE offline_tokens SET status = 'HELD' WHERE token_id = $1`
	if tx != nil {
		_, err = tx.ExecContext(ctx, queryUpdate, tokenID)
	} else {
		_, err = r.db.ExecContext(ctx, queryUpdate, tokenID)
	}
	return err
}

func (r *Repository) InvalidateToken(ctx context.Context, tx *sql.Tx, tokenID string) error {
	var status string
	querySelect := `SELECT status FROM offline_tokens WHERE token_id = $1 FOR UPDATE`
	var err error
	if tx != nil {
		err = tx.QueryRowContext(ctx, querySelect, tokenID).Scan(&status)
	} else {
		err = r.db.QueryRowContext(ctx, querySelect, tokenID).Scan(&status)
	}
	if err != nil {
		return err
	}

	if !domain.IsValidTokenTransition(status, "INVALIDATED") {
		return fmt.Errorf("invalid token status transition from %s to INVALIDATED", status)
	}

	queryUpdate := `UPDATE offline_tokens SET status = 'INVALIDATED', released_at = CURRENT_TIMESTAMP WHERE token_id = $1`
	if tx != nil {
		_, err = tx.ExecContext(ctx, queryUpdate, tokenID)
	} else {
		_, err = r.db.ExecContext(ctx, queryUpdate, tokenID)
	}
	return err
}

func (r *Repository) ConsumeToken(ctx context.Context, tx *sql.Tx, tokenID string) error {
	var status string
	querySelect := `SELECT status FROM offline_tokens WHERE token_id = $1 FOR UPDATE`
	var err error
	if tx != nil {
		err = tx.QueryRowContext(ctx, querySelect, tokenID).Scan(&status)
	} else {
		err = r.db.QueryRowContext(ctx, querySelect, tokenID).Scan(&status)
	}
	if err != nil {
		return err
	}

	if !domain.IsValidTokenTransition(status, "CONSUMED") {
		return fmt.Errorf("invalid token status transition from %s to CONSUMED", status)
	}

	query := `
		UPDATE offline_tokens
		SET consumed = TRUE, consumed_at = CURRENT_TIMESTAMP, status = 'CONSUMED'
		WHERE token_id = $1
	`
	var res sql.Result
	if tx != nil {
		res, err = tx.ExecContext(ctx, query, tokenID)
	} else {
		res, err = r.db.ExecContext(ctx, query, tokenID)
	}
	if err != nil {
		return err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return errors.New("token not found")
	}
	return nil
}

func (r *Repository) UnconsumeToken(ctx context.Context, tx *sql.Tx, tokenID string) error {
	return errors.New("token state machine cannot go backward: unconsume disabled")
}

func (r *Repository) ReleaseToken(ctx context.Context, tx *sql.Tx, tokenID string) error {
	var status string
	querySelect := `SELECT status FROM offline_tokens WHERE token_id = $1 FOR UPDATE`
	var err error
	if tx != nil {
		err = tx.QueryRowContext(ctx, querySelect, tokenID).Scan(&status)
	} else {
		err = r.db.QueryRowContext(ctx, querySelect, tokenID).Scan(&status)
	}
	if err != nil {
		return err
	}

	if !domain.IsValidTokenTransition(status, "INVALIDATED") {
		return fmt.Errorf("invalid token status transition from %s to INVALIDATED", status)
	}

	query := `
		UPDATE offline_tokens
		SET released_at = CURRENT_TIMESTAMP, status = 'INVALIDATED'
		WHERE token_id = $1
	`
	var res sql.Result
	if tx != nil {
		res, err = tx.ExecContext(ctx, query, tokenID)
	} else {
		res, err = r.db.ExecContext(ctx, query, tokenID)
	}
	if err != nil {
		return err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return errors.New("token not found")
	}
	return nil
}

func (r *Repository) GetExpiredTokens(ctx context.Context) ([]*domain.OfflineToken, error) {
	query := `
		SELECT token_id, owner_id, value, expiry, consumed, consumed_at, token_signature, reserved_at, released_at, risk_score_at_issue
		FROM offline_tokens
		WHERE expiry < CURRENT_TIMESTAMP AND consumed = FALSE AND released_at IS NULL
	`
	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tokens []*domain.OfflineToken
	for rows.Next() {
		var tok domain.OfflineToken
		err := rows.Scan(&tok.TokenID, &tok.OwnerID, &tok.Value, &tok.Expiry, &tok.Consumed, &tok.ConsumedAt, &tok.TokenSignature, &tok.ReservedAt, &tok.ReleasedAt, &tok.RiskScoreAtIssue)
		if err != nil {
			return nil, err
		}
		tokens = append(tokens, &tok)
	}
	return tokens, nil
}

// --- PAYMENT INTENTS ---

func (r *Repository) CreatePaymentIntent(ctx context.Context, tx *sql.Tx, intent *domain.PaymentIntent) error {
	query := `
		INSERT INTO payment_intents (txn_id, sender_id, receiver_id, amount, currency, nonce, expiry, status, device_id, token_id, failure_reason, settled_at, rejected_at, relay_hops, signature_hash, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
	`
	var err error
	if tx != nil {
		_, err = tx.ExecContext(ctx, query, intent.TxnID, intent.SenderID, intent.ReceiverID, intent.Amount, intent.Currency, intent.Nonce, intent.Expiry, intent.Status, intent.DeviceID, intent.TokenID, intent.FailureReason, intent.SettledAt, intent.RejectedAt, intent.RelayHops, intent.SignatureHash, intent.CreatedAt, intent.UpdatedAt)
	} else {
		_, err = r.db.ExecContext(ctx, query, intent.TxnID, intent.SenderID, intent.ReceiverID, intent.Amount, intent.Currency, intent.Nonce, intent.Expiry, intent.Status, intent.DeviceID, intent.TokenID, intent.FailureReason, intent.SettledAt, intent.RejectedAt, intent.RelayHops, intent.SignatureHash, intent.CreatedAt, intent.UpdatedAt)
	}
	return err
}

func (r *Repository) GetPaymentIntent(ctx context.Context, txnID string) (*domain.PaymentIntent, error) {
	query := `
		SELECT txn_id, sender_id, receiver_id, amount, currency, nonce, expiry, status, device_id, token_id, failure_reason, settled_at, rejected_at, relay_hops, signature_hash, created_at, updated_at
		FROM payment_intents WHERE txn_id = $1
	`
	row := r.db.QueryRowContext(ctx, query, txnID)
	var intent domain.PaymentIntent
	err := row.Scan(&intent.TxnID, &intent.SenderID, &intent.ReceiverID, &intent.Amount, &intent.Currency, &intent.Nonce, &intent.Expiry, &intent.Status, &intent.DeviceID, &intent.TokenID, &intent.FailureReason, &intent.SettledAt, &intent.RejectedAt, &intent.RelayHops, &intent.SignatureHash, &intent.CreatedAt, &intent.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &intent, nil
}

func (r *Repository) GetPaymentIntentForUpdate(ctx context.Context, tx *sql.Tx, txnID string) (*domain.PaymentIntent, error) {
	query := `
		SELECT txn_id, sender_id, receiver_id, amount, currency, nonce, expiry, status, device_id, token_id, failure_reason, settled_at, rejected_at, relay_hops, signature_hash, created_at, updated_at
		FROM payment_intents WHERE txn_id = $1 FOR UPDATE
	`
	row := tx.QueryRowContext(ctx, query, txnID)
	var intent domain.PaymentIntent
	err := row.Scan(&intent.TxnID, &intent.SenderID, &intent.ReceiverID, &intent.Amount, &intent.Currency, &intent.Nonce, &intent.Expiry, &intent.Status, &intent.DeviceID, &intent.TokenID, &intent.FailureReason, &intent.SettledAt, &intent.RejectedAt, &intent.RelayHops, &intent.SignatureHash, &intent.CreatedAt, &intent.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &intent, nil
}

func (r *Repository) UpdatePaymentIntentStatus(ctx context.Context, tx *sql.Tx, txnID string, status string, failureReason *string) error {
	var query string
	now := time.Now()
	var err error

	if status == domain.StateSettled {
		query = `UPDATE payment_intents SET status = $1, settled_at = $2, updated_at = $3 WHERE txn_id = $4`
		if tx != nil {
			_, err = tx.ExecContext(ctx, query, status, now, now, txnID)
		} else {
			_, err = r.db.ExecContext(ctx, query, status, now, now, txnID)
		}
	} else if status == domain.StateRejected || status == domain.StateFailed {
		query = `UPDATE payment_intents SET status = $1, rejected_at = $2, failure_reason = $3, updated_at = $4 WHERE txn_id = $5`
		if tx != nil {
			_, err = tx.ExecContext(ctx, query, status, now, failureReason, now, txnID)
		} else {
			_, err = r.db.ExecContext(ctx, query, status, now, failureReason, now, txnID)
		}
	} else {
		query = `UPDATE payment_intents SET status = $1, updated_at = $2 WHERE txn_id = $3`
		if tx != nil {
			_, err = tx.ExecContext(ctx, query, status, now, txnID)
		} else {
			_, err = r.db.ExecContext(ctx, query, status, now, txnID)
		}
	}
	return err
}

func (r *Repository) IncrementRelayHops(ctx context.Context, txnID string) error {
	query := `UPDATE payment_intents SET relay_hops = relay_hops + 1, updated_at = CURRENT_TIMESTAMP WHERE txn_id = $1`
	_, err := r.db.ExecContext(ctx, query, txnID)
	return err
}

func (r *Repository) GetStuckPaymentIntents(ctx context.Context, cutoff time.Time) ([]*domain.PaymentIntent, error) {
	query := `
		SELECT txn_id, sender_id, receiver_id, amount, currency, nonce, expiry, status, device_id, token_id, failure_reason, settled_at, rejected_at, relay_hops, signature_hash, created_at, updated_at
		FROM payment_intents
		WHERE status IN ('CREATED', 'SIGNED', 'ENCRYPTED', 'BROADCAST', 'RELAYED', 'VALIDATED', 'RESERVED') AND updated_at < $1
	`
	rows, err := r.db.QueryContext(ctx, query, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var intents []*domain.PaymentIntent
	for rows.Next() {
		var intent domain.PaymentIntent
		err := rows.Scan(&intent.TxnID, &intent.SenderID, &intent.ReceiverID, &intent.Amount, &intent.Currency, &intent.Nonce, &intent.Expiry, &intent.Status, &intent.DeviceID, &intent.TokenID, &intent.FailureReason, &intent.SettledAt, &intent.RejectedAt, &intent.RelayHops, &intent.SignatureHash, &intent.CreatedAt, &intent.UpdatedAt)
		if err != nil {
			return nil, err
		}
		intents = append(intents, &intent)
	}
	return intents, nil
}

// --- NONCE REGISTRY ---

func (r *Repository) RegisterNonce(ctx context.Context, tx *sql.Tx, nonce string, txnID string, expiry time.Time) error {
	query := `
		INSERT INTO nonce_registry (nonce, txn_id, expiry)
		VALUES ($1, $2, $3)
	`
	var err error
	if tx != nil {
		_, err = tx.ExecContext(ctx, query, nonce, txnID, expiry)
	} else {
		_, err = r.db.ExecContext(ctx, query, nonce, txnID, expiry)
	}
	return err
}

func (r *Repository) CheckNonce(ctx context.Context, nonce string) (bool, error) {
	query := `SELECT EXISTS(SELECT 1 FROM nonce_registry WHERE nonce = $1)`
	var exists bool
	err := r.db.QueryRowContext(ctx, query, nonce).Scan(&exists)
	return exists, err
}

// --- ACCOUNT BALANCES & LEDGER ---

func (r *Repository) CreateAccount(ctx context.Context, accountID string, initialBalance int64) error {
	query := `
		INSERT INTO account_balances (account_id, available_balance, reserved_balance, updated_at)
		VALUES ($1, $2, 0, CURRENT_TIMESTAMP)
		ON CONFLICT (account_id) DO NOTHING
	`
	_, err := r.db.ExecContext(ctx, query, accountID, initialBalance)
	return err
}

func (r *Repository) GetBalance(ctx context.Context, accountID string) (*domain.AccountBalance, error) {
	query := `
		SELECT account_id, available_balance, reserved_balance, updated_at
		FROM account_balances WHERE account_id = $1
	`
	row := r.db.QueryRowContext(ctx, query, accountID)
	var bal domain.AccountBalance
	err := row.Scan(&bal.AccountID, &bal.AvailableBalance, &bal.ReservedBalance, &bal.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &bal, nil
}

func (r *Repository) GetBalanceForUpdate(ctx context.Context, tx *sql.Tx, accountID string) (*domain.AccountBalance, error) {
	query := `
		SELECT account_id, available_balance, reserved_balance, updated_at
		FROM account_balances WHERE account_id = $1 FOR UPDATE
	`
	row := tx.QueryRowContext(ctx, query, accountID)
	var bal domain.AccountBalance
	err := row.Scan(&bal.AccountID, &bal.AvailableBalance, &bal.ReservedBalance, &bal.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &bal, nil
}

func (r *Repository) UpdateBalance(ctx context.Context, tx *sql.Tx, accountID string, available int64, reserved int64) error {
	query := `
		UPDATE account_balances
		SET available_balance = $1, reserved_balance = $2, updated_at = CURRENT_TIMESTAMP
		WHERE account_id = $3
	`
	var err error
	if tx != nil {
		_, err = tx.ExecContext(ctx, query, available, reserved, accountID)
	} else {
		_, err = r.db.ExecContext(ctx, query, available, reserved, accountID)
	}
	return err
}

func (r *Repository) CreateLedgerEntry(ctx context.Context, tx *sql.Tx, entry *domain.LedgerEntry) error {
	query := `
		INSERT INTO ledger_entries (txn_id, account_id, direction, amount, entry_type, balance_after)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING entry_id, created_at
	`
	row := tx.QueryRowContext(ctx, query, entry.TxnID, entry.AccountID, entry.Direction, entry.Amount, entry.EntryType, entry.BalanceAfter)
	return row.Scan(&entry.EntryID, &entry.CreatedAt)
}

// --- RELAY ATTEMPTS ---

func (r *Repository) RecordRelayAttempt(ctx context.Context, attempt *domain.RelayAttempt) error {
	query := `
		INSERT INTO relay_attempts (relay_id, txn_id, hop_count, status, first_seen_at, last_seen_at)
		VALUES ($1, $2, $3, $4, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
		ON CONFLICT (relay_id, txn_id)
		DO UPDATE SET hop_count = EXCLUDED.hop_count, status = EXCLUDED.status, last_seen_at = CURRENT_TIMESTAMP
	`
	_, err := r.db.ExecContext(ctx, query, attempt.RelayID, attempt.TxnID, attempt.HopCount, attempt.Status)
	return err
}

func (r *Repository) GetRelayAttempts(ctx context.Context, txnID string) ([]*domain.RelayAttempt, error) {
	query := `
		SELECT relay_id, txn_id, hop_count, status, first_seen_at, last_seen_at
		FROM relay_attempts WHERE txn_id = $1
	`
	rows, err := r.db.QueryContext(ctx, query, txnID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var attempts []*domain.RelayAttempt
	for rows.Next() {
		var a domain.RelayAttempt
		err := rows.Scan(&a.RelayID, &a.TxnID, &a.HopCount, &a.Status, &a.FirstSeenAt, &a.LastSeenAt)
		if err != nil {
			return nil, err
		}
		attempts = append(attempts, &a)
	}
	return attempts, nil
}

// --- AUDIT EVENTS ---

func (r *Repository) CreateAuditEvent(ctx context.Context, tx *sql.Tx, event *domain.AuditEvent) error {
	query := `
		INSERT INTO audit_events (txn_id, event_type, payload)
		VALUES ($1, $2, $3)
		RETURNING event_id, created_at
	`
	var row *sql.Row
	if tx != nil {
		row = tx.QueryRowContext(ctx, query, event.TxnID, event.EventType, event.Payload)
	} else {
		row = r.db.QueryRowContext(ctx, query, event.TxnID, event.EventType, event.Payload)
	}
	return row.Scan(&event.EventID, &event.CreatedAt)
}

// --- OUTBOX EVENTS ---

func (r *Repository) CreateOutboxEvent(ctx context.Context, tx *sql.Tx, event *domain.OutboxEvent) error {
	query := `
		INSERT INTO outbox_events (stream_name, event_type, payload)
		VALUES ($1, $2, $3)
		RETURNING event_id, created_at
	`
	var row *sql.Row
	if tx != nil {
		row = tx.QueryRowContext(ctx, query, event.StreamName, event.EventType, event.Payload)
	} else {
		row = r.db.QueryRowContext(ctx, query, event.StreamName, event.EventType, event.Payload)
	}
	return row.Scan(&event.EventID, &event.CreatedAt)
}

func (r *Repository) GetUnpublishedOutboxEvents(ctx context.Context) ([]*domain.OutboxEvent, error) {
	query := `
		SELECT event_id, stream_name, event_type, payload, published, created_at
		FROM outbox_events
		WHERE published = FALSE
		ORDER BY event_id ASC
		LIMIT 100
	`
	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []*domain.OutboxEvent
	for rows.Next() {
		var ev domain.OutboxEvent
		err := rows.Scan(&ev.EventID, &ev.StreamName, &ev.EventType, &ev.Payload, &ev.Published, &ev.CreatedAt)
		if err != nil {
			return nil, err
		}
		events = append(events, &ev)
	}
	return events, nil
}

func (r *Repository) MarkOutboxEventsPublished(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	query := `
		UPDATE outbox_events
		SET published = TRUE
		WHERE event_id = ANY($1)
	`
	_, err := r.db.ExecContext(ctx, query, pq.Array(ids))
	return err
}

func (r *Repository) CheckLedgerInconsistencies(ctx context.Context) (map[string]int64, error) {
	query := `
		SELECT txn_id, SUM(CASE WHEN direction = 'DEBIT' THEN amount ELSE -amount END) as imbalance
		FROM ledger_entries
		GROUP BY txn_id
		HAVING SUM(CASE WHEN direction = 'DEBIT' THEN amount ELSE -amount END) != 0
	`
	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	imbalances := make(map[string]int64)
	for rows.Next() {
		var txnID string
		var imbalance int64
		if err := rows.Scan(&txnID, &imbalance); err != nil {
			return nil, err
		}
		imbalances[txnID] = imbalance
	}
	return imbalances, nil
}

// --- SAGA STATES ---

func (r *Repository) CreateSagaState(ctx context.Context, tx *sql.Tx, saga *domain.SagaState) error {
	query := `
		INSERT INTO saga_states (saga_id, txn_id, status, step, updated_at)
		VALUES ($1, $2, $3, $4, $5)
	`
	var err error
	if tx != nil {
		_, err = tx.ExecContext(ctx, query, saga.SagaID, saga.TxnID, saga.Status, saga.Step, saga.UpdatedAt)
	} else {
		_, err = r.db.ExecContext(ctx, query, saga.SagaID, saga.TxnID, saga.Status, saga.Step, saga.UpdatedAt)
	}
	return err
}

func (r *Repository) UpdateSagaState(ctx context.Context, tx *sql.Tx, sagaID string, status string, step string) error {
	query := `
		UPDATE saga_states
		SET status = $1, step = $2, updated_at = CURRENT_TIMESTAMP
		WHERE saga_id = $3
	`
	var err error
	if tx != nil {
		_, err = tx.ExecContext(ctx, query, status, step, sagaID)
	} else {
		_, err = r.db.ExecContext(ctx, query, status, step, sagaID)
	}
	return err
}

func (r *Repository) GetSagaState(ctx context.Context, sagaID string) (*domain.SagaState, error) {
	query := `
		SELECT saga_id, txn_id, status, step, updated_at
		FROM saga_states WHERE saga_id = $1
	`
	row := r.db.QueryRowContext(ctx, query, sagaID)
	var saga domain.SagaState
	err := row.Scan(&saga.SagaID, &saga.TxnID, &saga.Status, &saga.Step, &saga.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &saga, nil
}

// --- DEVICE ATTESTATIONS ---

func (r *Repository) CreateDeviceAttestation(ctx context.Context, att *domain.DeviceAttestation) error {
	query := `
		INSERT INTO device_attestations (device_id, attestation_type, attestation_hash, trust_level, created_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (device_id) DO UPDATE 
		SET attestation_type = EXCLUDED.attestation_type, attestation_hash = EXCLUDED.attestation_hash, trust_level = EXCLUDED.trust_level, created_at = EXCLUDED.created_at
	`
	_, err := r.db.ExecContext(ctx, query, att.DeviceID, att.AttestationType, att.AttestationHash, att.TrustLevel, att.CreatedAt)
	return err
}

func (r *Repository) GetDeviceAttestation(ctx context.Context, deviceID string) (*domain.DeviceAttestation, error) {
	query := `
		SELECT device_id, attestation_type, attestation_hash, trust_level, created_at
		FROM device_attestations WHERE device_id = $1
	`
	row := r.db.QueryRowContext(ctx, query, deviceID)
	var att domain.DeviceAttestation
	err := row.Scan(&att.DeviceID, &att.AttestationType, &att.AttestationHash, &att.TrustLevel, &att.CreatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil // Return nil if attestation not found, so attestation can be optional
		}
		return nil, err
	}
	return &att, nil
}

// --- EVENT SOURCING ---

func (r *Repository) CreatePaymentEvent(ctx context.Context, tx *sql.Tx, ev *domain.PaymentEvent) error {
	query := `
		INSERT INTO payment_events (txn_id, event_type, event_version, payload, created_at)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING event_id
	`
	var row *sql.Row
	if tx != nil {
		row = tx.QueryRowContext(ctx, query, ev.TxnID, ev.EventType, ev.EventVersion, ev.Payload, ev.CreatedAt)
	} else {
		row = r.db.QueryRowContext(ctx, query, ev.TxnID, ev.EventType, ev.EventVersion, ev.Payload, ev.CreatedAt)
	}
	return row.Scan(&ev.EventID)
}

func (r *Repository) GetPaymentEvents(ctx context.Context, txnID string) ([]*domain.PaymentEvent, error) {
	query := `
		SELECT event_id, txn_id, event_type, event_version, payload, created_at
		FROM payment_events
		WHERE txn_id = $1
		ORDER BY event_id ASC
	`
	rows, err := r.db.QueryContext(ctx, query, txnID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []*domain.PaymentEvent
	for rows.Next() {
		var ev domain.PaymentEvent
		err := rows.Scan(&ev.EventID, &ev.TxnID, &ev.EventType, &ev.EventVersion, &ev.Payload, &ev.CreatedAt)
		if err != nil {
			return nil, err
		}
		events = append(events, &ev)
	}
	return events, nil
}

func (r *Repository) CreatePaymentSnapshot(ctx context.Context, tx *sql.Tx, snap *domain.PaymentSnapshot) error {
	query := `
		INSERT INTO payment_snapshots (aggregate_id, aggregate_version, snapshot_data, created_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (aggregate_id) DO UPDATE
		SET aggregate_version = EXCLUDED.aggregate_version, snapshot_data = EXCLUDED.snapshot_data, created_at = EXCLUDED.created_at
	`
	var err error
	if tx != nil {
		_, err = tx.ExecContext(ctx, query, snap.AggregateID, snap.AggregateVersion, snap.SnapshotData, snap.CreatedAt)
	} else {
		_, err = r.db.ExecContext(ctx, query, snap.AggregateID, snap.AggregateVersion, snap.SnapshotData, snap.CreatedAt)
	}
	return err
}

func (r *Repository) GetPaymentSnapshot(ctx context.Context, aggregateID string) (*domain.PaymentSnapshot, error) {
	query := `
		SELECT aggregate_id, aggregate_version, snapshot_data, created_at
		FROM payment_snapshots WHERE aggregate_id = $1
	`
	row := r.db.QueryRowContext(ctx, query, aggregateID)
	var snap domain.PaymentSnapshot
	err := row.Scan(&snap.AggregateID, &snap.AggregateVersion, &snap.SnapshotData, &snap.CreatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil // Snapshot not found is fine, we replay from version 0
		}
		return nil, err
	}
	return &snap, nil
}

// --- DEAD LETTER QUEUE (DLQ) ---

func (r *Repository) CreateDeadLetterEvent(ctx context.Context, tx *sql.Tx, ev *domain.DeadLetterEvent) error {
	query := `
		INSERT INTO dead_letter_events (payload, failure_reason, retry_count, timestamp)
		VALUES ($1, $2, $3, $4)
		RETURNING event_id
	`
	var row *sql.Row
	if tx != nil {
		row = tx.QueryRowContext(ctx, query, ev.Payload, ev.FailureReason, ev.RetryCount, ev.Timestamp)
	} else {
		row = r.db.QueryRowContext(ctx, query, ev.Payload, ev.FailureReason, ev.RetryCount, ev.Timestamp)
	}
	return row.Scan(&ev.EventID)
}

func (r *Repository) Ping(ctx context.Context) error {
	if chaos.GetController().IsPostgresOffline() {
		return errors.New("database connection down (simulated chaos)")
	}
	return r.db.PingContext(ctx)
}

func (r *Repository) GetDeadLetterEvents(ctx context.Context) ([]*domain.DeadLetterEvent, error) {
	query := `
		SELECT event_id, payload, failure_reason, retry_count, timestamp
		FROM dead_letter_events
		ORDER BY event_id ASC
	`
	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []*domain.DeadLetterEvent
	for rows.Next() {
		var ev domain.DeadLetterEvent
		err := rows.Scan(&ev.EventID, &ev.Payload, &ev.FailureReason, &ev.RetryCount, &ev.Timestamp)
		if err != nil {
			return nil, err
		}
		events = append(events, &ev)
	}
	return events, nil
}

func (r *Repository) GetDeadLetterEventsPaginated(ctx context.Context, limit int, offset int) ([]*domain.DeadLetterEvent, int, error) {
	var total int
	err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dead_letter_events").Scan(&total)
	if err != nil {
		return nil, 0, err
	}

	query := `
		SELECT event_id, payload, failure_reason, retry_count, timestamp
		FROM dead_letter_events
		ORDER BY event_id ASC
		LIMIT $1 OFFSET $2
	`
	rows, err := r.db.QueryContext(ctx, query, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var events []*domain.DeadLetterEvent
	for rows.Next() {
		var ev domain.DeadLetterEvent
		err := rows.Scan(&ev.EventID, &ev.Payload, &ev.FailureReason, &ev.RetryCount, &ev.Timestamp)
		if err != nil {
			return nil, 0, err
		}
		events = append(events, &ev)
	}
	return events, total, nil
}

func (r *Repository) GetDeadLetterEvent(ctx context.Context, id int64) (*domain.DeadLetterEvent, error) {
	query := `
		SELECT event_id, payload, failure_reason, retry_count, timestamp
		FROM dead_letter_events WHERE event_id = $1
	`
	row := r.db.QueryRowContext(ctx, query, id)
	var ev domain.DeadLetterEvent
	err := row.Scan(&ev.EventID, &ev.Payload, &ev.FailureReason, &ev.RetryCount, &ev.Timestamp)
	if err != nil {
		return nil, err
	}
	return &ev, nil
}

func (r *Repository) DeleteDeadLetterEvent(ctx context.Context, id int64) error {
	query := `DELETE FROM dead_letter_events WHERE event_id = $1`
	_, err := r.db.ExecContext(ctx, query, id)
	return err
}

// --- CQRS PROJECTIONS ---

func (r *Repository) CreateOrUpdateReadProjection(ctx context.Context, tx *sql.Tx, proj *domain.PaymentReadProjection) error {
	query := `
		INSERT INTO payment_read_projections (txn_id, sender_id, receiver_id, amount, status, relay_hops, settled_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (txn_id) DO UPDATE
		SET status = EXCLUDED.status, relay_hops = EXCLUDED.relay_hops, settled_at = EXCLUDED.settled_at, updated_at = EXCLUDED.updated_at
	`
	var err error
	if tx != nil {
		_, err = tx.ExecContext(ctx, query, proj.TxnID, proj.SenderID, proj.ReceiverID, proj.Amount, proj.Status, proj.RelayHops, proj.SettledAt, proj.UpdatedAt)
	} else {
		_, err = r.db.ExecContext(ctx, query, proj.TxnID, proj.SenderID, proj.ReceiverID, proj.Amount, proj.Status, proj.RelayHops, proj.SettledAt, proj.UpdatedAt)
	}
	return err
}

func (r *Repository) GetReadProjection(ctx context.Context, txnID string) (*domain.PaymentReadProjection, error) {
	query := `
		SELECT txn_id, sender_id, receiver_id, amount, status, relay_hops, settled_at, updated_at
		FROM payment_read_projections WHERE txn_id = $1
	`
	row := r.db.QueryRowContext(ctx, query, txnID)
	var proj domain.PaymentReadProjection
	err := row.Scan(&proj.TxnID, &proj.SenderID, &proj.ReceiverID, &proj.Amount, &proj.Status, &proj.RelayHops, &proj.SettledAt, &proj.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &proj, nil
}

// --- CONCURRENT OUTBOX WITH SKIP LOCKED ---

func (r *Repository) GetPendingOutboxEventsForUpdate(ctx context.Context, tx *sql.Tx, limit int, lockedBy string) ([]*domain.OutboxEvent, error) {
	// Select pending outbox events using SKIP LOCKED and lock them
	selectQuery := `
		SELECT event_id, stream_name, event_type, payload, published, created_at
		FROM outbox_events
		WHERE status = 'PENDING'
		ORDER BY event_id ASC
		LIMIT $1
		FOR UPDATE SKIP LOCKED
	`
	rows, err := tx.QueryContext(ctx, selectQuery, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []*domain.OutboxEvent
	var ids []int64
	for rows.Next() {
		var ev domain.OutboxEvent
		err := rows.Scan(&ev.EventID, &ev.StreamName, &ev.EventType, &ev.Payload, &ev.Published, &ev.CreatedAt)
		if err != nil {
			return nil, err
		}
		events = append(events, &ev)
		ids = append(ids, ev.EventID)
	}

	if len(ids) == 0 {
		return nil, nil
	}

	// Update locked events status to PROCESSING
	updateQuery := `
		UPDATE outbox_events
		SET status = 'PROCESSING', locked_by = $1, locked_at = CURRENT_TIMESTAMP
		WHERE event_id = ANY($2)
	`
	_, err = tx.ExecContext(ctx, updateQuery, lockedBy, pq.Array(ids))
	if err != nil {
		return nil, err
	}

	return events, nil
}

func (r *Repository) MarkOutboxEventPublished(ctx context.Context, tx *sql.Tx, id int64) error {
	query := `
		UPDATE outbox_events
		SET status = 'PUBLISHED', published = TRUE, locked_by = NULL, locked_at = NULL
		WHERE event_id = $1
	`
	var err error
	if tx != nil {
		_, err = tx.ExecContext(ctx, query, id)
	} else {
		_, err = r.db.ExecContext(ctx, query, id)
	}
	return err
}

func (r *Repository) MarkOutboxEventFailed(ctx context.Context, tx *sql.Tx, id int64, errStr string, maxRetries int) error {
	// Increment retry count. If it exceeds maxRetries, set status to FAILED, otherwise PENDING (so it retries)
	query := `
		UPDATE outbox_events
		SET retry_count = retry_count + 1,
		    last_error = $1,
		    status = CASE WHEN retry_count + 1 >= $2 THEN 'FAILED' ELSE 'PENDING' END,
		    locked_by = NULL,
		    locked_at = NULL
		WHERE event_id = $3
	`
	var err error
	if tx != nil {
		_, err = tx.ExecContext(ctx, query, errStr, maxRetries, id)
	} else {
		_, err = r.db.ExecContext(ctx, query, errStr, maxRetries, id)
	}
	return err
}

func (r *Repository) GetDevicesByOwner(ctx context.Context, ownerID string) ([]*domain.Device, error) {
	query := `
		SELECT device_id, owner_id, public_key, trust_score, status, created_at, revoked_at
		FROM devices WHERE owner_id = $1
	`
	rows, err := r.db.QueryContext(ctx, query, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var devices []*domain.Device
	for rows.Next() {
		var dev domain.Device
		err := rows.Scan(&dev.DeviceID, &dev.OwnerID, &dev.PublicKey, &dev.TrustScore, &dev.Status, &dev.CreatedAt, &dev.RevokedAt)
		if err != nil {
			return nil, err
		}
		devices = append(devices, &dev)
	}
	return devices, nil
}

func (r *Repository) IsEventProcessed(ctx context.Context, eventID string) (bool, error) {
	query := `SELECT EXISTS(SELECT 1 FROM processed_events WHERE event_id = $1)`
	var exists bool
	err := r.db.QueryRowContext(ctx, query, eventID).Scan(&exists)
	return exists, err
}

func (r *Repository) MarkEventProcessed(ctx context.Context, tx *sql.Tx, eventID string) error {
	query := `INSERT INTO processed_events (event_id, processed_at) VALUES ($1, CURRENT_TIMESTAMP) ON CONFLICT (event_id) DO NOTHING`
	var err error
	if tx != nil {
		_, err = tx.ExecContext(ctx, query, eventID)
	} else {
		_, err = r.db.ExecContext(ctx, query, eventID)
	}
	return err
}

func (r *Repository) GetProjectionCheckpoint(ctx context.Context, name string) (int64, error) {
	query := `SELECT last_event_id FROM projection_checkpoints WHERE projection_name = $1`
	var lastEventID int64
	err := r.db.QueryRowContext(ctx, query, name).Scan(&lastEventID)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return lastEventID, err
}

func (r *Repository) UpdateProjectionCheckpoint(ctx context.Context, tx *sql.Tx, name string, val int64) error {
	query := `
		INSERT INTO projection_checkpoints (projection_name, last_event_id, updated_at)
		VALUES ($1, $2, CURRENT_TIMESTAMP)
		ON CONFLICT (projection_name) DO UPDATE SET last_event_id = EXCLUDED.last_event_id, updated_at = CURRENT_TIMESTAMP
	`
	var err error
	if tx != nil {
		_, err = tx.ExecContext(ctx, query, name, val)
	} else {
		_, err = r.db.ExecContext(ctx, query, name, val)
	}
	return err
}

func (r *Repository) GetAllPaymentEvents(ctx context.Context) ([]*domain.PaymentEvent, error) {
	query := `
		SELECT event_id, txn_id, event_type, event_version, payload, created_at
		FROM payment_events
		ORDER BY event_id ASC
	`
	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []*domain.PaymentEvent
	for rows.Next() {
		var ev domain.PaymentEvent
		err := rows.Scan(&ev.EventID, &ev.TxnID, &ev.EventType, &ev.EventVersion, &ev.Payload, &ev.CreatedAt)
		if err != nil {
			return nil, err
		}
		events = append(events, &ev)
	}
	return events, nil
}

func (r *Repository) GetLatestOutboxEventID(ctx context.Context) (int64, error) {
	query := `SELECT COALESCE(MAX(event_id), 0) FROM outbox_events`
	var maxID int64
	err := r.db.QueryRowContext(ctx, query).Scan(&maxID)
	return maxID, err
}

func (r *Repository) AuditAccountBalances(ctx context.Context) ([]*domain.BalanceDiscrepancy, error) {
	queryAccounts := `SELECT account_id, available_balance, reserved_balance FROM account_balances`
	rows, err := r.db.QueryContext(ctx, queryAccounts)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var discrepancies []*domain.BalanceDiscrepancy

	for rows.Next() {
		var accountID string
		var actualAvailable, actualReserved int64
		err = rows.Scan(&accountID, &actualAvailable, &actualReserved)
		if err != nil {
			return nil, err
		}

		queryLedger := `
			SELECT balance_after FROM ledger_entries 
			WHERE account_id = $1 
			ORDER BY entry_id DESC LIMIT 1
		`
		var expectedAvailable int64
		err = r.db.QueryRowContext(ctx, queryLedger, accountID).Scan(&expectedAvailable)
		if err != nil {
			if err == sql.ErrNoRows {
				expectedAvailable = actualAvailable
			} else {
				return nil, err
			}
		}

		queryTokens := `
			SELECT COALESCE(SUM(value), 0) FROM offline_tokens 
			WHERE owner_id = $1 AND status IN ('ISSUED', 'HELD')
		`
		var expectedReserved int64
		err = r.db.QueryRowContext(ctx, queryTokens, accountID).Scan(&expectedReserved)
		if err != nil {
			return nil, err
		}

		if expectedAvailable != actualAvailable || expectedReserved != actualReserved {
			diff := (actualAvailable + actualReserved) - (expectedAvailable + expectedReserved)
			discrepancy := &domain.BalanceDiscrepancy{
				AccountID:         accountID,
				ExpectedAvailable: expectedAvailable,
				ActualAvailable:   actualAvailable,
				ExpectedReserved:  expectedReserved,
				ActualReserved:    actualReserved,
				DiscrepancyAmount: diff,
			}
			discrepancies = append(discrepancies, discrepancy)
		}
	}

	return discrepancies, nil
}

func (r *Repository) GetDeadLetterEventsCount(ctx context.Context) (int, error) {
	var count int
	err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dead_letter_events").Scan(&count)
	return count, err
}

func (r *Repository) GetPendingOutboxEventsCount(ctx context.Context) (int, error) {
	var count int
	err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM outbox_events WHERE status = 'PENDING'").Scan(&count)
	return count, err
}

