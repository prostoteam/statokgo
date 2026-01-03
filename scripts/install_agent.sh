#!/usr/bin/env bash
# Install or update the statok hostmetrics agent without cloning the repo.
#
# Behavior:
# - Uses system Go if present and >= GO_MIN_VERSION.
# - Otherwise downloads a temporary Go toolchain (not persisted) used only for this build.
# - Keeps Go caches/module downloads in a temporary directory to avoid polluting user state.
# - Idempotent: safe to run repeatedly. It does not start/stop any agent process or service.

set -euo pipefail
IFS=$'\n\t'

BIN_NAME="${BIN_NAME:-statok-agent}"
INSTALL_DIR="${INSTALL_DIR:-$HOME/.local/bin}"

# If GO_VERSION is set and valid, we'll try it first; otherwise we auto-select latest stable.
GO_VERSION="${GO_VERSION:-}"
GO_MIN_VERSION="${GO_MIN_VERSION:-1.21.0}"

STATOK_VERSION="${STATOK_VERSION:-latest}"
GOFLAGS="${GOFLAGS:--buildvcs=false}"

WORKLOAD="${WORKLOAD:-}"

MODULE_PATH="github.com/prostoteam/statokgo/cmd/statok-hostmetrics"
DEFAULT_BIN_NAME="statok-hostmetrics"

err() {
  echo "statok-install: $*" >&2
  exit 1
}

need_cmd() { command -v "$1" >/dev/null 2>&1 || err "missing required command: $1"; }
have_cmd() { command -v "$1" >/dev/null 2>&1; }

usage() {
  cat <<EOF
Usage:
  $(basename "$0") [--workload <value>]

Options:
  -w, --workload     Workload label passed to the agent at runtime (only affects printed run hint).
  -h, --help         Show this help.

Environment variables:
  WORKLOAD, BIN_NAME, INSTALL_DIR, STATOK_VERSION, GO_VERSION, GO_MIN_VERSION, GOFLAGS
EOF
}

download() {
  local url="$1"
  local out="$2"
  if have_cmd curl; then
    curl -fsSL "$url" -o "$out"
  elif have_cmd wget; then
    wget -qO "$out" "$url"
  else
    err "need curl or wget to download: $url"
  fi
}

version_ge() {
  [ "$(printf '%s\n' "$2" "$1" | sort -V | head -n1)" = "$2" ]
}

is_hex64() {
  # POSIX-ish: use grep -E
  printf '%s' "$1" | grep -Eq '^[0-9a-fA-F]{64}$'
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
    err "need one of: sha256sum, shasum, or openssl (for checksum verification)"
  fi
}

ensure_linux_arch() {
  local os arch
  os="$(uname -s)"
  arch="$(uname -m)"

  if [ "$os" != "Linux" ]; then
    err "unsupported OS: $os (only Linux is supported by this installer)"
  fi

  case "$arch" in
    x86_64|amd64) echo "amd64" ;;
    aarch64|arm64) echo "arm64" ;;
    *) err "unsupported arch: $arch (supported: amd64, arm64)" ;;
  esac
}

ensure_install_dir() {
  mkdir -p "$INSTALL_DIR"
  [ -d "$INSTALL_DIR" ] || err "install dir is not a directory: $INSTALL_DIR"
  [ -w "$INSTALL_DIR" ] || err "install dir is not writable: $INSTALL_DIR"
}

use_system_go_if_ok() {
  if ! have_cmd go; then
    return 1
  fi

  local gv
  gv="$(go version 2>/dev/null | awk '{print $3}' | sed 's/^go//')"
  [ -n "${gv:-}" ] || return 1

  if version_ge "$gv" "$GO_MIN_VERSION"; then
    return 0
  fi

  echo "statok-install: system Go $gv found, but $GO_MIN_VERSION+ is required; will use a temporary Go toolchain."
  return 1
}

