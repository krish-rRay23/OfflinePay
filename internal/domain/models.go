package domain

import (
	"time"
)

// Payment State Machine States
const (
	StateCreated     = "CREATED"
	StateSigned      = "SIGNED"
	StateEncrypted   = "ENCRYPTED"
	StateBroadcast   = "BROADCAST"
	StateRelayed     = "RELAYED"
	StateValidated   = "VALIDATED"
	StateReserved    = "RESERVED"
	StateSettled     = "SETTLED"
	StateRejected    = "REJECTED"
	StateFailed      = "FAILED"
	StateExpired     = "EXPIRED"
	StateDuplicate   = "DUPLICATE"
	StateReconciled  = "RECONCILED"
)

// Device Status
const (
	DeviceActive      = "ACTIVE"
	DeviceRevoked     = "REVOKED"
	DeviceCompromised = "COMPROMISED"
)

// Ledger Entry Direction
const (
	DirectionDebit  = "DEBIT"
	DirectionCredit = "CREDIT"
)

// Ledger Entry Type
const (
	EntryTypeSettlement  = "SETTLEMENT"
	EntryTypeReservation = "RESERVATION"
	EntryTypeRelease     = "RELEASE"
)

// Device identity model
type Device struct {
	DeviceID   string     `json:"device_id"`
	OwnerID    string     `json:"owner_id"`
	PublicKey  string     `json:"public_key"`
	TrustScore float64    `json:"trust_score"`
	Status     string     `json:"status"`
	CreatedAt  time.Time  `json:"created_at"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

// Offline token model
type OfflineToken struct {
	TokenID           string     `json:"token_id"`
	OwnerID           string     `json:"owner_id"`
	Value             int64      `json:"value"` // in cents / minor unit
	Expiry            time.Time  `json:"expiry"`
	Consumed          bool       `json:"consumed"`
	ConsumedAt        *time.Time `json:"consumed_at,omitempty"`
	TokenSignature    string     `json:"token_signature"`
	ReservedAt        time.Time  `json:"reserved_at"`
	ReleasedAt        *time.Time `json:"released_at,omitempty"`
	RiskScoreAtIssue  float64    `json:"risk_score_at_issue"`
	Status            string     `json:"status"` // ISSUED, HELD, CONSUMED, INVALIDATED
}

// Payment intent raw payload (what gets signed and encrypted)
type PaymentIntentPayload struct {
	TxnID      string    `json:"txn_id"`
	SenderID   string    `json:"sender_id"`
	ReceiverID string    `json:"receiver_id"`
	Amount     int64     `json:"amount"`
	Currency   string    `json:"currency"`
	Nonce      string    `json:"nonce"`
	Expiry     time.Time `json:"expiry"`
	DeviceID   string    `json:"device_id"`
	TokenID    string    `json:"token_id"`
}

// Payment intent full model in db
type PaymentIntent struct {
	TxnID         string     `json:"txn_id"`
	SenderID      string     `json:"sender_id"`
	ReceiverID    string     `json:"receiver_id"`
	Amount        int64      `json:"amount"`
	Currency      string     `json:"currency"`
	Nonce         string     `json:"nonce"`
	Expiry        time.Time  `json:"expiry"`
	Status        string     `json:"status"`
	DeviceID      string     `json:"device_id"`
	TokenID       string     `json:"token_id"`
	FailureReason *string    `json:"failure_reason,omitempty"`
	SettledAt     *time.Time `json:"settled_at,omitempty"`
	RejectedAt    *time.Time `json:"rejected_at,omitempty"`
	RelayHops     int        `json:"relay_hops"`
	SignatureHash string     `json:"signature_hash"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

// Encrypted envelope structure for transit
type EncryptedEnvelope struct {
	EphemeralPublicKey string `json:"ephemeral_public_key"` // PEM P-256 public key
	Ciphertext         string `json:"ciphertext"`           // Base64 AES-GCM encrypted payload
	Nonce              string `json:"nonce"`                // Nonce field from intent (for relay dedupe)
	AuthTag            string `json:"auth_tag"`             // Base64 auth tag
	TxnID              string `json:"txn_id"`               // Transaction ID (for relay dedupe)
	IV                 string `json:"iv"`                   // Base64 initialization vector
}

// Nonce registry entry
type NonceRegistry struct {
	Nonce      string    `json:"nonce"`
	TxnID      string    `json:"txn_id"`
	ConsumedAt time.Time `json:"consumed_at"`
	Expiry     time.Time `json:"expiry"`
}

// Account balance model
type AccountBalance struct {
	AccountID        string    `json:"account_id"`
	AvailableBalance int64     `json:"available_balance"`
	ReservedBalance  int64     `json:"reserved_balance"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// Double-entry ledger entry model
type LedgerEntry struct {
	EntryID      int64     `json:"entry_id"`
	TxnID        string    `json:"txn_id"`
	AccountID    string    `json:"account_id"`
	Direction    string    `json:"direction"`
	Amount       int64     `json:"amount"`
	EntryType    string    `json:"entry_type"`
	BalanceAfter int64     `json:"balance_after"`
	CreatedAt    time.Time `json:"created_at"`
}

// Relay attempt tracker
type RelayAttempt struct {
	RelayID     string    `json:"relay_id"`
	TxnID       string    `json:"txn_id"`
	HopCount    int       `json:"hop_count"`
	Status      string    `json:"status"`
	FirstSeenAt time.Time `json:"first_seen_at"`
	LastSeenAt  time.Time `json:"last_seen_at"`
}

// Audit event logger model
type AuditEvent struct {
	EventID   int64     `json:"event_id"`
	TxnID     string    `json:"txn_id"`
	EventType string    `json:"event_type"`
	Payload   string    `json:"payload"` // JSON string representation
	CreatedAt time.Time `json:"created_at"`
}

// Outbox event pattern
type OutboxEvent struct {
	EventID    int64     `json:"event_id"`
	StreamName string    `json:"stream_name"`
	EventType  string    `json:"event_type"`
	Payload    string    `json:"payload"` // JSON string representation
	Published  bool      `json:"published"`
	CreatedAt  time.Time `json:"created_at"`
}

// Validate transition logic
func IsValidTransition(oldState, newState string) bool {
	if oldState == newState {
		return true
	}

	transitions := map[string][]string{
		StateCreated:   {StateSigned},
		StateSigned:    {StateEncrypted, StateFailed},
		StateEncrypted: {StateBroadcast, StateFailed},
		StateBroadcast: {StateRelayed, StateExpired, StateFailed},
		StateRelayed:   {StateValidated, StateRejected, StateDuplicate, StateExpired, StateFailed},
		StateValidated: {StateReserved, StateRejected, StateFailed},
		StateReserved:  {StateSettled, StateFailed},
		StateSettled:   {StateReconciled},
		StateRejected:  {StateReconciled},
		StateFailed:    {StateReconciled},
		StateExpired:   {StateReconciled},
		StateDuplicate: {StateReconciled},
		StateReconciled: {},
	}

	allowed, ok := transitions[oldState]
	if !ok {
		return false
	}
	for _, a := range allowed {
		if a == newState {
			return true
		}
	}
	return false
}

// Saga Status Constants
const (
	SagaStatusStarted      = "STARTED"
	SagaStatusCompensating = "COMPENSATING"
	SagaStatusCompleted    = "COMPLETED"
	SagaStatusFailed       = "FAILED"
)

// PaymentEvent represents a versioned audit/state log for Event Sourcing
type PaymentEvent struct {
	EventID      int64     `json:"event_id"`
	TxnID        string    `json:"txn_id"`
	EventType    string    `json:"event_type"`
	EventVersion int       `json:"event_version"`
	Payload      string    `json:"payload"` // JSON string representation
	CreatedAt    time.Time `json:"created_at"`
}

// PaymentSnapshot holds a snapshot of a payment intent aggregate at a specific version
type PaymentSnapshot struct {
	AggregateID      string    `json:"aggregate_id"`
	AggregateVersion int       `json:"aggregate_version"`
	SnapshotData     string    `json:"snapshot_data"` // JSON representation of aggregate state
	CreatedAt        time.Time `json:"created_at"`
}

// DeadLetterEvent represents a failed outbox event that exceeded retry limits
type DeadLetterEvent struct {
	EventID       int64     `json:"event_id"`
	Payload       string    `json:"payload"`
	FailureReason string    `json:"failure_reason"`
	RetryCount    int       `json:"retry_count"`
	Timestamp     time.Time `json:"timestamp"`
}

// DeviceAttestation represents simulated security attestation data (Secure Enclave, TPM, etc.)
type DeviceAttestation struct {
	DeviceID        string    `json:"device_id"`
	AttestationType string    `json:"attestation_type"`
	AttestationHash string    `json:"attestation_hash"`
	TrustLevel      string    `json:"trust_level"`
	CreatedAt       time.Time `json:"created_at"`
}

// SagaState tracks execution state of a reservation-based transaction flow
type SagaState struct {
	SagaID    string    `json:"saga_id"`
	TxnID     string    `json:"txn_id"`
	Status    string    `json:"status"`
	Step      string    `json:"step"`
	UpdatedAt time.Time `json:"updated_at"`
}

// PaymentReadProjection represents the read-model for CQRS projections
type PaymentReadProjection struct {
	TxnID      string     `json:"txn_id"`
	SenderID   string     `json:"sender_id"`
	ReceiverID string     `json:"receiver_id"`
	Amount     int64      `json:"amount"`
	Status     string     `json:"status"`
	RelayHops  int        `json:"relay_hops"`
	SettledAt  *time.Time `json:"settled_at,omitempty"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// BalanceDiscrepancy represents the auditing discrepancy details for an account
type BalanceDiscrepancy struct {
	AccountID         string `json:"account_id"`
	ExpectedAvailable int64  `json:"expected_available"`
	ActualAvailable   int64  `json:"actual_available"`
	ExpectedReserved  int64  `json:"expected_reserved"`
	ActualReserved    int64  `json:"actual_reserved"`
	DiscrepancyAmount int64  `json:"discrepancy_amount"`
}

// IsValidTokenTransition validates token FSM transitions
func IsValidTokenTransition(oldState, newState string) bool {
	if oldState == newState {
		return true
	}
	transitions := map[string][]string{
		"ISSUED":      {"HELD", "INVALIDATED"},
		"HELD":        {"CONSUMED", "INVALIDATED"},
		"CONSUMED":    {},
		"INVALIDATED": {},
	}
	allowed, ok := transitions[oldState]
	if !ok {
		return false
	}
	for _, a := range allowed {
		if a == newState {
			return true
		}
	}
	return false
}

