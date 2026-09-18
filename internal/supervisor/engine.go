package supervisor

import "fmt"

type Engine struct{}

func (Engine) Evaluate(snapshot Snapshot, observation Observation) []Decision {
	if snapshot.Status.Terminal() || observation.TaskID != snapshot.TaskID || observation.ID == "" {
		return nil
	}

	switch observation.Type {
	case ObservationGitFinished:
		result, ok := observation.Data.(VerificationData)
		if ok && !result.Passed {
			return one(decision(observation, "git-verification-failed", ActionAttentionRequired,
				"workspace verification failed: "+result.Summary, BudgetCost{}))
		}
	case ObservationTimerHardTimeout:
		return one(decision(observation, "hard-timeout", ActionAttentionRequired,
			"task exceeded its hard timeout", BudgetCost{}))
	case ObservationTestFinished:
		result, ok := observation.Data.(VerificationData)
		if !ok || result.Passed {
			break
		}
		if snapshot.Budget.TestRepairs >= snapshot.Policy.MaxTestRepairs {
			return one(decision(observation, "test-repair-budget-exhausted", ActionAttentionRequired,
				"test repair budget exhausted", BudgetCost{}))
		}
		return one(decision(observation, "test-failed", ActionRepair,
			"configured tests failed: "+result.Summary, BudgetCost{TestRepairs: 1}))
	case ObservationAgentDisconnected, ObservationAgentExited:
		if observation.Type == ObservationAgentExited {
			data, ok := observation.Data.(AgentExitData)
			if !ok || data.ExitCode == 0 {
				break
			}
		}
		if snapshot.Budget.Retries >= snapshot.Policy.MaxRetries {
			return one(decision(observation, "retry-budget-exhausted", ActionAttentionRequired,
				"agent retry budget exhausted", BudgetCost{}))
		}
		return one(decision(observation, "agent-disconnected", ActionRetrySession,
			"agent session ended unexpectedly", BudgetCost{Retries: 1}))
	case ObservationTimerIdle:
		if snapshot.Budget.Nudges < snapshot.Policy.MaxNudges {
			return one(decision(observation, "idle-nudge", ActionNudge,
				"agent made no meaningful progress before the idle deadline", BudgetCost{Nudges: 1}))
		}
		if snapshot.Budget.Retries < snapshot.Policy.MaxRetries {
			return one(decision(observation, "idle-recovery", ActionCancelAndFollowUp,
				"agent remained idle after nudge budget was exhausted", BudgetCost{Retries: 1}))
		}
		return one(decision(observation, "idle-budget-exhausted", ActionAttentionRequired,
			"idle recovery budget exhausted", BudgetCost{}))
	case ObservationAgentTurnFinished:
		return one(decision(observation, "turn-finished", ActionStartVerification,
			"agent turn finished; deterministic verification is required", BudgetCost{}))
	case ObservationVerificationReady:
		if snapshot.Verification.TestFinished && snapshot.Verification.TestPassed &&
			snapshot.Verification.GitFinished && snapshot.Verification.GitPassed {
			return one(decision(observation, "verification-passed", ActionComplete,
				"all required verifiers passed", BudgetCost{}))
		}
	}
	return nil
}

func decision(observation Observation, ruleID string, action Action, reason string, cost BudgetCost) Decision {
	return Decision{
		RuleID: ruleID, Action: action, Reason: reason,
		Evidence:  []string{observation.ID},
		DedupeKey: fmt.Sprintf("%s:%s", observation.ID, ruleID), BudgetCost: cost,
	}
}

func one(value Decision) []Decision { return []Decision{value} }
