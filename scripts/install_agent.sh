#!/usr/bin/env bash
# Install/update statok hostmetrics agent and run it in background.
# - Uses system Go if >= GO_MIN_VERSION, else uses a temporary Go toolchain (deleted on exit).
# - Installs binary to $HOME/.local/bin/statok-agent by default (configurable via env).
# - Starts agent in background with default STATOK_HOST=statok.dev0101.xyz
# - Idempotent: re-running updates binary and avoids duplicate processes.

set -euo pipefail
IFS=$'\n\t'

# Install config (env-overridable)
BIN_NAME="${BIN_NAME:-statok-agent}"
INSTALL_DIR="${INSTALL_DIR:-$HOME/.local/bin}"
BIN_PATH="$INSTALL_DIR/$BIN_NAME"

# Runtime config
STATOK_HOST_DEFAULT="statok.dev0101.xyz"
STATOK_HOST="${STATOK_HOST:-$STATOK_HOST_DEFAULT}"
WORKLOAD="${WORKLOAD:-}"

# Build config
GO_MIN_VERSION="${GO_MIN_VERSION:-1.21.0}"
STATOK_VERSION="${STATOK_VERSION:-latest}"
GOFLAGS="${GOFLAGS:--buildvcs=false}"
MODULE_PATH="github.com/prostoteam/statokgo/cmd/statok-hostmetrics"
DEFAULT_BIN_NAME="statok-hostmetrics"

# Process management (root-safe, user-safe)
PID_FILE="${PID_FILE:-$HOME/.statok-agent.pid}"
LOG_FILE="${LOG_FILE:-$HOME/.statok-agent.log}"

err() { echo "statok-install: $*" >&2; exit 1; }
need_cmd() { command -v "$1" >/dev/null 2>&1 || err "missing required command: $1"; }
have_cmd() { command -v "$1" >/dev/null 2>&1; }

usage() {
  cat <<EOF
Usage:
  $(basename "$0") [--workload <value>]

Options:
  -w, --workload   Optional workload label passed to agent at runtime.
  -h, --help       Show this help.

Env vars:
  WORKLOAD, STATOK_HOST, BIN_NAME, INSTALL_DIR, STATOK_VERSION, GO_MIN_VERSION, GOFLAGS, PID_FILE, LOG_FILE
EOF
}

download() {
  local url="$1" out="$2"
  # Defensive: strip CR/LF from URL to avoid curl(3)
  url="$(printf '%s' "$url" | tr -d '\r\n')"
  if have_cmd curl; then
    curl -fsSL "$url" -o "$out"
  elif have_cmd wget; then
    wget -qO "$out" "$url"
  else
    err "need curl or wget"
  fi
}

version_ge() {
  [ "$(printf '%s\n' "$2" "$1" | sort -V | head -n1)" = "$2" ]
}

sha256_calc() {
  local file="$1"
  if have_cmd sha256sum; then
    sha256sum "$file" | awk '{print $1}'
  elif have_cmd shasum; then
    shasum -a 256 "$file" | awk '{print $1}'
  elif have_cmd openssl; then
    openssl dgst -sha256 "$file" | awk '{print $NF}'
  else
    err "need sha256sum, shasum, or openssl"
  fi
}

ensure_linux_arch() {
  local os arch
  os="$(uname -s)"
  arch="$(uname -m)"
  [ "$os" = "Linux" ] || err "unsupported OS: $os (Linux only)"
  case "$arch" in
    x86_64|amd64) echo "amd64" ;;
    aarch64|arm64) echo "arm64" ;;
    *) err "unsupported arch: $arch (supported: amd64, arm64)" ;;
  esac
}

ensure_install_dir() {
  mkdir -p "$INSTALL_DIR"
  [ -d "$INSTALL_DIR" ] || err "install dir not a directory: $INSTALL_DIR"
  [ -w "$INSTALL_DIR" ] || err "install dir not writable: $INSTALL_DIR"
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

# Extract up to N candidate lines: "<filename> <sha256>"
# Robust state machine: within each file object, capture filename + sha256.
extract_go_candidates() {
  local json="$1" arch="$2" limit="${3:-15}"

  awk -v a="$arch" -v limit="$limit" '
    BEGIN{
      in_files=0; in_file=0;
      fname=""; sha="";
      count=0;
    }
    /"files"[[:space:]]*:[[:space:]]*\[/ { in_files=1 }
    in_files==1 && /{[[:space:]]*$/ { in_file=1; fname=""; sha=""; next }
    in_file==1 {
      if ($0 ~ /"filename"[[:space:]]*:[[:space:]]*"/) {
        match($0, /"filename"[[:space:]]*:[[:space:]]*"([^"]+)"/, m); fname=m[1];
      }
      if ($0 ~ /"sha256"[[:space:]]*:[[:space:]]*"/) {
        match($0, /"sha256"[[:space:]]*:[[:space:]]*"([^"]+)"/, m); sha=m[1];
      }
      if ($0 ~ /}[[:space:]]*,?[[:space:]]*$/) {
        # end of file object
        if (fname ~ ("linux-"a"\\.tar\\.gz$") && sha ~ /^[0-9a-fA-F]{64}$/) {
          print fname, sha;
          count++;
          if (count >= limit) exit;
        }
        in_file=0;
      }
    }
  ' "$json" \
  | tr -d '\r' \
  | awk '{gsub(/[[:space:]]+$/, "", $0); print}'
}

