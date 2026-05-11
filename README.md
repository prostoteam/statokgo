# Statok Go Client

Lightweight, non-blocking Go library for emitting Statok metrics from any service or job. The client batches and ships
counter, unique, and value events to a Statok keeping the caller fast, safe, and resource-bounded.

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

	"github.com/prostoteam/statokgo"
)

func main() {
	// Build the ingest URL; path is added automatically when missing.
	endpoint := statok.EndpointFromHost("statok-ingester.example.com")
	workload := "payments-api"

	if _, err := statok.Init(workload, statok.Config{
		Endpoint: endpoint,  // creates default HTTP transport
		APIKey:   "your-statok-generated-api-key",
	}); err != nil {
		log.Fatal(err)
	}

	// Non-blocking calls; dropped silently if the queue is full.
	userID := uint32(42)
	statok.Count("requests", 1, "service=api", statok.Label("method", "GET"))
	statok.CountUnique(userID, "daily_active_users", "service=api")
	statok.Total("host.net.kb", 2048, "iface=eth0", "dir=rx")
	statok.Value("latency_ms", 123.4, "service=api", "endpoint=/login")
	statok.ValueSparse("host.fs.capacity_kb", 1024*1024, "mount=/")

	// Flush remaining events before exiting.
	_ = statok.Default().Close(context.Background())
}
```

Use `statok.Count` for counter deltas, `statok.CountUnique` for unique occurrences, `statok.Total` for monotonic
counter totals (first sample is baseline), and `statok.Value` for sampled values. All accept labels either as `"k=v"`
strings or via `statok.Label(k, v)` which sanitizes `=` and control characters.

If `Endpoint` is empty and `Transport` is nil, the client defaults to the public ingest host
`https://statok.dev0101.xyz/api/i/batch`. For HTTP transport, `APIKey` is required and should be the
Statok-generated ingest API key copied exactly as provided. The client sends it as the `Authorization` header value
without parsing or rewriting it.

## Core behaviors

- **Non-blocking hot path**: Count/CountUnique/Total/Value never block or panic. When the bounded queue is full, the event is
  dropped.
- **Bounded resources**: Queue size, batch size, max aggregated series per batch, and total-series cache are fixed
  internally to keep memory bounded without exposing tuning knobs.
- **Background flushing**: A worker goroutine batches events and flushes on size or time. Network I/O never runs in the
  caller goroutine.
- **Safe labels**: Label slices are copied so caller mutations cannot affect in-flight batches.
- **Errors are isolated**: Transport errors are logged (via `Logger`) but never returned to the caller; the worker keeps
  running.
- **Non-retryable auth handling**: By default, HTTP `401 Unauthorized` or API error code `unauthorized` disables further
  ingest attempts in the current client instance.

## Configuration reference

`statok.Config` fields:
Workload is supplied separately to `Init`/`NewClient` and becomes the required `workload` label on every metric.

| Field       | Default                                  | Purpose                                                                                                                          |
|-------------|------------------------------------------|----------------------------------------------------------------------------------------------------------------------------------|
| `Endpoint`  | `https://statok.dev0101.xyz/api/i/batch` | Ingest URL. When set and `Transport` is nil, an `HTTPTransport` is created and `/api/i/batch` is appended if no path is present. |
| `APIKey`    | `""`                                     | Statok-generated ingest API key for HTTP transport. Required when using `HTTPTransport`; sent exactly as provided.              |
| `Transport` | nil                                      | Any implementation of `Transport` (HTTP is provided). Must be safe for concurrent use.                                           |
| `Logger`    | `log.Default()`                          | Receives internal errors and send summaries. Provide your own or silence by using a logger that discards output.                 |
| `Verbose`   | `false`                                  | When true, logs the client version at startup and each flush with per-type counts and metric breakdowns.                         |

Counters with identical metric+labels are summed within each batch. Unique events with the same unique ID and identical
metric+labels may be deduplicated within a flush window. Values are forwarded as raw samples.

## Agent core metrics

Core collectors emit host-level metrics such as `host.uptime_min`, `host.mem.capacity_kb`, `host.net.kb`,
`host.disk.io_ops`, and `host.cpu.usage_pct`. The exact set depends on OS support and enabled collectors.

## Agent integrations

Hostmetrics integrations can add system-adjacent metrics when enabled:

- Docker: `docker.container.*` metrics auto-enabled when `/var/run/docker.sock` is detected.
- Nginx: `nginx.connections` and `nginx.totals` (type=accepts|handled|requests) via `stub_status` (enabled unless
  explicitly disabled; when no endpoint is set, probes `http://127.0.0.1:{80,8080,8081,8888}{/stub_status,/nginx_status}`
  and uses the first reachable endpoint).
- MongoDB: `mongo.*` metrics via `serverStatus` (enabled when instances are configured; `enabled` is optional).

## Lifecycle

- Create a client with `statok.NewClient(workload, cfg)` or set the package-level default with
  `statok.Init(workload, cfg)` and then call
  `statok.Count/CountUnique/Value` helpers.
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
- `CountUnique` accepts non-negative integers that fit `uint32`, strings, and byte slices as unique IDs. String and byte
  IDs are hashed to a stable 32-bit decimal ID before transport; the unique ID is never sent as a label.
- Avoid unbounded label cardinality; prefer coarse keys such as `service`, `host`, `region`, `status`.

## Performance & safety notes

- Caller overhead is a small allocation to clone labels plus a non-blocking channel send; when the queue is saturated,
  the event is dropped immediately.
- Aggregation maps are bounded internally; exceeding the cap falls back to per-event forwarding instead of growing
  unbounded memory.
- `Total` baselines are stored up to an internal cap; additional series are dropped silently.
- Network errors never surface to callers; they are logged and the worker continues with the next flush window.
- HTTP `401` and API response code `unauthorized` are treated as non-retryable; after one such response, the client
  drops new events until reinitialized with updated config/credentials.
