#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"
[[ -z "$(git status --porcelain)" ]] || { echo 'Commit your changes before releasing.' >&2; exit 1; }
[[ "$(git branch --show-current)" == main ]] || { echo 'Release from main.' >&2; exit 1; }
git fetch origin main --tags
[[ "$(git rev-parse HEAD)" == "$(git rev-parse origin/main)" ]] || { echo 'Push main before releasing.' >&2; exit 1; }
if [[ -n "${1:-}" ]]; then
  version="$1"
else
  last="$(git tag --sort=-v:refname | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | head -1 || true)"
  IFS='.' read -r major minor patch <<< "${last#v}"
  version="${major:-0}.${minor:-1}.$(( ${patch:--1} + 1 ))"
fi
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo 'Expected major.minor.patch' >&2; exit 1; }
git tag -a "v$version" -m "v$version"
git push origin "v$version"
echo "Pushed v$version — GitHub Actions is building the release: gh run list --workflow release.yml"
