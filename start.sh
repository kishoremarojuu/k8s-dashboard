#!/usr/bin/env bash
# Development launcher for the single-process Kubernetes Troubleshooter.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TOOL_DIR="${SCRIPT_DIR}/kube-troubleshooter"

command -v go >/dev/null 2>&1 || { echo "go not found on PATH" >&2; exit 1; }

# Keep the existing dashboard at the repository root. The Go module embeds a
# synchronized, ignored build copy so the resulting executable stays standalone.
cp "${SCRIPT_DIR}/k8s-dashboard.html" "${TOOL_DIR}/k8s-dashboard.html"
cd "${TOOL_DIR}"

exec go run . "$@"
