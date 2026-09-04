// Package recovery implements bounded, policy-controlled recovery. It deliberately
// does not execute financial transfers: it can only request a retry through the
// transactional outbox. Settlement remains the sole ledger-writing authority.
package recovery

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

const MinimumConfidence = 0.70

type Classifier interface {
	Classify(FailureEvent) Classification
}

type LLMClassifier interface {
	ClassifyAmbiguous(FailureEvent) (Classification, error)
}

type RulesFirstClassifier struct{ LLM LLMClassifier }

func (c RulesFirstClassifier) Classify(e FailureEvent) Classification {
	code := strings.ToLower(e.Code + " " + e.Message)
	switch {
	case containsAny(code, "timeout", "temporary", "connection", "redis", "outbox", "unavailable"):
		return Classification{Category: FailureNetwork, Confidence: 0.95, Source: "RULE"}
	case containsAny(code, "nonce", "replay", "signature", "revoked", "attestation", "security"):
		return Classification{Category: FailureSecurity, Confidence: 0.99, Source: "RULE"}
	case containsAny(code, "ledger", "balance", "token", "duplicate", "consistency"):
		return Classification{Category: FailureConsistency, Confidence: 0.95, Source: "RULE"}
	case containsAny(code, "database", "postgres", "infrastructure"):
		return Classification{Category: FailureInfrastructure, Confidence: 0.95, Source: "RULE"}
	case containsAny(code, "declined", "expired", "insufficient", "payment"):
		return Classification{Category: FailurePayment, Confidence: 0.95, Source: "RULE"}
	}
	if c.LLM == nil {
		return Classification{Category: FailureUnknown, Confidence: 0, Source: "RULE"}
	}
	result, err := c.LLM.ClassifyAmbiguous(e)
	if err != nil || !validCategory(result.Category) || result.Confidence < 0 || result.Confidence > 1 {
		return Classification{Category: FailureUnknown, Confidence: 0, Source: "LLM_INVALID"}
	}
	result.Source = "LLM"
	return result
}
func containsAny(s string, values ...string) bool {
	for _, v := range values {
		if strings.Contains(s, v) {
			return true
		}
	}
	return false
}
func validCategory(c FailureCategory) bool {
	return c == FailureNetwork || c == FailurePayment || c == FailureSecurity || c == FailureConsistency || c == FailureInfrastructure || c == FailureUnknown
}

type Policy struct {
	MaxAttempts int
	MaxAmount   int64
	MaxElapsed  time.Duration
	Cooldown    time.Duration
}

func DefaultPolicy() Policy {
	return Policy{MaxAttempts: 3, MaxAmount: 5_000_000, MaxElapsed: 24 * time.Hour, Cooldown: time.Minute}
} // 5,000,000 minor units = ₹50,000
func (p Policy) Decide(e FailureEvent, c Classification, attempts int, now time.Time) Decision {
	if e.Amount > p.MaxAmount || e.OccurredAt.Add(p.MaxElapsed).Before(now) {
		return Decision{Action: ActionEscalate, Reason: "recovery_budget_exceeded"}
	}
	if attempts >= p.MaxAttempts {
		return Decision{Action: ActionExhausted, Reason: "attempt_limit_exceeded"}
	}
	if c.Confidence < MinimumConfidence {
		return Decision{Action: ActionAbstain, Reason: "classification_confidence_below_threshold"}
	}
	switch c.Category {
	case FailureNetwork, FailureInfrastructure:
		return Decision{Action: ActionRetry, Reason: "transient_failure"}
	case FailurePayment:
		return Decision{Action: ActionCompensate, Reason: "payment_failure_requires_compensation"}
	case FailureSecurity, FailureConsistency:
		return Decision{Action: ActionEscalate, Reason: "security_or_consistency_failure"}
	default:
		return Decision{Action: ActionAbstain, Reason: "unknown_failure"}
	}
}
func (p Policy) NextAttempt(attempt int, now time.Time) time.Time {
	if attempt < 1 {
		attempt = 1
	}
	d := p.Cooldown << (attempt - 1)
	return now.Add(d)
}
func (p Policy) Validate() error {
	if p.MaxAttempts <= 0 || p.MaxAmount <= 0 || p.MaxElapsed <= 0 || p.Cooldown <= 0 {
		return errors.New("recovery policy limits must be positive")
	}
	return nil
}
func IdempotencyKey(txnID string, _ Action) string {
	return fmt.Sprintf("recovery:%s", txnID)
}
