package supervisor

import (
	"testing"
	"time"

	"cyber-foreman/internal/domain"
)

func TestEngineRulePriorityAndBudgets(t *testing.T) {
	policy := DefaultPolicy()
	base := Snapshot{TaskID: "task-1", Status: domain.TaskRunning, Policy: policy}
	tests := []struct {
		name        string
		snapshot    Snapshot
		observation Observation
		action      Action
		cost        BudgetCost
	}{
		{
			name: "idle nudge", snapshot: base,
			observation: Observation{ID: "idle-1", TaskID: "task-1", Type: ObservationTimerIdle},
			action:      ActionNudge, cost: BudgetCost{Nudges: 1},
		},
		{
			name: "idle recovery after nudges", snapshot: withBudget(base, Budget{Nudges: policy.MaxNudges}),
			observation: Observation{ID: "idle-2", TaskID: "task-1", Type: ObservationTimerIdle},
			action:      ActionCancelAndFollowUp, cost: BudgetCost{Retries: 1},
		},
		{
			name: "idle exhausted", snapshot: withBudget(base, Budget{Nudges: policy.MaxNudges, Retries: policy.MaxRetries}),
			observation: Observation{ID: "idle-3", TaskID: "task-1", Type: ObservationTimerIdle},
			action:      ActionAttentionRequired,
		},
		{
			name: "hard timeout", snapshot: base,
			observation: Observation{ID: "hard-1", TaskID: "task-1", Type: ObservationTimerHardTimeout},
			action:      ActionAttentionRequired,
		},
		{
			name: "failed tests repair", snapshot: base,
			observation: Observation{ID: "test-1", TaskID: "task-1", Type: ObservationTestFinished,
				Data: VerificationData{Verifier: "test", Passed: false, Summary: "exit 1"}},
			action: ActionRepair, cost: BudgetCost{TestRepairs: 1},
		},
		{
			name: "git failure escalates", snapshot: base,
			observation: Observation{ID: "git-1", TaskID: "task-1", Type: ObservationGitFinished,
				Data: VerificationData{Verifier: "git", Passed: false, SecurityViolation: true, Summary: "HEAD changed"}},
			action: ActionAttentionRequired,
		},
		{
			name: "disconnect retries", snapshot: base,
			observation: Observation{ID: "disconnect-1", TaskID: "task-1", Type: ObservationAgentDisconnected},
			action:      ActionRetrySession, cost: BudgetCost{Retries: 1},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			decisions := (Engine{}).Evaluate(test.snapshot, test.observation)
			if len(decisions) != 1 {
				t.Fatalf("got %d decisions, want 1", len(decisions))
			}
			if decisions[0].Action != test.action {
				t.Fatalf("action = %q, want %q", decisions[0].Action, test.action)
			}
			if decisions[0].BudgetCost != test.cost {
				t.Fatalf("cost = %#v, want %#v", decisions[0].BudgetCost, test.cost)
			}
			if decisions[0].DedupeKey == "" || len(decisions[0].Evidence) != 1 {
				t.Fatalf("decision is missing audit fields: %#v", decisions[0])
			}
		})
	}
}

func TestEngineIgnoresTerminalAndForeignObservations(t *testing.T) {
	observation := Observation{ID: "idle", TaskID: "other", Type: ObservationTimerIdle}
	snapshot := Snapshot{TaskID: "task", Status: domain.TaskRunning, Policy: DefaultPolicy()}
	if got := (Engine{}).Evaluate(snapshot, observation); len(got) != 0 {
		t.Fatalf("foreign observation produced %#v", got)
	}
	snapshot.Status = domain.TaskAttention
	observation.TaskID = snapshot.TaskID
	if got := (Engine{}).Evaluate(snapshot, observation); len(got) != 0 {
		t.Fatalf("terminal task produced %#v", got)
	}
}

func TestTimeoutProbeUsesInjectedClock(t *testing.T) {
	started := time.Date(2026, 9, 18, 8, 0, 0, 0, time.UTC)
	clock := fixedClock{now: started.Add(31 * time.Minute)}
	snapshot := Snapshot{
		TaskID: "task-1", Status: domain.TaskRunning, Policy: DefaultPolicy(),
		StartedAt: started, LastProgressAt: started.Add(29 * time.Minute), IdleSequence: 2,
	}
	observations := (TimeoutProbe{Clock: clock}).Observe(snapshot)
	if len(observations) != 2 {
		t.Fatalf("got %d observations, want hard and idle timeout", len(observations))
	}
	if observations[0].Type != ObservationTimerHardTimeout || observations[1].Type != ObservationTimerIdle {
		t.Fatalf("unexpected observations: %#v", observations)
	}
	if observations[0].ID == observations[1].ID {
		t.Fatal("timeout observations need distinct IDs")
	}
}

func withBudget(snapshot Snapshot, budget Budget) Snapshot {
	snapshot.Budget = budget
	return snapshot
}

type fixedClock struct{ now time.Time }

func (f fixedClock) Now() time.Time { return f.now }
