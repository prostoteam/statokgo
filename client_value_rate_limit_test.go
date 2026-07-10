package statok

import (
	"testing"
	"time"
)

func TestClientValueSparseRateLimitRunsBeforeQueueing(t *testing.T) {
	client := &Client{
		queue:    make(chan *event, 1),
		done:     make(chan struct{}),
		workload: "api",
	}
	client.valueLimiter.state.Store(uint64(time.Now().Unix()+60)<<rateLimitCounterBits | maxValueEventsPerSecond)

	client.ValueSparse("load", 1, "host=example")
	if got := len(client.queue); got != 0 {
		t.Fatalf("queue depth = %d, want 0", got)
	}
}
