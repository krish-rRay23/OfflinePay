package recovery

import (
	"testing"
	"time"
)

func TestRulesAndPolicyKeepAuthorityDeterministic(t *testing.T) {
	p := DefaultPolicy()
	now := time.Now()
	e := FailureEvent{TxnID: "t", Amount: 100, Code: "network timeout", OccurredAt: now}
	c := RulesFirstClassifier{}.Classify(e)
	if c.Category != FailureNetwork || c.Confidence < MinimumConfidence {
		t.Fatalf("unexpected classification: %+v", c)
	}
	d := p.Decide(e, c, 0, now)
	if d.Action != ActionRetry {
		t.Fatalf("got %s", d.Action)
	}
}
func TestPolicyAbstainsForLowConfidenceAndBoundsAttempts(t *testing.T) {
	p := DefaultPolicy()
	now := time.Now()
	e := FailureEvent{Amount: 1, OccurredAt: now}
	if got := p.Decide(e, Classification{Category: FailureNetwork, Confidence: .69}, 0, now).Action; got != ActionAbstain {
		t.Fatal(got)
	}
	if got := p.Decide(e, Classification{Category: FailureNetwork, Confidence: 1}, p.MaxAttempts, now).Action; got != ActionExhausted {
		t.Fatal(got)
	}
}
func TestRulesClassifierRejectsMalformedLLM(t *testing.T) {
	c := RulesFirstClassifier{LLM: badLLM{}}
	got := c.Classify(FailureEvent{Code: "ambiguous"})
	if got.Source != "LLM_INVALID" || got.Confidence != 0 {
		t.Fatalf("%+v", got)
	}
}

type badLLM struct{}

func (badLLM) ClassifyAmbiguous(FailureEvent) (Classification, error) {
	return Classification{Category: "money", Confidence: 2}, nil
}
func TestRecoveryTerminalStatesCannotTransition(t *testing.T) {
	for _, terminal := range []State{StateCompensated, StateEscalated, StateExhausted} {
		if IsValidTransition(terminal, StateScheduled) {
			t.Fatalf("terminal recovery state %s revived", terminal)
		}
	}
}
func TestRecoveryIdempotencyKeyIsStablePerTransaction(t *testing.T) {
	if IdempotencyKey("txn-1", ActionRetry) != IdempotencyKey("txn-1", ActionRetry) {
		t.Fatal("key is not stable")
	}
	if IdempotencyKey("txn-1", ActionRetry) != IdempotencyKey("txn-1", ActionCompensate) {
		t.Fatal("one transaction must have one recovery operation")
	}
	if IdempotencyKey("txn-1", ActionRetry) == IdempotencyKey("txn-2", ActionRetry) {
		t.Fatal("transaction collision")
	}
}
func BenchmarkPolicyDecision(b *testing.B) {
	p := DefaultPolicy()
	e := FailureEvent{Amount: 1, OccurredAt: time.Now()}
	c := Classification{Category: FailureNetwork, Confidence: 1}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = p.Decide(e, c, 0, time.Now())
	}
}