# Try to derive tarball + checksum for a specific version (GO_VERSION).
# Outputs: "<url> <sha256>" to stdout on success.
go_asset_for_version() {
  local version="$1"
  local arch url sha_url sha_file want_sha
  arch="$(ensure_linux_arch)"

  # Normalize: accept "1.22.3" or "go1.22.3"
  version="${version#go}"
  url="https://go.dev/dl/go${version}.linux-${arch}.tar.gz"
  sha_url="${url}.sha256"
  sha_file="$TMPDIR/go.tgz.sha256"

  download "$sha_url" "$sha_file"

  # sha file format typically: "<hash>  <filename>"
  want_sha="$(awk '{print $1}' "$sha_file" | head -n1)"

  if ! is_hex64 "${want_sha:-}"; then
    return 1
  fi

  printf '%s %s\n' "$url" "$want_sha"
}

# Auto-select latest stable linux-$arch tarball and checksum from go.dev JSON.
# Outputs: "<url> <sha256>" to stdout on success.
go_asset_latest_stable() {
  local arch json url sha filename ver
  arch="$(ensure_linux_arch)"
  json="$TMPDIR/go.dl.json"

  download "https://go.dev/dl/?mode=json" "$json"

  # Minimal JSON scraping (no jq):
  # - Find first object with "stable":true
  # - Within that object, find the file entry for linux-$arch tar.gz and extract filename + sha256.
  #
  # Assumptions: go.dev JSON structure is consistent.
  # We treat one "record" as everything until the closing "]," of the "files" array.
  ver="$(
    awk '
      BEGIN{RS="\\{"; FS="\n"}
      /"stable":[[:space:]]*true/ {
        for (i=1;i<=NF;i++) {
          if ($i ~ /"version":[[:space:]]*"/) {
            gsub(/.*"version":[[:space:]]*"/,"",$i); gsub(/".*/,"",$i);
            print $i; exit
          }
        }
      }' "$json" | head -n1
  )"
  [ -n "${ver:-}" ] || err "could not detect latest stable Go version from go.dev JSON"

  # Extract filename + sha256 for linux-$arch tar.gz for that version.
  # We locate the version block by searching forward from the version line, then pick the first matching filename.
  filename="$(
    awk -v v="$ver" -v a="$arch" '
      BEGIN{found=0}
      $0 ~ "\"version\":[[:space:]]*\""v"\"" {found=1}
      found==1 {
        if ($0 ~ "\"filename\":[[:space:]]*\"go.*linux-"a"\\.tar\\.gz\"") {
          match($0, /"filename":[[:space:]]*"([^"]+)"/, m); print m[1]; exit
        }
      }' "$json"
  )"
  sha="$(
    awk -v v="$ver" -v a="$arch" '
      BEGIN{found=0; want=0}
      $0 ~ "\"version\":[[:space:]]*\""v"\"" {found=1}
      found==1 {
        if ($0 ~ "\"filename\":[[:space:]]*\"go.*linux-"a"\\.tar\\.gz\"") {want=1}
        if (want==1 && $0 ~ "\"sha256\":[[:space:]]*\"") {
          match($0, /"sha256":[[:space:]]*"([^"]+)"/, m); print m[1]; exit
        }
      }' "$json"
  )"

  if [ -z "${filename:-}" ] || [ -z "${sha:-}" ] || ! is_hex64 "$sha"; then
    err "could not extract linux-${arch} tarball checksum from go.dev JSON"
  fi

  url="https://go.dev/dl/${filename}"
  printf '%s %s\n' "$url" "$sha"
}

