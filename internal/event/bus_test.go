package event

import (
	"context"
	"strings"
	"testing"
	"time"

	"cyber-foreman/internal/domain"
)

func TestBusReplaysEventsAndReportsHistoryGap(t *testing.T) {
	bus := NewBusWithHistory(2)
	for i := 0; i < 3; i++ {
		bus.Publish(domain.Event{TaskID: "task", Type: domain.EventAgentOutput, Timestamp: time.Now().UTC()})
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, gap := bus.SubscribeSince(ctx, 0, 1)
	if gap {
		t.Fatal("initial replay unexpectedly reported a gap")
	}
	first := <-events
	second := <-events
	if first.Sequence != 2 || second.Sequence != 3 || first.ID == "" || first.Version != "v1" {
		t.Fatalf("unexpected replay: %#v %#v", first, second)
	}

	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	_, gap = bus.SubscribeSince(ctx2, 1, 1)
	if gap {
		t.Fatal("sequence immediately before retained history should be replayable")
	}
	bus.Publish(domain.Event{TaskID: "task", Type: domain.EventAgentOutput, Timestamp: time.Now().UTC()})
	bus.Publish(domain.Event{TaskID: "task", Type: domain.EventAgentOutput, Timestamp: time.Now().UTC()})
	ctx3, cancel3 := context.WithCancel(context.Background())
	defer cancel3()
	_, gap = bus.SubscribeSince(ctx3, 1, 1)
	if !gap {
		t.Fatal("cursor older than retained history should report a gap")
	}
}

func TestBusDisconnectsSlowSubscriberInsteadOfSilentlyDropping(t *testing.T) {
	bus := NewBusWithHistory(4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := bus.Subscribe(ctx, 1)
	bus.Publish(domain.Event{TaskID: "task", Type: domain.EventAgentOutput})
	bus.Publish(domain.Event{TaskID: "task", Type: domain.EventAgentOutput})
	if _, ok := <-events; !ok {
		t.Fatal("buffered first event was lost")
	}
	if _, ok := <-events; ok {
		t.Fatal("slow subscriber remained open after overflow")
	}
}

func TestBusCapsLargePayloadInReplayHistory(t *testing.T) {
	bus := NewBusWithHistory(2)
	bus.Publish(domain.Event{TaskID: "task", Type: domain.EventAgentOutput, Data: strings.Repeat("x", maxStoredEventData+1)})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, _ := bus.SubscribeSince(ctx, 0, 1)
	event := <-events
	data, ok := event.Data.(map[string]any)
	if !ok || data["truncated"] != true {
		t.Fatalf("large replay payload was not capped: %#v", event.Data)
	}
}
