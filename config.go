package statok

import (
	"log"
	"net/url"
	"strings"
	"time"
)

const (
	defaultQueueSize             = 64 * 1024
	defaultMaxBatchSize          = 512
	defaultMaxSeriesPerBatch     = 2048
	defaultFlushInterval         = 500 * time.Millisecond
	defaultFlushTimeout          = 5 * time.Second
	defaultIngestPath            = "/api/i/batch"
	defaultValueAggAutoThreshold = 4
)

// ValueAggregationMode describes how value metrics are handled inside the client.
type ValueAggregationMode uint8

const (
	// ValueAggregationNone forwards every Value call as-is.
	ValueAggregationNone ValueAggregationMode = iota
	// ValueAggregationBatch averages values per metric/label within a flushed batch
	// so the ingester receives one representative sample per unique series.
	ValueAggregationBatch
	// ValueAggregationAuto forwards raw values until the per-series sample count
	// exceeds ValueAggAutoThreshold within a flush window, then switches to
	// averaged emission for that series.
	ValueAggregationAuto
)

// Config tunes client resource usage and behavior. All limits are best-effort; when
// the process is overloaded new events are dropped instead of blocking callers.
type Config struct {
	Endpoint          string
	Transport         Transport
	Logger            Logger
	Verbose           bool
	QueueSize         int
	MaxBatchSize      int
	MaxSeriesPerBatch int
	FlushInterval     time.Duration
	FlushTimeout      time.Duration
	LocalAggCounters  bool
	ValueMode         ValueAggregationMode
	// ValueAggAutoThreshold controls when ValueAggregationAuto switches a series
	// from raw forwarding to averaged emission within a flush window.
	ValueAggAutoThreshold int
}

// Logger is the minimal logging interface used by the library. The default logger
// writes to stderr using log.Printf semantics.
type Logger interface {
	Printf(format string, args ...any)
}

type noopLogger struct{}

func (noopLogger) Printf(string, ...any) {}

func (c *Config) applyDefaults() {
	if c.QueueSize <= 0 {
		c.QueueSize = defaultQueueSize
	}
	if c.MaxBatchSize <= 0 {
		c.MaxBatchSize = defaultMaxBatchSize
	}
	if c.MaxSeriesPerBatch <= 0 {
		c.MaxSeriesPerBatch = defaultMaxSeriesPerBatch
	}
	if c.FlushInterval <= 0 {
		c.FlushInterval = defaultFlushInterval
	}
	if c.FlushTimeout <= 0 {
		c.FlushTimeout = defaultFlushTimeout
	}
	if c.Endpoint != "" {
		c.Endpoint = ensureIngestPath(c.Endpoint)
	}
	if c.Logger == nil {
		c.Logger = log.Default()
	}
	if c.Transport == nil && c.Endpoint != "" {
		c.Transport = &HTTPTransport{
			Endpoint: c.Endpoint,
			Logger:   c.Logger,
		}
	}
	if ht, ok := c.Transport.(*HTTPTransport); ok && ht.Logger == nil {
		ht.Logger = c.Logger
	}
	if c.Logger == nil {
		c.Logger = noopLogger{}
	}
	if c.ValueAggAutoThreshold <= 0 {
		c.ValueAggAutoThreshold = defaultValueAggAutoThreshold
	}
}

// EndpointFromHost builds the ingest endpoint from a host (with or without scheme)
// and appends the default ingest path when missing.
func EndpointFromHost(host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return ""
	}
	if strings.HasPrefix(host, "http://") || strings.HasPrefix(host, "https://") {
		return ensureIngestPath(host)
	}
	return ensureIngestPath("https://" + host)
}

// ensureIngestPath appends the ingest path when the provided endpoint has no path.
// On parse errors, the original string is returned unchanged.
func ensureIngestPath(endpoint string) string {
	if endpoint == "" {
		return ""
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return endpoint
	}
	if u.Path == "" || u.Path == "/" {
		u.Path = defaultIngestPath
	}
	return u.String()
}
