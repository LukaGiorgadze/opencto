package bus

import (
	"context"
	"sync"

	"github.com/opencto/opencto/internal/domain"
)

type EventBus struct {
	mu          sync.RWMutex
	subscribers map[int]chan domain.Event
	nextID      int
}

func New() *EventBus {
	return &EventBus{
		subscribers: make(map[int]chan domain.Event),
	}
}

func (b *EventBus) Publish(ctx context.Context, event domain.Event) error {
	b.mu.RLock()
	defer b.mu.RUnlock()

	for _, ch := range b.subscribers {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ch <- event:
		}
	}

	return nil
}

func (b *EventBus) Subscribe(buffer int) (<-chan domain.Event, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()

	id := b.nextID
	b.nextID++

	ch := make(chan domain.Event, buffer)
	b.subscribers[id] = ch

	cancel := func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if existing, ok := b.subscribers[id]; ok {
			delete(b.subscribers, id)
			close(existing)
		}
	}

	return ch, cancel
}
