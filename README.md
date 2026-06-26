# OfflinePay: Offline-First Payment Intent & Proximity Relay System

OfflinePay is a production-quality backend prototype for an **offline-first payment intent system**. A user can create a cryptographically signed payment request without internet access, broadcast it over a local proximity network, and relay it through any nearby internet-connected node. The bank or ledger service performs secure, idempotent, replay-resistant settlement later.

> [!NOTE]
> OfflinePay is **not** a UPI clone and does **not** perform real-time offline bank settlement. It demonstrates a secure offline *pre-authorized token intent model* with eventual ledger reconciliation.

---

## Architecture Diagram

```mermaid
sequenceDiagram
    autonumber
    actor Alice as Alice (Offline Payer)
    participant Device as Payer Device (Offline)
    actor Bob as Bob (Merchant)
    participant Relay as Untrusted Relay Node (Online)
    participant Bank as Settlement Authority (Online)
    database Postgres as PostgreSQL Ledger
    database Redis as Redis Nonce Cache

    Note over Alice, Bank: 1. Provisioning Stage (Online)
    Alice->>Bank: Request Offline Spending Power ($50)
    Bank->>Postgres: SELECT FOR UPDATE alice_balance
    Bank->>Postgres: Reserve $50 (Move available -> reserved)
    Bank->>Bank: Sign Token (bank_private_key)
    Bank-->>Alice: Deliver Bank-Signed Token

    Note over Alice, Bob: 2. Offline Spending Stage (Offline Proximity)
    Alice->>Device: Initiate $35 Payment to Bob
    Device->>Device: Generate Nonce & Txn ID
    Device->>Device: Sign Payment Intent (device_private_key)
    Device->>Device: Encrypt payload using ECIES (bank_public_key)
    Device-->>Bob: Broadcast Ciphertext Envelope via Proximity

    Note over Bob, Bank: 3. Relay and Settlement Stage (Eventual Online)
    Bob->>Relay: Relay Envelope
    Relay->>Relay: Check local duplicate cache (txn_id & nonce)
    Relay->>Bank: POST /api/settlement/settle
    Bank->>Bank: Decrypt envelope using ECIES (bank_private_key)
    Bank->>Bank: Verify device signature & trust status
    Bank->>Bank: Verify bank token signature & expiry
    
    critical Database Transaction (Atomic)
        Bank->>Postgres: Register Nonce (unique check)
        Bank->>Postgres: Lock balances (alphabetical order)
        Bank->>Postgres: Check token not already consumed
        Bank->>Postgres: Move $35 reserved -> Bob available
        Bank->>Postgres: Move $15 change -> Alice available
        Bank->>Postgres: Write Double-Entry Ledger
        Bank->>Postgres: Mark token consumed & log audit event
        Bank->>Postgres: Write Outbox record
    end
    
    Bank-->>Relay: Return Settle HTTP 200 (ACK)
```

---

## Core Security & Cryptographic Model

### 1. Hybrid Encryption (ECIES)
To ensure that untrusted intermediate relay nodes cannot view or modify transactions:
1. The client device generates an ephemeral P-256 key pair.
2. It computes a shared ECDH secret using the bank's static public key and the ephemeral private key.
3. A 256-bit symmetric AES key is derived via **HKDF-SHA256**.
4. The transaction payload (including signatures and tokens) is encrypted using **AES-256-GCM** with a random 12-byte initialization vector (IV).
5. The final envelope structure contains: `ephemeral_public_key`, `ciphertext`, `nonce`, `auth_tag`, `txn_id`, and `iv`.

### 2. Double-Spend Prevention (Bank-Signed Tokens)
Offline spending requires pre-allocated spending limits. 
* Senders request an offline token when online.
* The bank reserves the value (e.g. $50) from the user's available balance and signs the token payload `(token_id, owner_id, value, expiry)` using the bank's P-256 private key.
* During offline transaction verification, the bank validates this signature.
* Change return: if a user spends $35 of a $50 token, the settlement service releases the remaining $15 back to the user's available balance.

### 3. Replay Protection
* **Fast-Path**: Nonces are checked and cached in Redis.
* **Authoritative**: Nonces are registered in a database table (`nonce_registry`) with a unique constraint. If a nonce is reused, the SQL insert fails, triggering an immediate transaction rollback.

---

## Repository Layout

