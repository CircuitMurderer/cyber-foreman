package event

import (
	"context"
	"sync"
	"sync/atomic"

	"cyber-foreman/internal/domain"
)

type Bus struct {
	mu     sync.RWMutex
	nextID atomic.Uint64
	subs   map[uint64]chan domain.Event
}

func NewBus() *Bus {
	return &Bus{subs: make(map[uint64]chan domain.Event)}
}

func (b *Bus) Publish(event domain.Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, subscriber := range b.subs {
		select {
		case subscriber <- event:
		default:
		}
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
		delete(b.subs, id)
		close(ch)
		b.mu.Unlock()
	}()
	return ch
}
