package signals

import "github.com/opencto/opencto/internal/domain"

type EnqueueEventSignal struct {
	Event domain.Event
}
