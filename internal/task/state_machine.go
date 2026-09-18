package task

import "cyber-foreman/internal/domain"

var transitions = map[domain.TaskStatus]map[domain.TaskStatus]bool{
	domain.TaskQueued: {
		domain.TaskRunning: true, domain.TaskFailed: true, domain.TaskStopped: true,
		domain.TaskAttention: true,
	},
	domain.TaskRunning: {
		domain.TaskRecovering: true, domain.TaskVerifying: true,
		domain.TaskFailed: true, domain.TaskStopped: true, domain.TaskAttention: true,
	},
	domain.TaskRecovering: {
		domain.TaskRunning: true, domain.TaskFailed: true, domain.TaskStopped: true,
		domain.TaskAttention: true,
	},
	domain.TaskVerifying: {
		domain.TaskRunning: true, domain.TaskCompleted: true,
		domain.TaskFailed: true, domain.TaskStopped: true, domain.TaskAttention: true,
	},
}

func CanTransition(from, to domain.TaskStatus) bool {
	return transitions[from][to]
}
