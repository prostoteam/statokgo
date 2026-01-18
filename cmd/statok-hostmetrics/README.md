# Hostmetrics agent

Collection cadence:

- Every 10s: CPU, memory, swap, network, disk I/O
- Every 60s: filesystem usage + inode counts

Metrics emitted by the agent (count metrics are deltas over the collection interval).
All metrics include the `workload` label as the first label; the table lists additional labels.

| Metric                           | Kind  | Unit    | Labels                                                                 |
|----------------------------------|-------|---------|------------------------------------------------------------------------|
| `host.cpu.usage_pct`             | value | percent | `cpu`, `mode` (user,nice,system,idle,iowait,irq,softirq,steal) |
| `host.mem.capacity_kb`           | value | KB      | `type` (total,used,free,available)                             |
| `host.swap.capacity_kb`          | value | KB      | `type` (total,used,free)                                       |
| `host.fs.capacity_kb`            | value | KB      | `mount`, `device`, `type` (total,used,free)                    |
| `host.fs.inodes_count`           | value | count   | `mount`, `device`, `type` (total,used,free)                    |
| `host.disk.io_kb`                | count | kb      | `device`, `dir` (read,write)                                   |
| `host.disk.io_ops`               | count | ops     | `device`, `dir` (read,write)                                   |
| `host.disk.io_time_ms`           | count | ms      | `device`                                                       |
| `host.net.kb`                    | count | kb      | `iface`, `dir` (rx,tx)                                         |
| `host.net.packets`               | count | packets | `iface`, `dir` (rx,tx)                                         |
| `host.net.errors`                | count | errors  | `iface`, `dir` (rx,tx)                                         |
| `host.net.dropped`               | count | packets | `iface`, `dir` (rx,tx)                                         |
| `docker.container.cpu.usage_pct` | value | percent | `service`                                                      |
| `docker.container.mem.usage_kb`  | value | kb      | `service`                                                      |

Docker metrics are enabled automatically when a local Docker socket is detected at `/var/run/docker.sock`. The label
mode is currently hardcoded to `service` (compose service / swarm service / fallback container name).

Agent flags:

- `--workload` / `-w`: set the workload label (defaults to hostname; empty value is an error).
- `--verbose` / `-v`: enable verbose client logging.

Environment overrides:

- `STATOK_ENDPOINT`: full ingest URL (highest priority).
- `STATOK_HOST`: host or URL used to build the ingest endpoint.
