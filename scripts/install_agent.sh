#!/usr/bin/env bash
# statok agent installer + runner
#
# - Builds agent (temporary Go if needed)
# - Installs to ~/.local/bin/statok-agent
# - Runs agent in background with default host
# - Idempotent: will not spawn duplicate processes

set -euo pipefail
IFS=$'\n\t'

BIN_NAME="statok-agent"
INSTALL_DIR="$HOME/.local/bin"
BIN_PATH="$INSTALL_DIR/$BIN_NAME"
PID_FILE="$HOME/.statok-agent.pid"
LOG_FILE="$HOME/.statok-agent.log"

STATOK_HOST="statok.dev0101.xyz"
WORKLOAD=""

GO_MIN_VERSION="1.21.0"
STATOK_VERSION="latest"
GOFLAGS="-buildvcs=false"

MODULE_PATH="github.com/prostoteam/statokgo/cmd/statok-hostmetrics"
DEFAULT_BIN_NAME="statok-hostmetrics"

err() { echo "statok-install: $*" >&2; exit 1; }
have_cmd() { command -v "$1" >/dev/null 2>&1; }

usage() {
  cat <<EOF
Usage:
  install_agent.sh [--workload <value>]

Options:
  -w, --workload   Optional workload label passed to agent
  -h, --help       Show this help
EOF
}

version_ge() {
  [ "$(printf '%s\n' "$2" "$1" | sort -V | head -n1)" = "$2" ]
}

ensure_linux_arch() {
  local arch
  case "$(uname -m)" in
    x86_64|amd64) arch="amd64" ;;
    aarch64|arm64) arch="arm64" ;;
    *) err "unsupported architecture" ;;
  esac
  echo "$arch"
}

download() {
  curl -fsSL "$1" -o "$2" || err "download failed: $1"
}

use_system_go() {
  have_cmd go || return 1
  local gv
  gv="$(go version | awk '{print $3}' | sed 's/^go//')"
  version_ge "$gv" "$GO_MIN_VERSION"
}

setup_temp_go() {
  local arch json filename sha url tgz got

  arch="$(ensure_linux_arch)"
  json="$TMPDIR/go.json"
  tgz="$TMPDIR/go.tgz"

  download "https://go.dev/dl/?mode=json" "$json"

  read filename sha < <(
    awk -v a="$arch" '
      /"filename":/ && /linux-'$arch'\.tar\.gz/ {
        match($0,/go[^"]+/,f)
        getline
        match($0,/"sha256":"([^"]+)"/,s)
        print f[0], s[1]
        exit
      }' "$json"
  )

  [ -n "$filename" ] || err "failed to locate Go tarball"

  url="https://dl.google.com/go/$filename"
  download "$url" "$tgz"

  got="$(sha256sum "$tgz" | awk '{print $1}')"
  [ "$got" = "$sha" ] || err "Go checksum mismatch"

  tar -C "$TMPDIR" -xzf "$tgz"
  export GOROOT="$TMPDIR/go"
  export PATH="$GOROOT/bin:$PATH"
}

install_agent() {
  mkdir -p "$INSTALL_DIR"

  export GOPATH="$TMPDIR/gopath"
  export GOMODCACHE="$TMPDIR/gomodcache"
  export GOCACHE="$TMPDIR/gocache"

  GOBIN="$TMPDIR/bin" GOFLAGS="$GOFLAGS" go install \
    "$MODULE_PATH@$STATOK_VERSION"

  install -m 0755 "$TMPDIR/bin/$DEFAULT_BIN_NAME" "$BIN_PATH"
}

is_running() {
  [ -f "$PID_FILE" ] && kill -0 "$(cat "$PID_FILE")" 2>/dev/null
}

start_agent() {
  if is_running; then
    echo "statok-install: agent already running (pid $(cat "$PID_FILE"))"
    return
  fi

  local args=( "--host" "$STATOK_HOST" )
  [ -n "$WORKLOAD" ] && args+=( "--workload" "$WORKLOAD" )

  nohup setsid "$BIN_PATH" "${args[@]}" \
    >>"$LOG_FILE" 2>&1 < /dev/null &

  echo $! > "$PID_FILE"
  echo "statok-install: agent started (pid $(cat "$PID_FILE"))"
}

parse_args() {
  while [ $# -gt 0 ]; do
    case "$1" in
      -w|--workload)
        WORKLOAD="$2"; shift 2 ;;
      -h|--help)
        usage; exit 0 ;;
      *)
        err "unknown argument: $1" ;;
    esac
  done
}

main() {
  parse_args "$@"

  TMPDIR="$(mktemp -d)"
  trap 'rm -rf "$TMPDIR"' EXIT

  if ! use_system_go; then
    setup_temp_go
  fi

  install_agent
  start_agent
}

main "$@"