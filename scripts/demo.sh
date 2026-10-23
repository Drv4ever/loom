#!/usr/bin/env bash
set -Eeuo pipefail
ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
export LOOM_DATA_DIR="${LOOM_DATA_DIR:-$HOME/.loom-two-computer}"
exec "$ROOT/scripts/run.sh" "$@"
