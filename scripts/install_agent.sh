#!/usr/bin/env bash
# Install/update statok hostmetrics agent and run it in background.
# - Uses system Go if >= GO_MIN_VERSION.
# - Otherwise tries temporary Go toolchain download (deleted on exit).
# - If downloads are blocked/altered, falls back to apt-get (Debian/Ubuntu) and removes afterwards.
# - Idempotent runtime: restarts only the agent started by this script (PID file), avoids duplicates.

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
STATOK_VERSION="${STATOK_VERSION:-latest}"
GOFLAGS="${GOFLAGS:--buildvcs=false}"

MODULE_PATH="github.com/prostoteam/statokgo/cmd/statok-hostmetrics"
DEFAULT_BIN_NAME="statok-hostmetrics"

# Only supported option
WORKLOAD="${WORKLOAD:-}"

# Track whether we installed Go via apt so we can remove it afterwards.
APT_GO_INSTALLED=0

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
    curl -fL --retry 3 --retry-delay 1 --connect-timeout 10 -sS "$url" -o "$out" \
      || return 1
  elif have_cmd wget; then
    wget --tries=3 --timeout=10 -qO "$out" "$url" \
      || return 1
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
    err "need sha256sum, shasum, or openssl for checksum verification"
  fi
}

is_hex64() { printf '%s' "$1" | grep -Eq '^[0-9a-fA-F]{64}$'; }

# Verify the downloaded file looks like gzip (tar.gz). If not, print a hint.
is_gzip_file() {
  local f="$1"
  # gzip magic bytes: 1f 8b
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

extract_go_candidates() {
  local json="$1" arch="$2" limit="${3:-12}"
  awk -v a="$arch" -v lim="$limit" '
    BEGIN{want=0; fname=""; sha=""; n=0}
    /"filename":[[:space:]]*"go[^"]*linux-'"$arch"'[.]tar[.]gz"/ {
      match($0, /"filename":[[:space:]]*"([^"]+)"/, m); fname=m[1]; want=1; next
    }
    want==1 && /"sha256":[[:space:]]*"/ {
      match($0, /"sha256":[[:space:]]*"([^"]+)"/, m); sha=m[1]
      if (fname != "" && sha != "") {
        print fname, sha
        n++
      }
      want=0; fname=""; sha=""
      if (n>=lim) exit
    }
  ' "$json"
}

setup_temp_go_or_fail() {
  need_cmd tar

  local arch json tgz fname sha url got ok=0
  arch="$(ensure_linux_arch)"

  json="$TMPDIR/go.dl.json"
  tgz="$TMPDIR/go.tgz"

  if ! download "https://go.dev/dl/?mode=json" "$json"; then
    err "failed to download go.dev JSON manifest (network/DNS/proxy issue)"
  fi

  # sanity: avoid HTML
  head -c 1 "$json" | grep -q '\[' || err "unexpected response from go.dev (not JSON)"

  while read -r fname sha; do
    fname="$(trim_ws "$fname")"
    sha="$(trim_ws "$sha")"
    [ -n "$fname" ] || continue
    is_hex64 "$sha" || continue

    url="https://dl.google.com/go/$fname"
    echo "statok-install: downloading temporary Go toolchain: $fname"

    if ! download "$url" "$tgz"; then
      echo "statok-install: download failed for $fname; trying next candidate"
      continue
    fi

    if ! is_gzip_file "$tgz"; then
      echo "statok-install: downloaded content is not a tar.gz (likely blocked/proxied). First line:"
      head -n 1 "$tgz" | sed 's/^/  /'
      echo "statok-install: trying next candidate"
      continue
    fi

    got="$(sha256_calc "$tgz")"
    if [ "$got" != "$sha" ]; then
      echo "statok-install: checksum mismatch for $fname; trying next candidate"
      continue
    fi

    echo "statok-install: checksum OK for $fname"
    ok=1

    rm -rf "$TMPDIR/go"
    tar -C "$TMPDIR" -xzf "$tgz"
    [ -x "$TMPDIR/go/bin/go" ] || err "temporary Go install failed: $TMPDIR/go/bin/go not found"

    export GOROOT="$TMPDIR/go"
    export PATH="$GOROOT/bin:$PATH"
    "$GOROOT/bin/go" version >/dev/null 2>&1 || err "temporary Go toolchain is not runnable"
    break
  done < <(extract_go_candidates "$json" "$arch" 12)

  return "$ok"
}

apt_install_go_for_build() {
  # Debian/Ubuntu fallback. Installs golang-go, uses it for build, then removes.
  have_cmd apt-get || err "could not obtain temp Go toolchain; apt-get not available for fallback"

  echo "statok-install: falling back to apt-get golang-go (temporary for build)"
  export DEBIAN_FRONTEND=noninteractive
  apt-get update -y >/dev/null
  apt-get install -y golang-go >/dev/null

  APT_GO_INSTALLED=1

  # Ensure it meets minimum
  local gv
  gv="$(go version 2>/dev/null | awk '{print $3}' | sed 's/^go//')"
  if ! version_ge "$gv" "$GO_MIN_VERSION"; then
    err "apt-installed Go $gv is still < $GO_MIN_VERSION; cannot proceed"
  fi
}

apt_remove_go_if_installed() {
  if [ "$APT_GO_INSTALLED" -eq 1 ]; then
    echo "statok-install: removing apt-installed Go packages"
    apt-get remove -y golang-go >/dev/null || true
    apt-get autoremove -y >/dev/null || true
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

  # Always cleanup temp dir and (if used) apt-installed Go.
  trap 'apt_remove_go_if_installed; rm -rf "$TMPDIR"' EXIT INT TERM

  if ! use_system_go_if_ok; then
    if ! setup_temp_go_or_fail; then
      # temp go path failed for all candidates -> apt fallback
      apt_install_go_for_build
    fi
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