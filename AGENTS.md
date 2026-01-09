## Client Library Requirements

A lightweight, non-blocking client library for emitting metric events (`Count` and `Value`) from applications or agents.  
The client must prioritize safety, bounded resource usage, and efficient transport to the Statok ingestion service.

### Functional Requirements

- Provide simple call interfaces:
  - `statok.Count(name string, value float64, labels ...string)`
  - `statok.Value(name string, value float64, labels ...string)`
- Accept label pairs or `"k=v"` string format without imposing strict structure.
- Emit metric events to a background worker for batching and network delivery.
- Support transport batching based on:
  - Maximum batch size
  - Periodic flush interval
- Support optional local aggregation:
  - **Counters**: aggregate identical (metric + labels) events within a batch.
  - **Values**: allow raw events or optional DDSketch-based local aggregation.

### Safety & Performance Requirements

- Calls to `Count` / `Value` must:
  - Never block.
  - Never panic.
  - Never cause unbounded memory growth.
- Event ingestion queue must be bounded in size.
- When the queue is full:
  - Events must be dropped silently.
  - Internal dropped-event statistics may be tracked but never exposed to callers.
- Minimal per-call allocations; internal pooling should be used where practical.
- All network operations must occur in background goroutines.

### Resource Constraints

- Memory usage must be explicitly bounded by:
  - Configurable queue size.
  - Maximum batch size.
  - Maximum number of unique aggregated series per batch.
- CPU overhead in the hot path must be minimal and constant-time.
- Label slices passed by users must be copied to avoid external mutation.

### Reliability Requirements

- Failure to send events (network errors, server issues) must not affect callers.
- Background worker must recover gracefully from errors and continue operating.
- Optional local DDSketch aggregation must use the same encoding parameters as the Statok collector to allow lossless merging.

### Extensibility Requirements

- Ability to expand supported transports (HTTP, gRPC, etc.) without changing the public API.
- Ability to enable or disable local aggregation modes via configuration.
- Internal behavior (queue size, flush interval, batch size) must be user-configurable at initialization.

### Instruction for CODEX
- When I send you `cm`, generate a great commit message formatted as `scope[, scope...]: action-verb sentence that states what changed and why, covering overall task context rather than tiny fixes`. Add details, like initial problem (if any), root cause, implementation details.
- Don't write tests unless you're asked to.
- Don't compile anything and dont run tests unless you're asked to.
- When adding new metrics - dont forget to add it to README.md
- Max value for ingester is 429496729, so try to fit in this (e.g. use Kb instead of bytes where possible, or ms instead nanosec).
- Always bump `const VersionString = "..."` in `./version.go`.