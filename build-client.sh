#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"
version="${1:-dev}"
mkdir -p dist
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath \
  -ldflags="-s -w -H windowsgui -X main.version=$version" -o dist/ace-spy.exe ./cmd/ace-spy
cp cmd/ace-spy/ace-spy.exe.manifest dist/
echo "Built dist/ace-spy.exe $version (+ ace-spy.exe.manifest, keep them side by side)"
