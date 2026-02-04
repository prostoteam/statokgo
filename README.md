# Statok Go Client

Lightweight, non-blocking Go library for emitting Statok metrics from any service or job. The client batches and ships
counter and value events to a Statok keeping the caller fast, safe, and resource-bounded.

## Install

```bash
go get github.com/prostoteam/statokgo@latest
```

## Quick start

```go
package main

import (
	"context"
	"log"
	"time"

	"github.com/prostoteam/statokgo"
)

func main() {
	// Build the ingest URL; path is added automatically when missing.
	endpoint := statok.EndpointFromHost("statok-ingester.example.com")
	workload := "payments-api"

	if _, err := statok.Init(workload, statok.Config{
		Endpoint:         endpoint, // creates default HTTP transport
		FlushInterval:    200 * time.Millisecond,
		MaxBatchSize:     256,
		LocalAggCounters: true, // sum identical counters per batch
		ValueMode:        statok.ValueAggregationAuto,
	}); err != nil {
		log.Fatal(err)
	}

	// Non-blocking calls; dropped silently if the queue is full.
	statok.Count("requests", 1, "service=api", statok.Label("method", "GET"))
	statok.Total("host.net.kb", 2048, "iface=eth0", "dir=rx")
	statok.Value("latency_ms", 123.4, "service=api", "endpoint=/login")
	statok.ValueSparse("host.fs.capacity_kb", 1024*1024, "mount=/")

	// Flush remaining events before exiting.
	_ = statok.Default().Close(context.Background())
}
```

Use `statok.Count` for counter deltas, `statok.Total` for monotonic counter totals (first sample is baseline), and
`statok.Value` for sampled values. All accept labels either as `"k=v"` strings or via `statok.Label(k, v)` which
sanitizes `=` and control characters.

If you call `statok.Init("my-service", statok.Config{})`, the client defaults to the public ingest host
`https://statok.dev0101.xyz/api/i/batch`.

## Core behaviors

- **Non-blocking hot path**: Count/Total/Value never block or panic. When the bounded queue is full, the event is
  dropped.
- **Bounded resources**: Queue size, batch size, max aggregated series per batch, and total-series cache are
  configurable.
- **Background flushing**: A worker goroutine batches events and flushes on size (`MaxBatchSize`) or time (
  `FlushInterval`). Network I/O never runs in the caller goroutine.
- **Safe labels**: Label slices are copied so caller mutations cannot affect in-flight batches.
- **Errors are isolated**: Transport errors are logged (via `Logger`) but never returned to the caller; the worker keeps
  running.

## Configuration reference

`statok.Config` fields (defaults applied when zero-valued):
Workload is supplied separately to `Init`/`NewClient` and becomes the required `workload` label on every metric.

| Field                   | Default                                  | Purpose                                                                                                                          |
|-------------------------|------------------------------------------|----------------------------------------------------------------------------------------------------------------------------------|
| `Endpoint`              | `https://statok.dev0101.xyz/api/i/batch` | Ingest URL. When set and `Transport` is nil, an `HTTPTransport` is created and `/api/i/batch` is appended if no path is present. |
| `Transport`             | nil                                      | Any implementation of `Transport` (HTTP is provided). Must be safe for concurrent use.                                           |
| `Logger`                | `log.Default()`                          | Receives internal errors and send summaries. Provide your own or silence by using a logger that discards output.                 |
| `Verbose`               | `false`                                  | When true, logs the client version at startup and each flush with per-type counts and metric breakdowns.                         |
| `QueueSize`             | 64_000                                   | Bounded channel depth; excess events are dropped.                                                                                |
| `MaxBatchSize`          | 512                                      | Flush when this many events are collected. Also capped by `QueueSize`.                                                           |
| `MaxSeriesPerBatch`     | 2_048                                    | Limits distinct series retained in aggregation maps per batch. Beyond this, events are forwarded without further aggregation.    |
| `MaxTotalSeries`        | 2_048                                    | Caps the number of distinct series tracked for `Total` baselines; new series are dropped when the cap is reached.                |
| `FlushInterval`         | 500ms                                    | Periodic flush cadence.                                                                                                          |
| `FlushTimeout`          | 5s                                       | Context timeout applied to each transport send.                                                                                  |
| `LocalAggCounters`      | false                                    | When true, sums counter events with identical metric+labels within the batch.                                                    |
| `ValueMode`             | `ValueAggregationNone`                   | Aggregation mode for values (see below).                                                                                         |
| `ValueAggAutoThreshold` | 4                                        | Used by `ValueAggregationAuto`; number of raw samples to forward before switching to averaging.                                  |

## Value aggregation modes

- `ValueAggregationNone` (default): forward every call as-is.
- `ValueAggregationBatch`: average values per metric+label within a batch; emits one value per series per flush.
- `ValueAggregationAuto`: forward raw samples until `ValueAggAutoThreshold` per series in the window, then emit a single
  average for that series. Balances fidelity for sparse series with compression for noisy ones.

Counters can be aggregated independently via `LocalAggCounters` (sum within the batch, keeping the latest timestamp).

## Agent core metrics

Core collectors emit host-level metrics such as `host.uptime_min`, `host.mem.capacity_kb`, `host.net.kb`,
`host.disk.io_ops`, and `host.cpu.usage_pct`. The exact set depends on OS support and enabled collectors.

## Agent integrations

Hostmetrics integrations can add system-adjacent metrics when enabled:

- Docker: `docker.container.*` metrics auto-enabled when `/var/run/docker.sock` is detected.
- Nginx: `nginx.connections`, `nginx.requests`, `nginx.accepts`, `nginx.handled` via `stub_status` (default endpoint
  `http://127.0.0.1/stub_status`, configurable).
- MongoDB: `mongo.*` metrics via `serverStatus` (config-driven).

## Lifecycle

- Create a client with `statok.NewClient(workload, cfg)` or set the package-level default with
  `statok.Init(workload, cfg)` and then call
  `statok.Count/Value` helpers.
- Call `client.Close(ctx)` during shutdown to flush the queue. Close drains without blocking the caller goroutine; it
  honors the provided context for the final send.
- Inspect `client.Dropped()` to see how many events were rejected because the queue was full (not exposed to callers
  otherwise).

## Labels and cardinality

- Labels may be provided as `"k=v"` strings or built with `statok.Label(k, v)`, which replaces `=`, `|`, and newlines
  with `_` to keep the line protocol well-formed.
- The `workload` label is injected automatically from the workload argument passed to `Init`/`NewClient` as the first
  label on every metric; do not pass your own `workload=...`
  label (events are dropped if you do).
- Avoid unbounded label cardinality; prefer coarse keys such as `service`, `host`, `region`, `status`.

## Performance & safety notes

- Caller overhead is a small allocation to clone labels plus a non-blocking channel send; when the queue is saturated,
  the event is dropped immediately.
- Aggregation maps are bounded by `MaxSeriesPerBatch`; exceeding the cap falls back to per-event forwarding instead of
  growing unbounded memory.
- `Total` baselines are stored up to `MaxTotalSeries`; additional series are dropped silently.
- Network errors never surface to callers; they are logged and the worker continues with the next flush window.
