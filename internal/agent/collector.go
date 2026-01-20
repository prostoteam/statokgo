package agent

import (
	"context"
	"time"
)

// Collector produces metrics on a fixed cadence.
// Implementations must emit metrics directly via statok.Value / statok.Count.
type Collector interface {
	ID() string
	Every() time.Duration
	Collect(ctx context.Context) error
}

// CollectorCloser is an optional interface for collectors that need shutdown cleanup.
type CollectorCloser interface {
	Close(ctx context.Context) error
}
