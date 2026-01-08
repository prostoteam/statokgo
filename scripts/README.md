# Deploying statok-agent on Ubuntu

Use `./update` to git pull, build, and run `statok-agent` in the foreground (no systemd/nohup). Run it from inside the repo checkout; Ctrl+C to stop. Default endpoint is `https://statok.dev0101.xyz` (override with `STATOK_ENDPOINT=...`).

## One-line installer

```bash
curl -fsSL https://raw.githubusercontent.com/prostoteam/statokgo/main/scripts/install_agent.sh | bash -s -- --workload my-workload --verbose
```

Override defaults with env vars if needed:

```bash
GO_VERSION=1.25.4 ./install_agent.sh
```

Quick use:

```bash
cd /path/to/statok-agent
./update
```

Defaults:
- App dir: current directory (`APP_DIR`)
- Main package: `./cmd/statok-hostmetrics` (`MAIN_PKG`)
- Output binary: `./statok-agent` (`OUTPUT`)
- Go flags: `-buildvcs=false` (`GOFLAGS`)

Override via env vars if needed:

```bash
MAIN_PKG=./cmd/your-main OUTPUT=./statok-agent ./update
```