* `cmd/server/`: Boots up the API server, transactional outbox worker, and periodic reconciliation scanner.
* `cmd/simulation/`: The chaos and resilience simulator CLI.
* `internal/api/`: Gin HTTP routes, controllers, and payload validations.
* `internal/crypto/`: ECDSA signature and ECIES hybrid encryption implementation.
* `internal/db/`: PostgreSQL pool management and embedded SQL migrations.
* `internal/domain/`: Core struct models, state transition validators, and event types.
* `internal/eventbus/`: Redis Streams event publishing and subscriber consumer groups.
* `internal/outbox/`: Transactional Outbox pattern worker.
* `internal/reconciliation/`: Background ledger integrity audits and token expiration sweeps.
* `internal/repository/`: Authoritative SQL mapping and query layer.
* `internal/risk/`: Trust score assessment and transaction throttling rules.

---

## Database Schema

```sql
-- Devices Identity
CREATE TABLE devices (
    device_id VARCHAR(100) PRIMARY KEY,
    owner_id VARCHAR(100) NOT NULL,
    public_key TEXT NOT NULL,
    trust_score DOUBLE PRECISION NOT NULL DEFAULT 1.0,
    status VARCHAR(50) NOT NULL DEFAULT 'ACTIVE',
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    revoked_at TIMESTAMP WITH TIME ZONE
);

-- Offline Spending Tokens
CREATE TABLE offline_tokens (
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

-- Payment Intents Lifecycle
CREATE TABLE payment_intents (
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

-- Anti-Replay Nonce Registry
CREATE TABLE nonce_registry (
    nonce VARCHAR(100) PRIMARY KEY,
    txn_id VARCHAR(100) NOT NULL REFERENCES payment_intents(txn_id),
    consumed_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    expiry TIMESTAMP WITH TIME ZONE NOT NULL
);

-- Accounts & Balances
CREATE TABLE account_balances (
    account_id VARCHAR(100) PRIMARY KEY,
    available_balance BIGINT NOT NULL CHECK (available_balance >= 0),
    reserved_balance BIGINT NOT NULL DEFAULT 0 CHECK (reserved_balance >= 0),
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

-- Double-Entry Ledger Entries
CREATE TABLE ledger_entries (
    entry_id BIGSERIAL PRIMARY KEY,
    txn_id VARCHAR(100) NOT NULL REFERENCES payment_intents(txn_id),
    account_id VARCHAR(100) NOT NULL REFERENCES account_balances(account_id),
    direction VARCHAR(10) NOT NULL CHECK (direction IN ('DEBIT', 'CREDIT')),
    amount BIGINT NOT NULL CHECK (amount > 0),
    entry_type VARCHAR(50) NOT NULL,
    balance_after BIGINT NOT NULL,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);
```

---

## API Endpoints

### 1. Register Device
* **Route**: `POST /api/identity/devices`
* **Request**:
  ```json
  {
    "device_id": "dev-alice-phone",
    "owner_id": "user-alice",
    "public_key": "-----BEGIN PUBLIC KEY-----\n..."
  }
  ```
* **Response**: `200 OK` device JSON model.

### 2. Issue Token
* **Route**: `POST /api/tokens/issue`
* **Request**:
  ```json
  {
    "owner_id": "user-alice",
    "value": 5000,
    "duration_seconds": 3600
  }
  ```
* **Response**: `200 OK` offline token JSON.

### 3. Settle Envelope (Direct)
* **Route**: `POST /api/settlement/settle`
* **Request**:
  ```json
  {
    "envelope": {
      "ephemeral_public_key": "-----BEGIN PUBLIC KEY-----\n...",
      "ciphertext": "base64...",
      "nonce": "nonce-uuid",
      "auth_tag": "base64...",
      "txn_id": "txn-uuid",
      "iv": "base64..."
    },
    "hop_count": 1
  }
  ```
* **Response**: `200 OK` `{ "status": "SETTLED" }` or `422 Unprocessable Entity` on failure.

---

## Running the Application

### 1. Build and Start the Environment
Ensure Docker is running, then execute:
```bash
docker compose up --build
```
This boots up:
* **PostgreSQL** on port `5432` (Auto-applies migrations)
* **Redis** on port `6379`
* **Jaeger Tracing** Web UI on port `16686`
* **Prometheus metrics** UI on port `9090`
* **OfflinePay Main Server** on port `8080`

### 2. Run the Chaos & Reliability Simulator
To execute the integration scenarios (packet loss, duplicate relays, replay attacks, concurrent race bursts, and token expiration reconciliations), run:
```bash
# Set database environment variable
$env:DATABASE_URL="postgres://postgres:postgres@localhost:5432/offlinepay?sslmode=disable"
$env:REDIS_ADDR="localhost:6379"
go run cmd/simulation/main.go
```
Check the terminal logs to see detailed structured JSON reports demonstrating exactly-once token consumption and ledger balance audits.
