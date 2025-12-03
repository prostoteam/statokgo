package main

import (
	"context"
	"log"
	"math"
	"math/rand"
	"os"
	"os/signal"
	"time"

	statok "github.com/prostoteam/statokgo"
)

type metricKind int

const (
	kindCounter metricKind = iota
	kindValue
)

type metricSpec struct {
	Name       string
	Kind       metricKind
	Interval   time.Duration
	BurstSize  int
	Base       float64
	Jitter     float64
	Labels     [][]string
	SlowDrift  bool
	SineWave   bool
	Spikey     bool
	HighVolume bool
}

func main() {
	_, err := statok.Init(statok.Config{
		Endpoint:          "http://localhost:8085/api/i/batch",
		QueueSize:         400_000,
		MaxBatchSize:      1_000_000,
		MaxSeriesPerBatch: 30_000,
		FlushInterval:     2 * time.Second,
		LocalAggCounters:  true,
		ValueMode:         statok.ValueAggregationBatch,
	})
	if err != nil {
		log.Fatalf("statok: init failed: %v", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	StartSyntheticMetrics(ctx)

	<-ctx.Done()

	flushCtx, flushCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer flushCancel()

	if client := statok.Default(); client != nil {
		_ = client.Close(flushCtx)
	}
}

// StartSyntheticMetrics spawns goroutines that continuously emit artificial
// Statok metrics. It is useful for exercising queue pressure, aggregation and
// batching logic during development.
func StartSyntheticMetrics(ctx context.Context) {
	rand.Seed(time.Now().UnixNano())

	commonLabels := [][]string{
		{statok.Label("env", "dev"), statok.Label("region", "eu-west-1")},
		{statok.Label("env", "dev"), statok.Label("region", "us-east-1")},
		{statok.Label("env", "staging"), statok.Label("region", "eu-west-1")},
	}

	specs := []metricSpec{
		{"metric_test_counter_1", kindCounter, 500 * time.Millisecond, 1, 1, 0.3, commonLabels, true, false, false, false},
		{"metric_test_counter_2", kindCounter, 750 * time.Millisecond, 1, 2, 0.5, commonLabels, false, true, false, false},
		{"metric_test_counter_3", kindCounter, time.Second, 1, 1, 1.0, commonLabels, false, false, true, false},
		{"metric_test_counter_4", kindCounter, 2 * time.Second, 1, 5, 1.0, commonLabels, false, false, false, false},
		{"metric_test_counter_5", kindCounter, 250 * time.Millisecond, 2, 1, 0.2, commonLabels, true, false, false, false},

		// High load counters to test batching
		{"metric_test_counter_hot_1", kindCounter, 10 * time.Millisecond, 100, 1, 0.1, commonLabels, false, false, false, true},
		{"metric_test_counter_hot_2", kindCounter, 5 * time.Millisecond, 200, 1, 0.1, commonLabels, false, false, false, true},
	}

	specs = append(specs,
		metricSpec{"metric_test_counter_hot_3", kindCounter, time.Millisecond, 500, 1, 0.1, commonLabels, false, false, false, true},
		metricSpec{"metric_test_value_1", kindValue, 500 * time.Millisecond, 1, 100, 10, commonLabels, false, true, false, false},
		metricSpec{"metric_test_value_2", kindValue, 200 * time.Millisecond, 1, 20, 5, commonLabels, true, false, false, false},
		metricSpec{"metric_test_value_3", kindValue, 750 * time.Millisecond, 1, 0.5, 0.2, commonLabels, false, false, true, false},
		metricSpec{"metric_test_value_4", kindValue, time.Second, 1, 50, 15, commonLabels, false, false, false, false},
		metricSpec{"metric_test_value_5", kindValue, 300 * time.Millisecond, 1, 1, 0.3, commonLabels, false, false, false, false},
		metricSpec{"metric_test_value_hot_1", kindValue, 10 * time.Millisecond, 50, 100, 30, commonLabels, false, false, false, true},
		metricSpec{"metric_test_value_hot_2", kindValue, 5 * time.Millisecond, 100, 50, 10, commonLabels, false, false, false, true},
		metricSpec{"metric_test_counter_error_rate", kindCounter, 800 * time.Millisecond, 1, 1, 0.5, commonLabels, false, false, true, false},
		metricSpec{"metric_test_value_latency_p99", kindValue, 400 * time.Millisecond, 1, 300, 50, commonLabels, false, true, true, false},
		metricSpec{"metric_test_value_cpu", kindValue, 250 * time.Millisecond, 1, 40, 20, commonLabels, false, true, false, false},
		metricSpec{"metric_test_value_mem_rss", kindValue, 600 * time.Millisecond, 1, 1024, 128, commonLabels, true, false, false, false},
	)

	for _, spec := range specs {
		go runMetricLoop(ctx, spec)
	}
}

func runMetricLoop(ctx context.Context, spec metricSpec) {
	var tick uint64
	drift := 0.0

	ticker := time.NewTicker(spec.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		tick++
		burstSize := spec.BurstSize
		if spec.Spikey && rand.Float64() < 0.02 {
			burstSize *= 50
		}
		if spec.HighVolume && rand.Float64() < 0.01 {
			burstSize *= 20
		}
		for i := 0; i < burstSize; i++ {
			val := spec.Base
			if spec.SineWave {
				angle := float64(tick%360) * (math.Pi / 180.0)
				val = spec.Base + math.Sin(angle)*spec.Jitter
			} else {
				val += (rand.Float64()*2 - 1) * spec.Jitter
			}
			if spec.SlowDrift {
				drift += (rand.Float64()*2 - 1) * 0.05
				val += drift
			}
			if spec.Spikey && rand.Float64() < 0.001 {
				val *= 10
			}
			lbls := spec.Labels[rand.Intn(len(spec.Labels))]
			switch spec.Kind {
			case kindCounter:
				delta := val
				if delta <= 0 {
					delta = 1
				}
				statok.Count(spec.Name, delta, lbls...)
			case kindValue:
				statok.Value(spec.Name, val, lbls...)
			}
		}
	}
}
