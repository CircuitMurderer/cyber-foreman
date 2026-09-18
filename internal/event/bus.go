package event

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"

	"cyber-foreman/internal/domain"
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

func (b *Bus) Publish(event domain.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.seq++
	event.Sequence = b.seq
	event.ID = fmt.Sprintf("evt-%d", event.Sequence)
	event.Version = "v1"
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
}

func (b *Bus) recordHistory(event domain.Event) {
	stored := event
	if data, err := json.Marshal(event.Data); err == nil && len(data) > maxStoredEventData {
		stored.Data = map[string]any{"truncated": true, "original_bytes": len(data)}
	}
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
	b.mu.Lock()
	oldest := uint64(0)
	if len(b.history) > 0 {
		oldest = b.history[0].Sequence
	}
	gap := after > 0 && oldest > 0 && after+1 < oldest
	replay := make([]domain.Event, 0, len(b.history))
	for _, event := range b.history {
		if event.Sequence > after {
			replay = append(replay, event)
		}
	}
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
	return ch, gap
}
