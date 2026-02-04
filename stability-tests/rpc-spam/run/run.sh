#!/usr/bin/env bash
set -euo pipefail

# Example:
#   ./run.sh --duration=60s --workers=200 --clients=10 --start-node
#   ./run.sh --rpc-address=127.0.0.1:42420 --duration=2m --workers=500 --clients=50

RUN_STABILITY_TESTS=true go test .. -v -timeout 86400s -- "$@"
