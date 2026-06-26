-- CREATE TABLES FOR OFFLINEPAY

CREATE TABLE IF NOT EXISTS devices (
    device_id VARCHAR(100) PRIMARY KEY,
    owner_id VARCHAR(100) NOT NULL,
    public_key TEXT NOT NULL,
    trust_score DOUBLE PRECISION NOT NULL DEFAULT 1.0,
    status VARCHAR(50) NOT NULL DEFAULT 'ACTIVE',
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    revoked_at TIMESTAMP WITH TIME ZONE
);

CREATE TABLE IF NOT EXISTS offline_tokens (
    token_id VARCHAR(100) PRIMARY KEY,
    owner_id VARCHAR(100) NOT NULL,
    value BIGINT NOT NULL,
    expiry TIMESTAMP WITH TIME ZONE NOT NULL,
    consumed BOOLEAN NOT NULL DEFAULT FALSE,
    consumed_at TIMESTAMP WITH TIME ZONE,
    token_signature TEXT NOT NULL,
    reserved_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    released_at TIMESTAMP WITH TIME ZONE,
    risk_score_at_issue DOUBLE PRECISION NOT NULL DEFAULT 0.0
);

CREATE TABLE IF NOT EXISTS payment_intents (
    txn_id VARCHAR(100) PRIMARY KEY,
    sender_id VARCHAR(100) NOT NULL,
    receiver_id VARCHAR(100) NOT NULL,
    amount BIGINT NOT NULL,
    currency VARCHAR(10) NOT NULL,
    nonce VARCHAR(100) NOT NULL,
    expiry TIMESTAMP WITH TIME ZONE NOT NULL,
    status VARCHAR(50) NOT NULL,
    device_id VARCHAR(100) NOT NULL REFERENCES devices(device_id),
    token_id VARCHAR(100) REFERENCES offline_tokens(token_id),
    failure_reason TEXT,
    settled_at TIMESTAMP WITH TIME ZONE,
    rejected_at TIMESTAMP WITH TIME ZONE,
    relay_hops INT DEFAULT 0,
    signature_hash VARCHAR(100),
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS nonce_registry (
    nonce VARCHAR(100) PRIMARY KEY,
    txn_id VARCHAR(100) NOT NULL REFERENCES payment_intents(txn_id),
    consumed_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    expiry TIMESTAMP WITH TIME ZONE NOT NULL
);

CREATE TABLE IF NOT EXISTS account_balances (
    account_id VARCHAR(100) PRIMARY KEY,
    available_balance BIGINT NOT NULL CHECK (available_balance >= 0),
    reserved_balance BIGINT NOT NULL DEFAULT 0 CHECK (reserved_balance >= 0),
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS ledger_entries (
    entry_id BIGSERIAL PRIMARY KEY,
    txn_id VARCHAR(100) NOT NULL REFERENCES payment_intents(txn_id),
    account_id VARCHAR(100) NOT NULL REFERENCES account_balances(account_id),
    direction VARCHAR(10) NOT NULL CHECK (direction IN ('DEBIT', 'CREDIT')),
    amount BIGINT NOT NULL CHECK (amount > 0),
    entry_type VARCHAR(50) NOT NULL,
    balance_after BIGINT NOT NULL,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS relay_attempts (
    relay_id VARCHAR(100) NOT NULL,
    txn_id VARCHAR(100) NOT NULL,
    hop_count INT NOT NULL DEFAULT 1,
    status VARCHAR(50) NOT NULL,
    first_seen_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    last_seen_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (relay_id, txn_id)
);

CREATE TABLE IF NOT EXISTS audit_events (
    event_id BIGSERIAL PRIMARY KEY,
    txn_id VARCHAR(100) NOT NULL,
    event_type VARCHAR(100) NOT NULL,
    payload JSONB NOT NULL,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS outbox_events (
    event_id BIGSERIAL PRIMARY KEY,
    stream_name VARCHAR(100) NOT NULL,
    event_type VARCHAR(100) NOT NULL,
    payload JSONB NOT NULL,
    published BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

-- Indices for faster lookups and uniqueness constraints
CREATE INDEX IF NOT EXISTS idx_payment_intents_token ON payment_intents(token_id);
CREATE INDEX IF NOT EXISTS idx_payment_intents_status ON payment_intents(status);
CREATE INDEX IF NOT EXISTS idx_ledger_entries_txn ON ledger_entries(txn_id);
CREATE INDEX IF NOT EXISTS idx_outbox_events_published ON outbox_events(published);
