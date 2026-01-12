package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	statok "github.com/prostoteam/statokgo"
	"github.com/prostoteam/statokgo/internal/agent"
	"github.com/prostoteam/statokgo/internal/agent/catalog"
)

const statokIngestHost = "statok.dev0101.xyz"

func main() {
	var workload string
	var verbose bool
	flag.StringVar(&workload, "workload", "", "workload label forwarded to the Statok backend")
	flag.StringVar(&workload, "w", "", "shorthand for --workload")
	flag.BoolVar(&verbose, "verbose", false, "enable verbose logging")
	flag.BoolVar(&verbose, "v", false, "shorthand for --verbose")
	flag.Parse()

	endpointHost := statokIngestHost
	if envEndpoint := strings.TrimSpace(os.Getenv("STATOK_ENDPOINT")); envEndpoint != "" {
		endpointHost = envEndpoint
	} else if envHost := strings.TrimSpace(os.Getenv("STATOK_HOST")); envHost != "" {
		endpointHost = envHost
	}

	endpoint := statok.EndpointFromHost(endpointHost)

	_, err := statok.Init(statok.Config{
		Endpoint:          endpoint,
		QueueSize:         64_000,
		MaxBatchSize:      2_000,
		MaxSeriesPerBatch: 5_000,
		FlushInterval:     2 * time.Second,
		LocalAggCounters:  true,
		ValueMode:         statok.ValueAggregationBatch,
		Verbose:           verbose,
		Workload:          workload,
	})
	if err != nil {
		log.Fatalf("statok: init failed: %v", err)
	}

	host := "unknown"
	if h, err := os.Hostname(); err == nil && h != "" {
		host = h
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	agent.Run(ctx, host, catalog.CoreCollectors(), catalog.IntegrationProbes())

	flushCtx, flushCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer flushCancel()
	if client := statok.Default(); client != nil {
		_ = client.Close(flushCtx)
	}
}
