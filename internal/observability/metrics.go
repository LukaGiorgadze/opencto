package observability

import "time"

type Metrics interface {
	IncrementCounter(name string, labels map[string]string)
	ObserveDuration(name string, value time.Duration, labels map[string]string)
}

type NoopMetrics struct{}

func (NoopMetrics) IncrementCounter(string, map[string]string) {}

func (NoopMetrics) ObserveDuration(string, time.Duration, map[string]string) {}
