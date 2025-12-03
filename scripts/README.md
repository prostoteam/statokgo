# Deploying statok-agent on Ubuntu

Use `./update` to git pull, build, and run `statok-agent` in the foreground (no systemd/nohup). Run it from inside the repo checkout; Ctrl+C to stop. Default endpoint is `https://statok.dev0101.xyz` (override with `STATOK_ENDPOINT=...`).

Quick use:

```bash
cd /path/to/statok-agent
./update
```

Defaults:
- App dir: current directory (`APP_DIR`)
- Main package: `./cmd/statok-hostmetrics` (`MAIN_PKG`)
- Output binary: `./statok-agent` (`OUTPUT`)
- Host: `statok.dev0101.xyz` (`STATOK_HOST`)
- Go flags: `-buildvcs=false` (`GOFLAGS`)

Override via env vars if needed:

```bash
STATOK_HOST=collector.example.com MAIN_PKG=./cmd/your-main OUTPUT=./statok-agent ./update
```
