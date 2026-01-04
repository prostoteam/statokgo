#!/usr/bin/env bash
# Install/update statok hostmetrics agent and run it in background.
#
# Design goals (simplified, deterministic, minimal host impact):
# - Uses system Go if >= GO_MIN_VERSION.
# - Otherwise downloads a fixed temporary Go toolchain from dl.google.com, uses it to build, and deletes it.
# - NO apt-get fallback (no package install/remove; avoids modifying host package state).
# - Idempotent runtime: restarts only the agent started by this script (PID file), avoids duplicates.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/prostoteam/statokgo/main/scripts/install_agent.sh | bash -s -- --workload firstvds-proxy
#
# Optional env overrides:
#   GO_MIN_VERSION=1.21.0
#   GO_BOOTSTRAP_VERSION=1.22.5
#   BIN_NAME=statok-agent
#   INSTALL_DIR=$HOME/.local/bin
#   STATOK_HOST_DEFAULT=statok.dev0101.xyz
#   STATOK_VERSION=latest
#   GOFLAGS="-buildvcs=false"

set -euo pipefail
IFS=$'\n\t'

# Install/runtime defaults
BIN_NAME="${BIN_NAME:-statok-agent}"
INSTALL_DIR="${INSTALL_DIR:-$HOME/.local/bin}"
BIN_PATH="$INSTALL_DIR/$BIN_NAME"

STATOK_HOST_DEFAULT="${STATOK_HOST_DEFAULT:-statok.dev0101.xyz}"
PID_FILE="${PID_FILE:-$HOME/.statok-agent.pid}"
LOG_FILE="${LOG_FILE:-$HOME/.statok-agent.log}"

# Build defaults
GO_MIN_VERSION="${GO_MIN_VERSION:-1.21.0}"
# Fixed bootstrap Go version to download if system Go is too old.
# Pick any known-good >= GO_MIN_VERSION.
GO_BOOTSTRAP_VERSION="${GO_BOOTSTRAP_VERSION:-1.22.5}"

STATOK_VERSION="${STATOK_VERSION:-latest}"
GOFLAGS="${GOFLAGS:--buildvcs=false}"

MODULE_PATH="github.com/prostoteam/statokgo/cmd/statok-hostmetrics"
DEFAULT_BIN_NAME="statok-hostmetrics"

# Only supported option
WORKLOAD="${WORKLOAD:-}"

err() { echo "statok-install: $*" >&2; exit 1; }
have_cmd() { command -v "$1" >/dev/null 2>&1; }
need_cmd() { have_cmd "$1" || err "missing required command: $1"; }

usage() {
  cat <<EOF
Usage:
  $(basename "$0") [--workload <value>]

Options:
  -w, --workload   Optional workload label passed to agent at runtime
  -h, --help       Show this help
EOF
}

trim_ws() { printf '%s' "$1" | tr -d '\r' | sed 's/^[[:space:]]*//; s/[[:space:]]*$//'; }

download() {
  local url="$1" out="$2"
  url="$(trim_ws "$url")"
  [ -n "$url" ] || err "download called with empty URL"

  if have_cmd curl; then
    curl -fL --retry 3 --retry-delay 1 --connect-timeout 10 -sS "$url" -o "$out" || return 1
  elif have_cmd wget; then
    wget --tries=3 --timeout=10 -qO "$out" "$url" || return 1
  else
    err "need curl or wget"
  fi
}

version_ge() {
  # true if $1 >= $2 (semver-ish using sort -V)
  [ "$(printf '%s\n' "$2" "$1" | sort -V | head -n1)" = "$2" ]
}

# Verify downloaded file looks like gzip (tar.gz). If not, likely proxied/HTML.
is_gzip_file() {
  local f="$1"
  local magic
  magic="$(head -c 2 "$f" 2>/dev/null | od -An -tx1 | tr -d ' \n')"
  [ "$magic" = "1f8b" ]
}

ensure_linux_arch() {
  [ "$(uname -s)" = "Linux" ] || err "unsupported OS (Linux only)"
  case "$(uname -m)" in
    x86_64|amd64) echo "amd64" ;;
    aarch64|arm64) echo "arm64" ;;
    *) err "unsupported arch (amd64/arm64 only)" ;;
  esac
}

ensure_install_dir() {
  mkdir -p "$INSTALL_DIR"
  [ -d "$INSTALL_DIR" ] || err "install dir is not a directory: $INSTALL_DIR"
  [ -w "$INSTALL_DIR" ] || err "install dir is not writable: $INSTALL_DIR"
}

use_system_go_if_ok() {
  have_cmd go || return 1
  local gv
  gv="$(go version 2>/dev/null | awk '{print $3}' | sed 's/^go//')"
  [ -n "${gv:-}" ] || return 1
  if version_ge "$gv" "$GO_MIN_VERSION"; then
    return 0
  fi
  echo "statok-install: system Go $gv found, but $GO_MIN_VERSION+ is required; will use a temporary Go toolchain."
  return 1
}