setup_temp_go() {
  need_cmd tar

  local arch json tgz line filename want_sha url got_sha ok=0

  arch="$(ensure_linux_arch)"
  json="$TMPDIR/go.dl.json"
  tgz="$TMPDIR/go.tgz"

  download "https://go.dev/dl/?mode=json" "$json"
  # Sanity: should start with '[' (not HTML)
  head -c 1 "$json" | grep -q '\[' || err "unexpected response from go.dev (not JSON)"

  while IFS= read -r line; do
    filename="$(printf '%s' "$line" | awk '{print $1}' | tr -d '\r\n')"
    want_sha="$(printf '%s' "$line" | awk '{print $2}' | tr -d '\r\n')"

    [ -n "$filename" ] || continue
    [ -n "$want_sha" ] || continue

    url="https://dl.google.com/go/$filename"
    echo "statok-install: downloading temporary Go toolchain: $filename"
    if ! download "$url" "$tgz"; then
      echo "statok-install: download failed for $filename; trying next candidate"
      continue
    fi

    got_sha="$(sha256_calc "$tgz" | tr -d '\r\n')"
    if [ "$got_sha" != "$want_sha" ]; then
      echo "statok-install: checksum mismatch for $filename; trying next candidate"
      continue
    fi

    echo "statok-install: checksum OK for $filename"
    ok=1

    rm -rf "$TMPDIR/go"
    tar -C "$TMPDIR" -xzf "$tgz"
    [ -x "$TMPDIR/go/bin/go" ] || err "temporary Go install failed: $TMPDIR/go/bin/go not found"

    export GOROOT="$TMPDIR/go"
    export PATH="$GOROOT/bin:$PATH"
    "$GOROOT/bin/go" version >/dev/null 2>&1 || err "temporary Go toolchain is not runnable"
    break
  done < <(extract_go_candidates "$json" "$arch" 15)

  [ "$ok" -eq 1 ] || err "could not obtain a valid Go toolchain (all candidate checksums failed)"
}

install_agent() {
  local gobin_tmp final_tmp

  gobin_tmp="$TMPDIR/gobin"
  mkdir -p "$gobin_tmp"

  # Keep Go caches temporary to avoid polluting user state.
  export GOPATH="$TMPDIR/gopath"
  export GOMODCACHE="$TMPDIR/gomodcache"
  export GOCACHE="$TMPDIR/gocache"
  mkdir -p "$GOPATH" "$GOMODCACHE" "$GOCACHE"

  echo "statok-install: installing ${MODULE_PATH}@${STATOK_VERSION}"
  GOBIN="$gobin_tmp" GOFLAGS="$GOFLAGS" go install "${MODULE_PATH}@${STATOK_VERSION}"

  final_tmp="$gobin_tmp/$DEFAULT_BIN_NAME"
  [ -f "$final_tmp" ] || err "expected built binary not found: $final_tmp"

  # Atomic-ish replace into final location.
  if have_cmd install; then
    install -m 0755 -T "$final_tmp" "$BIN_PATH"
  else
    cp -f "$final_tmp" "$BIN_PATH"
    chmod 0755 "$BIN_PATH"
  fi

  echo "statok-install: installed: $BIN_PATH"
}

pid_is_our_agent() {
  local pid="$1"
  [ -n "$pid" ] || return 1
  [ "$pid" -eq "$pid" ] 2>/dev/null || return 1
  kill -0 "$pid" 2>/dev/null || return 1
  # Best-effort: confirm executable path matches.
  if [ -r "/proc/$pid/exe" ]; then
    local exe
    exe="$(readlink -f "/proc/$pid/exe" 2>/dev/null || true)"
    [ "$exe" = "$(readlink -f "$BIN_PATH" 2>/dev/null || printf '%s' "$BIN_PATH")" ] || return 1
  fi
  return 0
}

stop_existing_if_any() {
  if [ -f "$PID_FILE" ]; then
    local pid
    pid="$(tr -d '\r\n' < "$PID_FILE" || true)"
    if pid_is_our_agent "$pid"; then
      echo "statok-install: stopping existing agent (pid $pid)"
      kill "$pid" 2>/dev/null || true
      # Give it a moment; then force if needed.
      for _ in 1 2 3 4 5; do
        if ! kill -0 "$pid" 2>/dev/null; then break; fi
        sleep 1
      done
      if kill -0 "$pid" 2>/dev/null; then
        kill -9 "$pid" 2>/dev/null || true
      fi
    fi
    rm -f "$PID_FILE"
  fi
}

start_agent_background() {
  local args=()

  [ -x "$BIN_PATH" ] || err "binary not executable: $BIN_PATH"

  # Build args only for workload (host is via env for compatibility with your earlier usage)
  if [ -n "${WORKLOAD:-}" ]; then
    args+=( "--workload" "$WORKLOAD" )
  fi

  # Ensure we do not spawn duplicates.
  # We stop any previously tracked agent (if it is ours) and then start exactly one.
  stop_existing_if_any

  echo "statok-install: starting agent in background (STATOK_HOST=$STATOK_HOST)"
  : >>"$LOG_FILE"

  # Fully detach: no job control, no zombies from this script.
  # setsid may not exist everywhere; fall back to nohup without setsid.
  if have_cmd setsid; then
    nohup env STATOK_HOST="$STATOK_HOST" "$BIN_PATH" "${args[@]}" \
      >>"$LOG_FILE" 2>&1 < /dev/null &
  else
    nohup env STATOK_HOST="$STATOK_HOST" "$BIN_PATH" "${args[@]}" \
      >>"$LOG_FILE" 2>&1 < /dev/null &
  fi

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
  need_cmd tr

  parse_args "$@"
  ensure_install_dir

  TMPDIR="$(mktemp -d -t statok-install.XXXXXX)"
  export TMPDIR
  trap 'rm -rf "$TMPDIR"' EXIT INT TERM

  if ! use_system_go_if_ok; then
    setup_temp_go
  fi

  install_agent
  start_agent_background
}

main "$@"