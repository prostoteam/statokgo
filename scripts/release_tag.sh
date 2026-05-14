#!/usr/bin/env bash
set -euo pipefail

# Release helper:
# 1) find latest semver tag vX.Y.Z
# 2) create next patch tag
# 3) push tag to remote
# 4) warm module cache via `go get <module>@latest` in a temp module

REMOTE="${REMOTE:-origin}"
MODULE_PATH="${MODULE_PATH:-$(go list -m)}"

git fetch --tags "${REMOTE}" >/dev/null 2>&1 || true

last_tag="$(git tag --list 'v[0-9]*.[0-9]*.[0-9]*' --sort=v:refname | tail -n 1)"
if [[ -z "${last_tag}" ]]; then
  last_tag="v0.0.0"
fi

if [[ ! "${last_tag}" =~ ^v([0-9]+)\.([0-9]+)\.([0-9]+)$ ]]; then
  echo "latest tag is not semver (vX.Y.Z): ${last_tag}" >&2
  exit 1
fi

major="${BASH_REMATCH[1]}"
minor="${BASH_REMATCH[2]}"
patch="${BASH_REMATCH[3]}"
next_tag="v${major}.${minor}.$((patch + 1))"

if git rev-parse -q --verify "refs/tags/${next_tag}" >/dev/null; then
  echo "tag already exists: ${next_tag}" >&2
  exit 1
fi

echo "[release] latest: ${last_tag}"
echo "[release] next:   ${next_tag}"
echo "[release] going to: create and push ${next_tag} to ${REMOTE}, then run go get ${MODULE_PATH}@latest"

git tag "${next_tag}"
echo "[release] created tag ${next_tag}"

git push "${REMOTE}" "${next_tag}"
echo "[release] pushed ${next_tag} to ${REMOTE}"

tmp_dir="$(mktemp -d)"
cleanup() {
  rm -rf "${tmp_dir}"
}
trap cleanup EXIT

(
  cd "${tmp_dir}"
  go mod init cachewarm >/dev/null 2>&1
  go get "${MODULE_PATH}@latest" >/dev/null
)
echo "[release] cache warmed via go get ${MODULE_PATH}@latest"
echo "[release] done: ${next_tag}"
