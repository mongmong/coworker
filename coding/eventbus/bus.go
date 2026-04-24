package eventbus

import (
	"log/slog"
	"sync"

	"github.com/chris/coworker/core"
)

// InMemoryBus fan-outs committed runtime events to live subscribers.
// It intentionally keeps no replay buffer; the SQLite event log is authoritative.
type InMemoryBus struct {
	mu          sync.RWMutex
	subscribers map[chan<- *core.Event]struct{}
}

// NewInMemoryBus creates an empty in-memory event bus.
func NewInMemoryBus() *InMemoryBus {
	return &InMemoryBus{
		subscribers: make(map[chan<- *core.Event]struct{}),
	}
}

// Publish sends the event to all current subscribers without blocking.
func (b *InMemoryBus) Publish(event *core.Event) {
	if event == nil {
		return
	}

	b.mu.RLock()
	subscribers := make([]chan<- *core.Event, 0, len(b.subscribers))
	for ch := range b.subscribers {
		subscribers = append(subscribers, ch)
	}
	b.mu.RUnlock()

	for _, ch := range subscribers {
		select {
		case ch <- event:
		default:
			slog.Warn("event dropped for slow subscriber", "event_kind", string(event.Kind), "run_id", event.RunID)
		}
	}
}

// Subscribe registers a subscriber channel for future published events.
func (b *InMemoryBus) Subscribe(ch chan<- *core.Event) {
	if ch == nil {
		return
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	b.subscribers[ch] = struct{}{}
}

// Unsubscribe removes a subscriber channel.
func (b *InMemoryBus) Unsubscribe(ch chan<- *core.Event) {
	if ch == nil {
		return
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.subscribers, ch)
}

var _ core.EventBus = (*InMemoryBus)(nil)
