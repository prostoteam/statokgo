# Hostmetrics agent

Collection cadence:

- Every 10s: CPU, memory, swap, network, disk I/O
- Every 60s: filesystem usage, inode counts, uptime

Metrics emitted by the agent (count metrics are deltas over the collection interval).
All metrics include the `workload` label as the first label; the table lists additional labels.

| Metric                           | Kind  | Unit    | Labels                                                          |
|----------------------------------|-------|---------|-----------------------------------------------------------------|
| `host.cpu.usage_pct`             | value | percent | `cpu`, `mode` (user,nice,system,idle,iowait,irq,softirq,steal)  |
| `host.mem.capacity_kb`           | value | KB      | `type` (total,used,free,available)                              |
| `host.swap.capacity_kb`          | value | KB      | `type` (total,used,free)                                        |
| `host.uptime_min`                | value | min     |                                                                 |
| `host.fs.capacity_kb`            | value | KB      | `mount`, `device`, `type` (total,used,free)                     |
| `host.fs.inodes_count`           | value | count   | `mount`, `device`, `type` (total,used,free)                     |
| `host.disk.io_kb`                | count | kb      | `device`, `dir` (read,write)                                    |
| `host.disk.io_ops`               | count | ops     | `device`, `dir` (read,write)                                    |
| `host.disk.io_time_ms`           | count | ms      | `device`                                                        |
| `host.net.kb`                    | count | kb      | `iface`, `dir` (rx,tx)                                          |
| `host.net.packets`               | count | packets | `iface`, `dir` (rx,tx)                                          |
| `host.net.errors`                | count | errors  | `iface`, `dir` (rx,tx)                                          |
| `host.net.dropped`               | count | packets | `iface`, `dir` (rx,tx)                                          |
| `docker.container.cpu.usage_pct` | value | percent | `service`                                                       |
| `docker.container.mem.usage_kb`  | value | kb      | `service`                                                       |
| `docker.container.net.kb`        | count | kb      | `service`, `dir` (rx,tx)                                        |
| `docker.container.restart_count` | count | count   | `service`                                                       |
| `nginx.connections`              | value | count   | `state` (active,reading,writing,waiting)                        |
| `nginx.totals`                   | count | count   | `type` (accepts,handled,requests)                               |
| `mongo.connections`              | value | count   | `instance`, `type` (current,available)                          |
| `mongo.mem.resident_mb`          | value | mb      | `instance`                                                      |
| `mongo.wt.cache.kb`              | value | kb      | `instance`, `type` (used,max)                                   |
| `mongo.wt.cache.evictions_count` | count | count   | `instance`                                                      |
| `mongo.ops_count`                | count | ops     | `instance`, `type` (insert,query,update,delete,getmore,command) |
| `mongo.op_latency_ms`            | value | ms      | `instance`, `type` (reads,writes,commands)                      |

Docker metrics are enabled automatically when a local Docker socket is detected at `/var/run/docker.sock`. The label
mode is currently hardcoded to `service` (compose service / swarm service / fallback container name).

Nginx metrics require a reachable `stub_status` endpoint and are enabled by default unless explicitly disabled. When
no endpoint is configured, the agent auto-probes `http://127.0.0.1:{80,8080,8081,8888}{/stub_status,/nginx_status}` and
uses the first reachable match.

Mongo integration is enabled when instances are configured; the `enabled` flag is optional. Set `enabled: false` to
disable it even when instances are present.

Agent flags:

- `--workload` / `-w`: set the workload label (defaults to hostname; empty value is an error).
- `--config` / `-c`: path to the YAML config file (optional).
- `--verbose` / `-v`: enable verbose client logging.

Environment overrides:

- `STATOK_API_KEY`: required API token for ingest auth.
- `STATOK_ENDPOINT`: full ingest URL (highest priority).
- `STATOK_HOST`: host or URL used to build the ingest endpoint.
- Endpoint env vars are evaluated outside the config and always apply.

Config file (optional):

- System: `/etc/statok/hostmetrics.yaml`
- User: `$XDG_CONFIG_HOME/statok/hostmetrics.yaml` (fallback: `~/.config/statok/hostmetrics.yaml`)
- Override path via `--config` / `-c` or `STATOK_CONFIG`.
- When running as root, the system path is checked before the user path; otherwise user path is preferred.
- `${VAR}` expansion is supported for all string fields.
- `env_files` can provide `${VAR}` values from simple `KEY=VALUE` files (later files override earlier ones; process env
  is used as a fallback). Missing files are ignored. Optional `export ` prefix is supported. Values may be wrapped in
  single or double quotes (quotes are stripped).
- Config values override flags for overlapping fields (e.g., `agent.workload`); an empty workload is an error.

Example:

```yaml
agent:
  workload: "my-workload"
env_files:
  - "/etc/statok/agent.env"

integrations:
  nginx:
    enabled: true
    endpoint: "http://127.0.0.1/stub_status"
  mongo:
    instances:
      - uri: "mongodb://monitor:${MONGO_PASSWORD}@localhost:27017/admin"
```

Mongo note: `serverStatus` runs against the `admin` database and requires appropriate permissions for the configured
user.
