package supervisor

import (
	"strconv"
	"time"
)

type Clock interface {
	Now() time.Time
}

type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now().UTC() }

type TimeoutProbe struct {
	Clock Clock
}

func (p TimeoutProbe) Observe(snapshot Snapshot) []Observation {
	clock := p.Clock
	if clock == nil {
		clock = RealClock{}
	}
	now := clock.Now().UTC()
	observations := make([]Observation, 0, 2)
	if snapshot.Policy.HardTimeout > 0 && !snapshot.StartedAt.IsZero() &&
		!now.Before(snapshot.StartedAt.Add(snapshot.Policy.HardTimeout)) {
		observations = append(observations, Observation{
			ID:     fmtObservationID(snapshot.TaskID, ObservationTimerHardTimeout, snapshot.StartedAt.Add(snapshot.Policy.HardTimeout), 0),
			TaskID: snapshot.TaskID, Type: ObservationTimerHardTimeout, Timestamp: now,
		})
	}
	if snapshot.Policy.IdleTimeout > 0 && !snapshot.LastProgressAt.IsZero() &&
		!now.Before(snapshot.LastProgressAt.Add(snapshot.Policy.IdleTimeout)) {
		observations = append(observations, Observation{
			ID:     fmtObservationID(snapshot.TaskID, ObservationTimerIdle, snapshot.LastProgressAt.Add(snapshot.Policy.IdleTimeout), snapshot.IdleSequence),
			TaskID: snapshot.TaskID, Type: ObservationTimerIdle, Timestamp: now,
		})
	}
	return observations
}

func fmtObservationID(taskID string, kind ObservationType, deadline time.Time, sequence int) string {
	return taskID + ":" + string(kind) + ":" + deadline.UTC().Format(time.RFC3339Nano) + ":" + strconv.Itoa(sequence)
}
