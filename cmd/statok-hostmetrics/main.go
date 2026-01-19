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

type stringFlag struct {
	value string
	set   bool
}

func (f *stringFlag) String() string { return f.value }

func (f *stringFlag) Set(v string) error {
	f.value = v
	f.set = true
	return nil
}

func main() {
	var verbose bool
	var workloadFlag stringFlag
	flag.Var(&workloadFlag, "workload", "workload label injected into every metric")
	flag.Var(&workloadFlag, "w", "shorthand for --workload")
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

	workload := strings.TrimSpace(workloadFlag.value)
	if workloadFlag.set {
		if workload == "" {
			log.Fatal("statok: workload is empty")
		}
	} else if workload == "" {
		host, err := os.Hostname()
		if err != nil {
			log.Fatalf("statok: workload not set and hostname lookup failed: %v", err)
		}
		workload = strings.TrimSpace(host)
		if workload == "" {
			log.Fatal("statok: workload not set and hostname is empty")
		}
	}

	_, err := statok.Init(workload, statok.Config{
		Endpoint:          endpoint,
		QueueSize:         64_000,
		MaxBatchSize:      2_000,
		MaxSeriesPerBatch: 5_000,
		FlushInterval:     2 * time.Second,
		LocalAggCounters:  true,
		ValueMode:         statok.ValueAggregationBatch,
		Verbose:           verbose,
	})
	if err != nil {
		log.Fatalf("statok: init failed: %v", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	agent.Run(ctx, catalog.CoreCollectors(), catalog.IntegrationProbes())

	flushCtx, flushCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer flushCancel()
	if client := statok.Default(); client != nil {
		_ = client.Close(flushCtx)
	}
}