setup_temp_go() {
  need_cmd tar

  local url want_sha tgz got_sha picked

  # Choose toolchain:
  # 1) If GO_VERSION is set, try it.
  # 2) Otherwise, pick latest stable from go.dev JSON.
  picked=""
  if [ -n "${GO_VERSION:-}" ]; then
    if picked="$(go_asset_for_version "$GO_VERSION" 2>/dev/null)"; then
      :
    else
      echo "statok-install: GO_VERSION=$GO_VERSION did not resolve to a valid checksum asset; falling back to latest stable."
      picked=""
    fi
  fi

  if [ -z "$picked" ]; then
    picked="$(go_asset_latest_stable)"
  fi

  url="$(printf '%s' "$picked" | awk '{print $1}')"
  want_sha="$(printf '%s' "$picked" | awk '{print $2}')"

  tgz="$TMPDIR/go.tgz"

  echo "statok-install: downloading temporary Go toolchain from $url"
  download "$url" "$tgz"

  echo "statok-install: verifying Go toolchain checksum"
  got_sha="$(sha256_calc "$tgz")"
  [ "$got_sha" = "$want_sha" ] || err "checksum mismatch for Go tarball (expected $want_sha, got $got_sha)"

  rm -rf "$TMPDIR/go"
  tar -C "$TMPDIR" -xzf "$tgz"
  [ -x "$TMPDIR/go/bin/go" ] || err "temporary Go install failed: $TMPDIR/go/bin/go not found"

  export GOROOT="$TMPDIR/go"
  export PATH="$GOROOT/bin:$PATH"

  # Sanity check
  "$GOROOT/bin/go" version >/dev/null 2>&1 || err "temporary Go toolchain is not runnable"
}

install_agent() {
  local gobin_tmp final_tmp final_path resolved_version=""

  gobin_tmp="$TMPDIR/gobin"
  mkdir -p "$gobin_tmp"

  # Isolate Go caches to temp to avoid polluting user state.
  export GOPATH="$TMPDIR/gopath"
  export GOMODCACHE="$TMPDIR/gomodcache"
  export GOCACHE="$TMPDIR/gocache"
  mkdir -p "$GOPATH" "$GOMODCACHE" "$GOCACHE"

  if [ "$STATOK_VERSION" != "latest" ]; then
    resolved_version="$STATOK_VERSION"
  else
    resolved_version="$(GOFLAGS="$GOFLAGS" go list -m -f '{{.Version}}' "github.com/prostoteam/statokgo@${STATOK_VERSION}" 2>/dev/null || true)"
  fi

  if [ -n "${resolved_version:-}" ] && [ "$resolved_version" != "latest" ]; then
    echo "statok-install: installing ${MODULE_PATH}@${resolved_version}"
  else
    echo "statok-install: installing ${MODULE_PATH}@${STATOK_VERSION}"
  fi

  # Build/install into temp bin dir first (prevents partial installs).
  GOBIN="$gobin_tmp" GOFLAGS="$GOFLAGS" go install "${MODULE_PATH}@${STATOK_VERSION}"

  final_tmp="$gobin_tmp/$DEFAULT_BIN_NAME"
  [ -f "$final_tmp" ] || err "expected built binary not found: $final_tmp"

  final_path="$INSTALL_DIR/$BIN_NAME"

  # Atomic-ish replace: stage then copy into place.
  if have_cmd install; then
    install -m 0755 -T "$final_tmp" "$final_path"
  else
    cp -f "$final_tmp" "$final_path"
    chmod 0755 "$final_path"
  fi

  echo "statok-install: installed: $final_path"
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
        usage
        exit 0
        ;;
      *)
        err "unknown argument: $1 (use --help)"
        ;;
    esac
  done
}

print_run_hint() {
  local bin_path workload_part=""
  bin_path="$INSTALL_DIR/$BIN_NAME"

  if [ -n "${WORKLOAD:-}" ]; then
    # Assumes the agent accepts: --workload <value>
    workload_part=" --workload \"${WORKLOAD}\""
  fi

  cat <<EOF
statok-install: done.

Run:
  STATOK_HOST=collector.example.com "$bin_path"$workload_part

If '$INSTALL_DIR' is not in PATH, add this to your shell profile:
  export PATH="$INSTALL_DIR:\$PATH"

Repeat runs and processes:
  - This script only installs/updates the binary; it does not start/stop the agent.
  - It does not background processes, so it will not leave zombie processes.
EOF
}

main() {
  need_cmd uname
  need_cmd awk
  need_cmd sed
  need_cmd sort
  need_cmd head
  need_cmd mktemp

  parse_args "$@"
  ensure_install_dir

  TMPDIR="$(mktemp -d -t statok-install.XXXXXX)"
  export TMPDIR
  trap 'rm -rf "$TMPDIR"' EXIT INT TERM

  if ! use_system_go_if_ok; then
    setup_temp_go
  fi

  install_agent
  print_run_hint
}

main "$@"