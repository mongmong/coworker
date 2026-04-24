package eventbus

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/chris/coworker/core"
)

func TestInMemoryBus_SubscribePublishUnsubscribe(t *testing.T) {
	t.Parallel()

	bus := NewInMemoryBus()
	sub := make(chan *core.Event, 1)
	bus.Subscribe(sub)

	event := &core.Event{
		ID:            "evt_subscribe",
		RunID:         "run_subscribe",
		Sequence:      1,
		Kind:          core.EventRunCreated,
		SchemaVersion: 1,
		Payload:       `{"run_id":"run_subscribe"}`,
		CreatedAt:     time.Unix(1, 0).UTC(),
	}

	bus.Publish(event)

	select {
	case got := <-sub:
		if got != event {
			t.Fatalf("received event pointer %p, want %p", got, event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for published event")
	}

	bus.Unsubscribe(sub)
	bus.Publish(&core.Event{ID: "evt_after_unsubscribe"})

	select {
	case got := <-sub:
		t.Fatalf("received event after unsubscribe: %+v", got)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestInMemoryBus_SlowSubscriberDoesNotBlock(t *testing.T) {
	t.Parallel()

	bus := NewInMemoryBus()
	slow := make(chan *core.Event)
	fast := make(chan *core.Event, 1)

	bus.Subscribe(slow)
	bus.Subscribe(fast)

	done := make(chan struct{})
	event := &core.Event{
		ID:            "evt_non_blocking",
		RunID:         "run_non_blocking",
		Sequence:      1,
		Kind:          core.EventJobCreated,
		SchemaVersion: 1,
		Payload:       `{"job_id":"job_1"}`,
		CreatedAt:     time.Unix(2, 0).UTC(),
	}

	go func() {
		bus.Publish(event)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("Publish blocked on a slow subscriber")
	}

	select {
	case got := <-fast:
		if got != event {
			t.Fatalf("fast subscriber received %p, want %p", got, event)
		}
	case <-time.After(time.Second):
		t.Fatal("fast subscriber did not receive the event")
	}
}

func TestInMemoryBus_ConcurrentPublishIsSafe(t *testing.T) {
	t.Parallel()

	const (
		publishers         = 8
		eventsPerPublisher = 25
		totalEvents        = publishers * eventsPerPublisher
	)

	bus := NewInMemoryBus()
	sub := make(chan *core.Event, totalEvents)
	bus.Subscribe(sub)

	var wg sync.WaitGroup
	for publisher := 0; publisher < publishers; publisher++ {
		publisher := publisher
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < eventsPerPublisher; i++ {
				bus.Publish(&core.Event{
					ID:            fmt.Sprintf("evt_%d_%d", publisher, i),
					RunID:         "run_concurrent",
					Sequence:      publisher*eventsPerPublisher + i + 1,
					Kind:          core.EventJobCompleted,
					SchemaVersion: 1,
					Payload:       fmt.Sprintf(`{"publisher":%d,"index":%d}`, publisher, i),
					CreatedAt:     time.Unix(int64(i+1), 0).UTC(),
				})
			}
		}()
	}

	wg.Wait()

	received := 0
	deadline := time.After(2 * time.Second)
	for received < totalEvents {
		select {
		case <-sub:
			received++
		case <-deadline:
			t.Fatalf("received %d events, want %d", received, totalEvents)
		}
	}
}
