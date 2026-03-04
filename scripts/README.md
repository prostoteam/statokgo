# Deploying statok-agent on Ubuntu

Use the installer to set up a systemd service that starts on boot. Default endpoint is `https://statok.dev0101.xyz`
(override with `STATOK_ENDPOINT=...` or `STATOK_HOST=...`).

## One-line installer (system service)

```bash
curl -fsSL https://raw.githubusercontent.com/prostoteam/statokgo/main/scripts/install_agent.sh | sudo bash -s -- --workload my-workload --verbose
```

The installer will prompt for `STATOK_API_KEY` (hidden input) and save it to `/etc/statok/agent.env` with mode `600`.
The systemd unit reads it via `EnvironmentFile`, so the key is not passed via command-line args.

Non-interactive install is also supported by passing `STATOK_API_KEY` in the environment:

```bash
curl -fsSL https://raw.githubusercontent.com/prostoteam/statokgo/main/scripts/install_agent.sh -o /tmp/install_agent.sh
sudo STATOK_API_KEY='123_xxx' bash /tmp/install_agent.sh --workload my-workload --verbose
```

Override defaults with env vars if needed:

```bash
GO_BOOTSTRAP_VERSION=1.22.5 ./install_agent.sh
```

Check status:

```bash
sudo systemctl status statok-agent
```

Tail logs:

```bash
sudo journalctl -u statok-agent -f
```

User service (no sudo):

```bash
SYSTEMD_SCOPE=user ./install_agent.sh --workload my-workload --verbose
systemctl --user status statok-agent
```

Tail logs (user service):

```bash
journalctl --user -u statok-agent -f
```

Note: user services start on boot only if lingering is enabled (`loginctl enable-linger $USER`).

If `--workload` is omitted, the agent defaults to the system hostname; passing an empty workload value is an error.

Defaults:
- Service name: `statok-agent` (`SERVICE_NAME`)
- Install dir: `/usr/local/bin` (`INSTALL_DIR`)
- Go flags: `-buildvcs=false` (`GOFLAGS`)
- API key env file: `/etc/statok/agent.env` for system scope, `~/.config/statok/agent.env` for user scope (`AGENT_ENV_FILE` / `--api-key-file`)

Override via env vars if needed:

```bash
SERVICE_NAME=statok-agent STATOK_HOST_DEFAULT=statok.dev0101.xyz ./install_agent.sh
```

If `SERVICE_NAME` is customized, use that name in `systemctl`/`journalctl` commands instead of `statok-agent`.

## Foreground (debug)

Use `./update` to git pull, build, and run `statok-agent` in the foreground (no systemd/nohup).
Run it from inside the repo checkout; Ctrl+C to stop.
