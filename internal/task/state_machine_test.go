package task

import (
	"testing"

	"cyber-foreman/internal/domain"
)

func TestCanTransition(t *testing.T) {
	tests := []struct {
		name string
		from domain.TaskStatus
		to   domain.TaskStatus
		want bool
	}{
		{"start", domain.TaskQueued, domain.TaskRunning, true},
		{"verify", domain.TaskRunning, domain.TaskVerifying, true},
		{"complete", domain.TaskVerifying, domain.TaskCompleted, true},
		{"skip verification", domain.TaskRunning, domain.TaskCompleted, false},
		{"restart completed", domain.TaskCompleted, domain.TaskRunning, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CanTransition(tt.from, tt.to); got != tt.want {
				t.Fatalf("CanTransition(%q, %q) = %v, want %v", tt.from, tt.to, got, tt.want)
			}
		})
	}
}
