package core

// EventBus is the live fan-out surface for already-committed runtime events.
// SQLite remains the source of truth; the bus is used for real-time observers.
type EventBus interface {
	Publish(event *Event)
	Subscribe(ch chan<- *Event)
	Unsubscribe(ch chan<- *Event)
}
