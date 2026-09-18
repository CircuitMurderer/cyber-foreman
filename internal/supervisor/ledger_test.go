package supervisor

import (
	"errors"
	"testing"
)

func TestApplyDecisionIsImmutableAndIdempotent(t *testing.T) {
	snapshot := Snapshot{TaskID: "task-1", Policy: DefaultPolicy()}
	decision := Decision{DedupeKey: "idle-1:idle-nudge", BudgetCost: BudgetCost{Nudges: 1}}
	next, duplicate, err := ApplyDecision(snapshot, decision)
	if err != nil || duplicate {
		t.Fatalf("first apply = duplicate %v, err %v", duplicate, err)
	}
	if snapshot.Budget.Nudges != 0 || len(snapshot.AppliedDecisions) != 0 {
		t.Fatalf("input snapshot was mutated: %#v", snapshot)
	}
	if next.Budget.Nudges != 1 || !next.AppliedDecisions[decision.DedupeKey] {
		t.Fatalf("decision was not applied: %#v", next)
	}
	again, duplicate, err := ApplyDecision(next, decision)
	if err != nil || !duplicate || again.Budget.Nudges != 1 {
		t.Fatalf("duplicate apply = %#v, duplicate %v, err %v", again, duplicate, err)
	}
}

func TestApplyDecisionRejectsBudgetOverflow(t *testing.T) {
	policy := DefaultPolicy()
	snapshot := Snapshot{Policy: policy, Budget: Budget{Retries: policy.MaxRetries}}
	decision := Decision{DedupeKey: "retry", BudgetCost: BudgetCost{Retries: 1}}
	_, _, err := ApplyDecision(snapshot, decision)
	if !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("error = %v, want ErrBudgetExceeded", err)
	}
}
