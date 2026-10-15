package tower

import (
	"sync"
	"time"
)

// Event is something an operator did or the system noticed, drawn as a
// marker on the timeline so cause and effect line up.
type Event struct {
	T       int64  `json:"t"` // unix ms
	Kind    string `json:"kind"`
	Service string `json:"service,omitempty"`
	Text    string `json:"text"`
}

type eventLog struct {
	mu     sync.Mutex
	events []Event
}

func newEventLog() *eventLog { return &eventLog{} }

func (l *eventLog) add(kind, service, text string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, Event{T: time.Now().UnixMilli(), Kind: kind, Service: service, Text: text})
	if len(l.events) > 400 {
		l.events = append([]Event(nil), l.events[len(l.events)-300:]...)
	}
}

// since returns events at or after t.
func (l *eventLog) since(t time.Time) []Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	ms := t.UnixMilli()
	var out []Event
	for i := len(l.events) - 1; i >= 0 && l.events[i].T >= ms; i-- {
		out = append([]Event{l.events[i]}, out...)
	}
	return out
}

func (l *eventLog) recent(n int) []Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.events) <= n {
		return append([]Event(nil), l.events...)
	}
	return append([]Event(nil), l.events[len(l.events)-n:]...)
}
