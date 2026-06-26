-- ADD status column to offline_tokens for Token FSM
ALTER TABLE offline_tokens ADD COLUMN IF NOT EXISTS status VARCHAR(50) NOT NULL DEFAULT 'ISSUED';

-- CREATE processed_events table for consumer idempotency
CREATE TABLE IF NOT EXISTS processed_events (
    event_id VARCHAR(100) PRIMARY KEY,
    processed_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

-- CREATE projection_checkpoints table for lag monitoring
CREATE TABLE IF NOT EXISTS projection_checkpoints (
    projection_name VARCHAR(100) PRIMARY KEY,
    last_event_id BIGINT NOT NULL DEFAULT 0,
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);
