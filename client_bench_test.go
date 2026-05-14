package statok

import (
	"context"
	"testing"
	"time"
)

type discardTransport struct{}

func (discardTransport) Send(context.Context, *Payload) error { return nil }

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
		queue:         make(chan *event, 1),
		done:          make(chan struct{}),
		logger:        noopLogger{},
		workloadLabel: Label("workload", "bench-workload"),
	}
	c.queue <- borrowEvent(metricTypeCounter, "prefill", 1, c.workloadLabel, nil)

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
				Label("workload", "bench-workload"),
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
				Label("workload", "bench-workload"),
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
