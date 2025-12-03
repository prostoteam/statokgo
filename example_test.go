package statok_test

import (
	"context"
	"fmt"
	"time"

	"github.com/prostoteam/statokgo"
)

type memoryTransport struct {
	batches []*statok.Payload
}

func (m *memoryTransport) Send(_ context.Context, p *statok.Payload) error {
	m.batches = append(m.batches, p)
	return nil
}

func ExampleClient() {
	mt := &memoryTransport{}
	client, err := statok.NewClient(statok.Config{
		Transport:        mt,
		FlushInterval:    50 * time.Millisecond,
		MaxBatchSize:     16,
		LocalAggCounters: true,
		ValueMode:        statok.ValueAggregationBatch,
	})
	if err != nil {
		panic(err)
	}
	for i := 0; i < 5; i++ {
		client.Count("requests_total", 1, "method=GET")
		client.Value("latency_ms", 123.4, "method=GET")
	}

	client.Close(context.Background())
	fmt.Printf("batches=%d counters=%d values=%d\n",
		len(mt.batches),
		len(mt.batches[0].Counters),
		len(mt.batches[0].Values))
	// Output:
	// batches=1 counters=1 values=1
}
