package statok

import (
	"context"
	"net/http"
	"testing"
	"time"
)

type discardTransport struct{}

func (discardTransport) Send(context.Context, *Payload) error { return nil }

type flakyBenchmarkTransport struct {
	failEvery int
	calls     int
}

func (t *flakyBenchmarkTransport) Send(_ context.Context, _ *Payload) error {
	t.calls++
	if t.failEvery > 0 && t.calls%t.failEvery == 0 {
		return &HTTPTransportError{
			Endpoint:   "http://statok.test/api/i/batch",
			StatusCode: http.StatusServiceUnavailable,
			Status:     "503 Service Unavailable",
		}
	}
	return nil
}

func newBenchmarkClient(b *testing.B) *Client {
	b.Helper()

	c, err := NewClient("bench-workload", Config{
		Transport: discardTransport{},
		Logger:    noopLogger{},
	})
	if err != nil {
		b.Fatalf("NewClient() error = %v", err)
	}

	b.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = c.Close(ctx)
	})

	return c
}

func BenchmarkClientCountParallel(b *testing.B) {
	c := newBenchmarkClient(b)
	labels := []string{
		Label("env", "prod"),
		Label("region", "us-east-1"),
		Label("host", "bench-01"),
	}

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			c.Count("bench.counter", 1, labels...)
		}
	})
}

func BenchmarkDefaultCountParallel(b *testing.B) {
	c := newBenchmarkClient(b)
	defaultMu.Lock()
	defaultClient = c
	defaultMu.Unlock()
	b.Cleanup(func() {
		defaultMu.Lock()
		if defaultClient == c {
			defaultClient = nil
		}
		defaultMu.Unlock()
	})

	labels := []string{
		Label("env", "prod"),
		Label("region", "us-east-1"),
		Label("host", "bench-01"),
	}

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			Count("bench.counter", 1, labels...)
		}
	})
}

func BenchmarkClientValueParallel(b *testing.B) {
	c := newBenchmarkClient(b)
	labels := []string{
		Label("env", "prod"),
		Label("region", "us-east-1"),
		Label("host", "bench-01"),
	}

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			c.Value("bench.value", 42.5, labels...)
		}
	})
}

func BenchmarkClientCountDropWhenQueueFull(b *testing.B) {
	c := &Client{
		queue:    make(chan *event, 1),
		done:     make(chan struct{}),
		logger:   noopLogger{},
		workload: "bench-workload",
	}
	c.queue <- borrowEvent(metricTypeCounter, "prefill", 1, nil)

	labels := []string{
		Label("env", "prod"),
		Label("region", "us-east-1"),
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.Count("bench.counter", 1, labels...)
	}
}

func BenchmarkBatchBuilderCounters(b *testing.B) {
	events := make([]*event, 0, defaultMaxBatchSize)
	for i := 0; i < defaultMaxBatchSize; i++ {
		events = append(events, &event{
			typ:   metricTypeCounter,
			name:  "bench.counter",
			value: 1,
			labels: []string{
				Label("env", "prod"),
				Label("host", "bench-01"),
				Label("shard", string(rune('a'+(i%8)))),
			},
			ts: time.Unix(1700000000+int64(i), 0),
		})
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		builder := newBatchBuilder(len(events))
		for _, ev := range events {
			builder.add(ev)
		}
		if payload := builder.build(); payload == nil {
			b.Fatal("builder returned nil payload")
		}
	}
}

func BenchmarkBatchBuilderValues(b *testing.B) {
	events := make([]*event, 0, defaultMaxBatchSize)
	for i := 0; i < defaultMaxBatchSize; i++ {
		events = append(events, &event{
			typ:   metricTypeValue,
			name:  "bench.value",
			value: 42.5,
			labels: []string{
				Label("env", "prod"),
				Label("host", "bench-01"),
				Label("shard", string(rune('a'+(i%8)))),
			},
			ts: time.Unix(1700000000+int64(i), 0),
		})
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		builder := newBatchBuilder(len(events))
		for _, ev := range events {
			builder.add(ev)
		}
		if payload := builder.build(); payload == nil {
			b.Fatal("builder returned nil payload")
		}
	}
}

func BenchmarkClientBuildPayload(b *testing.B) {
	c := &Client{batchSession: "bench-session"}
	events := make([]*event, 0, defaultMaxBatchSize)
	for i := 0; i < defaultMaxBatchSize; i++ {
		events = append(events, &event{
			typ:   metricTypeCounter,
			name:  "bench.counter",
			value: 1,
			labels: []string{
				Label("env", "prod"),
				Label("host", "bench-01"),
				Label("shard", string(rune('a'+(i%8)))),
			},
			ts: time.Unix(1700000000+int64(i), 0),
		})
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		payload := c.buildPayload(events)
		if payload == nil || payload.BatchID == "" {
			b.Fatal("buildPayload() returned empty payload or missing batch id")
		}
	}
}

func BenchmarkClientSendPayload(b *testing.B) {
	newPayload := func() *Payload {
		return &Payload{
			BatchID: "bench-batch",
			Counters: []CounterEvent{{
				Metric:    "bench.counter",
				Value:     1,
				Labels:    []string{Label("env", "prod"), Label("host", "bench-01")},
				Timestamp: 1700000000,
			}},
		}
	}

	b.Run("success", func(b *testing.B) {
		c := &Client{
			cfg:    Config{Transport: discardTransport{}, Logger: noopLogger{}},
			logger: noopLogger{},
		}
		retryQueue := make([]retryBatch, 0, defaultRetryQueueSize)

		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			payload := newPayload()
			c.sendPayload(payload, 1, &retryQueue, false)
		}
	})

	b.Run("retryable_failure_then_requeue", func(b *testing.B) {
		tr := &flakyBenchmarkTransport{failEvery: 1}
		c := &Client{
			cfg:    Config{Transport: tr, Logger: noopLogger{}},
			logger: noopLogger{},
		}

		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			retryQueue := make([]retryBatch, 0, defaultRetryQueueSize)
			payload := newPayload()
			c.sendPayload(payload, 1, &retryQueue, false)
			if len(retryQueue) != 1 {
				b.Fatalf("retryQueue len = %d, want 1", len(retryQueue))
			}
		}
	})
}
