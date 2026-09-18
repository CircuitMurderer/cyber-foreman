package supervisor

import "errors"

var ErrBudgetExceeded = errors.New("supervision budget exceeded")

// ApplyDecision returns a new snapshot with the decision reserved and its
// budget charged. The input snapshot and its decision map are never mutated.
func ApplyDecision(snapshot Snapshot, decision Decision) (next Snapshot, duplicate bool, err error) {
	if decision.DedupeKey == "" {
		return snapshot, false, errors.New("decision dedupe key is required")
	}
	if snapshot.AppliedDecisions[decision.DedupeKey] {
		return snapshot, true, nil
	}
	if snapshot.Budget.Nudges+decision.BudgetCost.Nudges > snapshot.Policy.MaxNudges ||
		snapshot.Budget.Retries+decision.BudgetCost.Retries > snapshot.Policy.MaxRetries ||
		snapshot.Budget.TestRepairs+decision.BudgetCost.TestRepairs > snapshot.Policy.MaxTestRepairs {
		return snapshot, false, ErrBudgetExceeded
	}

	next = snapshot
	next.AppliedDecisions = make(map[string]bool, len(snapshot.AppliedDecisions)+1)
	for key, applied := range snapshot.AppliedDecisions {
		next.AppliedDecisions[key] = applied
	}
	next.AppliedDecisions[decision.DedupeKey] = true
	next.Budget.Nudges += decision.BudgetCost.Nudges
	next.Budget.Retries += decision.BudgetCost.Retries
	next.Budget.TestRepairs += decision.BudgetCost.TestRepairs
	return next, false, nil
}
