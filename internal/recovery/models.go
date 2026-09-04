package recovery

import "time"

type FailureCategory string

const (
	FailureNetwork        FailureCategory = "NETWORK"
	FailurePayment        FailureCategory = "PAYMENT"
	FailureSecurity       FailureCategory = "SECURITY"
	FailureConsistency    FailureCategory = "CONSISTENCY"
	FailureInfrastructure FailureCategory = "INFRASTRUCTURE"
	FailureUnknown        FailureCategory = "UNKNOWN"
)

type Action string

const (
	ActionRetry      Action = "RETRY"
	ActionCompensate Action = "COMPENSATE"
	ActionEscalate   Action = "ESCALATE"
	ActionAbstain    Action = "ABSTAIN"
	ActionExhausted  Action = "EXHAUSTED"
)

type State string

const (
	StatePending     State = "PENDING"
	StateDiagnosed   State = "DIAGNOSED"
	StateScheduled   State = "SCHEDULED"
	StateRecovering  State = "RECOVERING"
	StateCompensated State = "COMPENSATED"
	StateEscalated   State = "ESCALATED"
	StateExhausted   State = "EXHAUSTED"
)

type FailureEvent struct {
	ID         string
	TxnID      string
	Amount     int64
	Code       string
	Message    string
	OccurredAt time.Time
}
type Classification struct {
	Category   FailureCategory
	Confidence float64
	Source     string
}
type Decision struct {
	Action Action
	Reason string
}
type Operation struct {
	ID             string
	TxnID          string
	IdempotencyKey string
	Action         Action
	State          State
	AttemptCount   int
	NextAttemptAt  time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// IsValidTransition prevents a terminal recovery operation from being revived.
func IsValidTransition(from, to State) bool {
	if from == to {
		return true
	}
	switch from {
	case StatePending:
		return to == StateDiagnosed
	case StateDiagnosed:
		return to == StateScheduled || to == StateEscalated
	case StateScheduled:
		return to == StateRecovering || to == StateExhausted || to == StateEscalated
	case StateRecovering:
		return to == StateScheduled || to == StateCompensated || to == StateEscalated || to == StateExhausted
	default:
		return false
	}
}
