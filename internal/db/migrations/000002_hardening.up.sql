-- OFFLINEPAY V2 HARDENING MIGRATIONS

-- Upgrade outbox_events to support concurrency, lock safety, and retries
ALTER TABLE outbox_events ADD COLUMN IF NOT EXISTS status VARCHAR(50) NOT NULL DEFAULT 'PENDING';
ALTER TABLE outbox_events ADD COLUMN IF NOT EXISTS retry_count INT NOT NULL DEFAULT 0;
ALTER TABLE outbox_events ADD COLUMN IF NOT EXISTS last_error TEXT;
ALTER TABLE outbox_events ADD COLUMN IF NOT EXISTS locked_at TIMESTAMP WITH TIME ZONE;
ALTER TABLE outbox_events ADD COLUMN IF NOT EXISTS locked_by VARCHAR(100);

-- Table for Immutable Versioned Event Store (Event Sourcing)
CREATE TABLE IF NOT EXISTS payment_events (
    event_id BIGSERIAL PRIMARY KEY,
    txn_id VARCHAR(100) NOT NULL REFERENCES payment_intents(txn_id),
    event_type VARCHAR(100) NOT NULL,
    event_version INT NOT NULL DEFAULT 1,
    payload JSONB NOT NULL,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

-- Table for Aggregate State Snapshots
CREATE TABLE IF NOT EXISTS payment_snapshots (
    aggregate_id VARCHAR(100) PRIMARY KEY,
    aggregate_version INT NOT NULL,
    snapshot_data JSONB NOT NULL,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

-- Table for Dead Letter Queue (DLQ)
CREATE TABLE IF NOT EXISTS dead_letter_events (
    event_id BIGSERIAL PRIMARY KEY,
    payload JSONB NOT NULL,
    failure_reason TEXT NOT NULL,
    retry_count INT NOT NULL,
    timestamp TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

-- Table for Device Cryptographic Attestation Registry
CREATE TABLE IF NOT EXISTS device_attestations (
    device_id VARCHAR(100) PRIMARY KEY REFERENCES devices(device_id) ON DELETE CASCADE,
    attestation_type VARCHAR(50) NOT NULL,
    attestation_hash TEXT NOT NULL,
    trust_level VARCHAR(50) NOT NULL,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

-- Table for Saga State Machine Tracking
CREATE TABLE IF NOT EXISTS saga_states (
    saga_id VARCHAR(100) PRIMARY KEY,
    txn_id VARCHAR(100) NOT NULL,
    status VARCHAR(50) NOT NULL,
    step VARCHAR(50) NOT NULL,
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

-- Table for CQRS Read Projections (Read Model)
CREATE TABLE IF NOT EXISTS payment_read_projections (
    txn_id VARCHAR(100) PRIMARY KEY,
    sender_id VARCHAR(100) NOT NULL,
    receiver_id VARCHAR(100) NOT NULL,
    amount BIGINT NOT NULL,
    status VARCHAR(50) NOT NULL,
    relay_hops INT NOT NULL DEFAULT 0,
    settled_at TIMESTAMP WITH TIME ZONE,
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

-- Indexes for performance
CREATE INDEX IF NOT EXISTS idx_payment_events_txn ON payment_events(txn_id);
CREATE INDEX IF NOT EXISTS idx_saga_states_txn ON saga_states(txn_id);
CREATE INDEX IF NOT EXISTS idx_outbox_events_status ON outbox_events(status) WHERE status = 'PENDING';
