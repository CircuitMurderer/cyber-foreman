package event

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"

	"cyber-foreman/internal/domain"
	"cyber-foreman/internal/storage"
)

type Bus struct {
	mu              sync.Mutex
	nextID          atomic.Uint64
	seq             uint64
	limit           int
	maxHistoryBytes int
	historyBytes    int
	history         []domain.Event
	historySizes    []int
	subs            map[uint64]chan domain.Event
	journal         storage.EventJournal
}

const (
	defaultMaxHistoryBytes = 16 * 1024 * 1024
	maxStoredEventData     = 256 * 1024
)

func NewBus() *Bus {
	return NewBusWithHistory(4096)
}

func NewBusWithHistory(limit int) *Bus {
	if limit < 1 {
		limit = 1
	}
	return &Bus{limit: limit, maxHistoryBytes: defaultMaxHistoryBytes, subs: make(map[uint64]chan domain.Event)}
}

func NewBusWithJournal(journal storage.EventJournal) *Bus {
	bus := NewBus()
	bus.journal = journal
	return bus
}

func (b *Bus) Publish(event domain.Event) {
	_ = b.PublishChecked(event)
}

// PublishChecked is used for lifecycle events whose durable append must be
// acknowledged to the caller. Publish remains available for adapters and
// supervisor interfaces that intentionally expose a fire-and-forget sink.
func (b *Bus) PublishChecked(event domain.Event) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	event = compactEvent(event)
	if b.journal != nil {
		persisted, err := b.journal.AppendEvent(event)
		if err != nil {
			return err
		}
		event = persisted
		b.seq = event.Sequence
	} else {
		b.seq++
		event.Sequence = b.seq
		event.ID = fmt.Sprintf("evt-%d", event.Sequence)
		event.Version = "v1"
	}
	b.recordHistory(event)
	for id, subscriber := range b.subs {
		select {
		case subscriber <- event:
		default:
			// A slow consumer must reconnect from its last delivered sequence.
			// Disconnecting is observable; silently skipping an event is not.
			delete(b.subs, id)
			close(subscriber)
		}
	}
	return nil
}

func compactEvent(event domain.Event) domain.Event {
	stored := event
	if data, err := json.Marshal(event.Data); err == nil && len(data) > maxStoredEventData {
		stored.Data = map[string]any{"truncated": true, "original_bytes": len(data)}
	}
	return stored
}

func (b *Bus) recordHistory(event domain.Event) {
	stored := compactEvent(event)
	encoded, _ := json.Marshal(stored)
	size := len(encoded)
	b.history = append(b.history, stored)
	b.historySizes = append(b.historySizes, size)
	b.historyBytes += size
	remove := 0
	for len(b.history)-remove > b.limit || (b.historyBytes > b.maxHistoryBytes && len(b.history)-remove > 1) {
		b.historyBytes -= b.historySizes[remove]
		remove++
	}
	if remove > 0 {
		copy(b.history, b.history[remove:])
		b.history = b.history[:len(b.history)-remove]
		copy(b.historySizes, b.historySizes[remove:])
		b.historySizes = b.historySizes[:len(b.historySizes)-remove]
	}
}

func (b *Bus) Subscribe(ctx context.Context, buffer int) <-chan domain.Event {
	if buffer < 1 {
		buffer = 1
	}
	id := b.nextID.Add(1)
	ch := make(chan domain.Event, buffer)
	b.mu.Lock()
	b.subs[id] = ch
	b.mu.Unlock()

	go func() {
		<-ctx.Done()
		b.mu.Lock()
		if subscriber, ok := b.subs[id]; ok {
			delete(b.subs, id)
			close(subscriber)
		}
		b.mu.Unlock()
	}()
	return ch
}

// SubscribeSince atomically installs a live subscription and replays retained
// events after the requested sequence. gap is true when older requested events
// have already fallen out of the in-memory history window.
func (b *Bus) SubscribeSince(ctx context.Context, after uint64, buffer int) (<-chan domain.Event, bool) {
	events, gap, _ := b.subscribeSince(ctx, after, buffer, "")
	return events, gap
}

// SubscribeTaskSince replays one task's durable history before atomically
// switching to the live stream. A journal error is returned before headers are
// committed by the HTTP layer.
func (b *Bus) SubscribeTaskSince(ctx context.Context, after uint64, buffer int, taskID string) (<-chan domain.Event, bool, error) {
	return b.subscribeSince(ctx, after, buffer, taskID)
}

func (b *Bus) subscribeSince(ctx context.Context, after uint64, buffer int, taskID string) (<-chan domain.Event, bool, error) {
	b.mu.Lock()
	var replay []domain.Event
	if b.journal != nil {
		var err error
		replay, err = b.journal.EventsAfter(after, taskID)
		if err != nil {
			b.mu.Unlock()
			return nil, false, err
		}
	} else {
		replay = make([]domain.Event, 0, len(b.history))
		for _, event := range b.history {
			if event.Sequence > after && (taskID == "" || event.TaskID == taskID) {
				replay = append(replay, event)
			}
		}
	}
	oldest := uint64(0)
	if len(b.history) > 0 {
		oldest = b.history[0].Sequence
	}
	gap := b.journal == nil && after > 0 && oldest > 0 && after+1 < oldest
	if buffer < len(replay)+1 {
		buffer = len(replay) + 1
	}
	if buffer < 1 {
		buffer = 1
	}
	id := b.nextID.Add(1)
	ch := make(chan domain.Event, buffer)
	for _, event := range replay {
		ch <- event
	}
	b.subs[id] = ch
	b.mu.Unlock()

	go func() {
		<-ctx.Done()
		b.mu.Lock()
		if subscriber, ok := b.subs[id]; ok {
			delete(b.subs, id)
			close(subscriber)
		}
		b.mu.Unlock()
	}()
	return ch, gap, nil
}