setup_temp_go_or_fail() {
  need_cmd tar

  local arch fname url tgz
  arch="$(ensure_linux_arch)"

  fname="go${GO_BOOTSTRAP_VERSION}.linux-${arch}.tar.gz"
  url="https://dl.google.com/go/${fname}"
  tgz="$TMPDIR/go.tgz"

  echo "statok-install: downloading temporary Go toolchain: ${fname}"
  download "$url" "$tgz" || err "failed to download ${url}"

  if ! is_gzip_file "$tgz"; then
    echo "statok-install: downloaded content is not a tar.gz (likely blocked/proxied). First line:"
    head -n 1 "$tgz" | sed 's/^/  /'
    err "cannot obtain a valid Go toolchain"
  fi

  rm -rf "$TMPDIR/go"
  tar -C "$TMPDIR" -xzf "$tgz"
  [ -x "$TMPDIR/go/bin/go" ] || err "temporary Go install failed: $TMPDIR/go/bin/go not found"

  export GOROOT="$TMPDIR/go"
  export PATH="$GOROOT/bin:$PATH"

  go version >/dev/null 2>&1 || err "temporary Go toolchain is not runnable"

  # Ensure minimum satisfied (defensive)
  local gv
  gv="$(go version 2>/dev/null | awk '{print $3}' | sed 's/^go//')"
  if ! version_ge "$gv" "$GO_MIN_VERSION"; then
    err "temporary Go $gv is still < $GO_MIN_VERSION; set GO_BOOTSTRAP_VERSION accordingly"
  fi
}

install_agent() {
  local gobin_tmp final_tmp

  gobin_tmp="$TMPDIR/gobin"
  mkdir -p "$gobin_tmp"

  # Keep Go caches isolated (no user pollution).
  export GOPATH="$TMPDIR/gopath"
  export GOMODCACHE="$TMPDIR/gomodcache"
  export GOCACHE="$TMPDIR/gocache"
  mkdir -p "$GOPATH" "$GOMODCACHE" "$GOCACHE"

  echo "statok-install: installing ${MODULE_PATH}@${STATOK_VERSION}"
  GOBIN="$gobin_tmp" GOFLAGS="$GOFLAGS" go install "${MODULE_PATH}@${STATOK_VERSION}"

  final_tmp="$gobin_tmp/$DEFAULT_BIN_NAME"
  [ -f "$final_tmp" ] || err "expected built binary not found: $final_tmp"

  if have_cmd install; then
    install -m 0755 -T "$final_tmp" "$BIN_PATH"
  else
    cp -f "$final_tmp" "$BIN_PATH"
    chmod 0755 "$BIN_PATH"
  fi

  echo "statok-install: installed: $BIN_PATH"
}

pid_is_ours_and_running() {
  [ -f "$PID_FILE" ] || return 1
  local pid
  pid="$(cat "$PID_FILE" 2>/dev/null || true)"
  [[ "$pid" =~ ^[0-9]+$ ]] || return 1
  kill -0 "$pid" 2>/dev/null || return 1
  if [ -r "/proc/$pid/cmdline" ]; then
    tr '\0' ' ' <"/proc/$pid/cmdline" | grep -Fq "$BIN_PATH" || return 1
  fi
  return 0
}

stop_agent_if_running() {
  if ! pid_is_ours_and_running; then
    return 0
  fi

  local pid
  pid="$(cat "$PID_FILE")"
  echo "statok-install: stopping existing agent (pid $pid)"
  kill "$pid" 2>/dev/null || true

  for _ in 1 2 3 4 5; do
    if ! kill -0 "$pid" 2>/dev/null; then
      rm -f "$PID_FILE"
      echo "statok-install: stopped"
      return 0
    fi
    sleep 1
  done

  echo "statok-install: agent did not stop gracefully; sending SIGKILL"
  kill -9 "$pid" 2>/dev/null || true
  rm -f "$PID_FILE"
}

start_agent() {
  touch "$LOG_FILE" 2>/dev/null || true

  local args=()
  if [ -n "${WORKLOAD:-}" ]; then
    args+=( "--workload" "$WORKLOAD" )
  fi

  echo "statok-install: starting agent in background (host $STATOK_HOST_DEFAULT)"
  STATOK_HOST="$STATOK_HOST_DEFAULT" setsid "$BIN_PATH" "${args[@]}" >>"$LOG_FILE" 2>&1 < /dev/null &
  echo $! > "$PID_FILE"

  echo "statok-install: agent started (pid $(cat "$PID_FILE"))"
  echo "statok-install: logs: $LOG_FILE"
}

parse_args() {
  while [ $# -gt 0 ]; do
    case "$1" in
      -w|--workload)
        [ $# -ge 2 ] || err "missing value for $1"
        WORKLOAD="$2"
        shift 2
        ;;
      -h|--help)
        usage; exit 0
        ;;
      *)
        err "unknown argument: $1 (use --help)"
        ;;
    esac
  done
}

main() {
  need_cmd uname
  need_cmd awk
  need_cmd sed
  need_cmd sort
  need_cmd head
  need_cmd mktemp
  need_cmd tar

  parse_args "$@"
  ensure_install_dir

  TMPDIR="$(mktemp -d -t statok-install.XXXXXX)"
  export TMPDIR

  # Always cleanup temp dir.
  trap 'rm -rf "$TMPDIR"' EXIT INT TERM

  if ! use_system_go_if_ok; then
    setup_temp_go_or_fail
  fi

  install_agent
  stop_agent_if_running
  start_agent

  cat <<EOF
statok-install: done.

Binary:
  $BIN_PATH

Default host:
  $STATOK_HOST_DEFAULT

PID file:
  $PID_FILE

To stop:
  if [ -f "$PID_FILE" ]; then kill \$(cat "$PID_FILE"); rm -f "$PID_FILE"; fi
EOF
}

main "$@"
