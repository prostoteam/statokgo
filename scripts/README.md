# Deploying statok-agent on Ubuntu

Use the installer to set up a systemd service that starts on boot. Default endpoint is `https://statok.dev0101.xyz`
(override with `STATOK_ENDPOINT=...` or `STATOK_HOST=...`).

## One-line installer (system service)

```bash
curl -fsSL https://raw.githubusercontent.com/prostoteam/statokgo/main/scripts/install_agent.sh | sudo bash -s -- --workload my-workload --verbose
```

Override defaults with env vars if needed:

```bash
GO_BOOTSTRAP_VERSION=1.22.5 ./install_agent.sh
```

Check status:

```bash
sudo systemctl status statok-agent
```

User service (no sudo):

```bash
SYSTEMD_SCOPE=user ./install_agent.sh --workload my-workload --verbose
systemctl --user status statok-agent
```

Note: user services start on boot only if lingering is enabled (`loginctl enable-linger $USER`).

Defaults:
- Service name: `statok-agent` (`SERVICE_NAME`)
- Install dir: `/usr/local/bin` (`INSTALL_DIR`)
- Go flags: `-buildvcs=false` (`GOFLAGS`)

Override via env vars if needed:

```bash
SERVICE_NAME=statok-agent STATOK_HOST_DEFAULT=statok.dev0101.xyz ./install_agent.sh
```

## Foreground (debug)

Use `./update` to git pull, build, and run `statok-agent` in the foreground (no systemd/nohup).
Run it from inside the repo checkout; Ctrl+C to stop.
