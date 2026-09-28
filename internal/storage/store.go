package storage

import "cyber-foreman/internal/domain"

// TaskStore persists task snapshots used to restore the control plane after a
// restart. Runtime-only state such as process handles and ACP sessions is never
// persisted.
type TaskStore interface {
	PutTask(domain.Task) error
	DeleteTask(string) error
	ListTasks() ([]domain.Task, error)
}

// EventJournal is the durable append-only source for event replay.
type EventJournal interface {
	AppendEvent(domain.Event) (domain.Event, error)
	EventsAfter(after uint64, taskID string) ([]domain.Event, error)
}

type Store interface {
	TaskStore
	EventJournal
	Close() error
}
