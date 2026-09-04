-- Bounded recovery is write-ahead and independent of the settlement ledger.
CREATE TABLE IF NOT EXISTS failure_events (
    failure_id UUID PRIMARY KEY,
    txn_id VARCHAR(100) NOT NULL REFERENCES payment_intents(txn_id),
    category VARCHAR(32) NOT NULL,
    code VARCHAR(100) NOT NULL,
    message TEXT NOT NULL,
    confidence DOUBLE PRECISION NOT NULL CHECK (confidence >= 0 AND confidence <= 1),
    classifier_source VARCHAR(32) NOT NULL,
    occurred_at TIMESTAMP WITH TIME ZONE NOT NULL,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS recovery_operations (
    recovery_id UUID PRIMARY KEY,
    txn_id VARCHAR(100) NOT NULL REFERENCES payment_intents(txn_id),
    idempotency_key VARCHAR(200) NOT NULL UNIQUE,
    action VARCHAR(32) NOT NULL,
    state VARCHAR(32) NOT NULL,
    attempt_count INT NOT NULL DEFAULT 0 CHECK (attempt_count >= 0 AND attempt_count <= 3),
    next_attempt_at TIMESTAMP WITH TIME ZONE,
    last_error TEXT,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_recovery_operations_due ON recovery_operations (state, next_attempt_at);
