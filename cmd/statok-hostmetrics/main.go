package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	statok "github.com/prostoteam/statokgo"
	"github.com/prostoteam/statokgo/internal/agent"
	"github.com/prostoteam/statokgo/internal/agent/catalog"
	"github.com/prostoteam/statokgo/internal/collectors/mongo"
	"github.com/prostoteam/statokgo/internal/collectors/nginx"
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
	var configPath string
	flag.Var(&workloadFlag, "workload", "workload label injected into every metric")
	flag.Var(&workloadFlag, "w", "shorthand for --workload")
	flag.StringVar(&configPath, "config", "", "path to YAML config (optional)")
	flag.StringVar(&configPath, "c", "", "shorthand for --config")
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
	apiKey := os.Getenv("STATOK_API_KEY")

	configSource, configPaths, err := resolveConfigSource(configPath)
	if err != nil {
		log.Fatalf("statok: config resolution failed: %v", err)
	}
	logConfigPaths(configPaths)
	fileCfg, err := loadFileConfig(configSource)
	if err != nil {
		log.Fatalf("statok: config load failed: %v", err)
	}
	runtimeCfg, err := resolveRuntimeConfig(fileCfg, workloadFlag.value, workloadFlag.set)
	if err != nil {
		log.Fatalf("statok: config invalid: %v", err)
	}
	if configSource.Path != "" {
		log.Printf("statok: config loaded from %s", configSource.Path)
	} else {
		log.Printf("statok: no config found, using defaults")
	}
	if err := initClient(runtimeCfg.Workload, endpoint, apiKey, verbose); err != nil {
		log.Fatalf("statok: init failed: %v", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	core := catalog.CoreCollectors()
	probes := catalog.IntegrationProbes()
	if runtimeCfg.MongoEnabled {
		probes = append(probes, mongo.NewProbe(runtimeCfg.MongoInstances, agent.MongoEvery, mongoRetryInterval))
	}
	if runtimeCfg.NginxEnabled {
		probes = append(probes, nginx.NewProbe(runtimeCfg.NginxEndpoint, agent.NginxEvery))
	}
	agent.Run(ctx, core, probes)
	flushAndClose()
}

func initClient(workload string, endpoint string, apiKey string, verbose bool) error {
	if apiKey == "" {
		return errors.New("STATOK_API_KEY is required")
	}
	_, err := statok.Init(workload, statok.Config{
		Endpoint:          endpoint,
		APIKey:            apiKey,
		QueueSize:         64_000,
		MaxBatchSize:      2_000,
		MaxSeriesPerBatch: 5_000,
		FlushInterval:     2 * time.Second,
		LocalAggCounters:  true,
		ValueMode:         statok.ValueAggregationBatch,
		Verbose:           verbose,
	})
	if errors.Is(err, statok.ErrInvalidAPIKey) {
		return fmt.Errorf("invalid STATOK_API_KEY: %w", err)
	}
	if errors.Is(err, statok.ErrMissingAPIKey) {
		return errors.New("STATOK_API_KEY is required")
	}
	return err
}

func flushAndClose() {
	flushCtx, flushCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer flushCancel()
	if client := statok.Default(); client != nil {
		_ = client.Close(flushCtx)
	}
}

func logConfigPaths(paths []string) {
	if len(paths) == 0 {
		log.Printf("statok: config search paths: (none)")
		return
	}
	log.Printf("statok: config search paths: %s", strings.Join(paths, ", "))
}
