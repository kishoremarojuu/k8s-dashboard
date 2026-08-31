#!/usr/bin/env bash
# Development launcher for the single-process Kubernetes Troubleshooter.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "${SCRIPT_DIR}"

command -v go >/dev/null 2>&1 || { echo "go not found on PATH" >&2; exit 1; }

exec go run . "$@"
