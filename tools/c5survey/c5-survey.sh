#!/usr/bin/env bash
#
# c5-survey.sh - classify ONE peer by whether the pruning-point UTXO set it serves
#                hashes to that pruning point's own header UTXO commitment.
#
# This is the acceptance test a future "refuse to export/import an unverified bucket"
# change would enforce. htnd already runs it on every import, in
# verifyAndRepairImportedPruningPointUTXOSet, and reports the outcome in three
# distinguishable log lines. This script does nothing but drive one IBD against one
# peer in an isolated datadir and read that verdict back.
#
# It changes no consensus code and touches no existing datadir.
#
# Usage:
#   ./c5-survey.sh --peer 51.89.232.58:42421 [--htnd ./htnd] [--workdir ./c5-runs]
#                  [--timeout 5400] [--p2p-port 42621] [--rpc-port 42620]
#
# Exit codes double as the verdict:
#   0  PASS        served set matched the header commitment on the first try
#   0  PASS_DEDUP  matched after de-duplicating re-delivered chunks; the set itself was fine
#   1  FAIL        matched neither - this peer is a bad exporter
#   2  OTHER       import never reached a verdict (timeout, connection refused, crash)
#
# The datadir and log are ALWAYS kept. On FAIL they are the evidence.

set -uo pipefail

PEER=""
HTND_BIN="htnd"
WORKDIR="./c5-runs"
TIMEOUT_SECONDS=5400
P2P_PORT=42621
RPC_PORT=42620

usage() { sed -n '2,30p' "$0" | sed 's/^# \{0,1\}//'; exit 2; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --peer)     PEER="${2:-}"; shift 2 ;;
    --htnd)     HTND_BIN="${2:-}"; shift 2 ;;
    --workdir)  WORKDIR="${2:-}"; shift 2 ;;
    --timeout)  TIMEOUT_SECONDS="${2:-}"; shift 2 ;;
    --p2p-port) P2P_PORT="${2:-}"; shift 2 ;;
    --rpc-port) RPC_PORT="${2:-}"; shift 2 ;;
    -h|--help)  usage ;;
    *) echo "unknown argument: $1" >&2; usage ;;
  esac
done

[[ -n "$PEER" ]] || { echo "--peer is required" >&2; usage; }

# One peer per datadir. Surveying two peers into the same datadir tells you nothing,
# because the second import starts from state the first one already installed.
RUN_ID="$(printf '%s' "$PEER" | tr -c 'A-Za-z0-9' '_')-$(date -u +%Y%m%dT%H%M%SZ)"
RUN_DIR="$WORKDIR/$RUN_ID"
DATA_DIR="$RUN_DIR/datadir"
LOG_FILE="$RUN_DIR/htnd.log"

mkdir -p "$DATA_DIR" || { echo "could not create $DATA_DIR" >&2; exit 2; }

echo "C5 survey"
echo "  peer     : $PEER"
echo "  datadir  : $DATA_DIR   (isolated, never reused, never deleted)"
echo "  log      : $LOG_FILE"
echo "  timeout  : ${TIMEOUT_SECONDS}s"
echo
echo "Note: --utxoindex is NOT enabled and is not needed. The pruning-point UTXO set is"
echo "imported and checked before any index is built, so the verdict does not depend on it."
echo "--archival is NOT needed either; C5 only classifies the exporter."
echo

# --connect pins us to exactly this peer: no DNS seeds, no peer discovery, so the
# imported set provably came from the peer being classified.
"$HTND_BIN" \
  --appdir="$DATA_DIR" \
  --connect="$PEER" \
  --listen="0.0.0.0:$P2P_PORT" \
  --rpclisten="0.0.0.0:$RPC_PORT" \
  --loglevel=info \
  >"$LOG_FILE" 2>&1 &
HTND_PID=$!

cleanup() {
  if kill -0 "$HTND_PID" 2>/dev/null; then
    kill "$HTND_PID" 2>/dev/null
    for _ in $(seq 1 30); do
      kill -0 "$HTND_PID" 2>/dev/null || break
      sleep 1
    done
    kill -9 "$HTND_PID" 2>/dev/null
  fi
}
trap cleanup EXIT INT TERM

# The three verdict lines, verbatim from
# domain/consensus/processes/consensusstatemanager/import_pruning_utxo_set.go
PASS_RE='UTXO set matches its own header commitment'
PASS_DEDUP_RE='Repaired imported pruning point .* matches the header commitment'
FAIL_RE='still does not match its header after recomputation'

VERDICT="OTHER"
DETAIL="import did not reach a verdict"
ELAPSED=0

# htnd writes the same lines to its console (captured above) and to its own rotating
# log under the appdir. Search both, so a change in either destination cannot make a
# real verdict look like a timeout.
APPDIR_LOG="$DATA_DIR/logs/htnd.log"
match_first() {
  grep -h -m1 -E "$1" "$LOG_FILE" "$APPDIR_LOG" 2>/dev/null | head -1
}

while (( ELAPSED < TIMEOUT_SECONDS )); do
  if [[ -n "$(match_first "$PASS_RE")" ]]; then
    VERDICT="PASS"; DETAIL="$(match_first "$PASS_RE")"; break
  fi
  if [[ -n "$(match_first "$PASS_DEDUP_RE")" ]]; then
    VERDICT="PASS_DEDUP"; DETAIL="$(match_first "$PASS_DEDUP_RE")"; break
  fi
  if [[ -n "$(match_first "$FAIL_RE")" ]]; then
    VERDICT="FAIL"; DETAIL="$(match_first "$FAIL_RE")"; break
  fi
  if ! kill -0 "$HTND_PID" 2>/dev/null; then
    DETAIL="htnd exited before reaching a verdict; see $LOG_FILE"
    break
  fi
  sleep 5
  ELAPSED=$(( ELAPSED + 5 ))
done

if [[ "$VERDICT" == "OTHER" && $ELAPSED -ge $TIMEOUT_SECONDS ]]; then
  DETAIL="timed out after ${TIMEOUT_SECONDS}s without an import verdict"
fi

cleanup
trap - EXIT INT TERM

echo
echo "=================================================================="
echo "C5 VERDICT: $VERDICT   peer=$PEER"
echo "$DETAIL"
echo "log kept at: $LOG_FILE"
echo "=================================================================="

case "$VERDICT" in
  PASS|PASS_DEDUP)
    echo
    echo "This peer serves a pruning-point UTXO set that reconciles with its own header"
    echo "commitment. It is a candidate bootstrap source: a node synced from it does not"
    echo "enter the offset regime, and it would pass a future export check."
    exit 0 ;;
  FAIL)
    echo
    echo "This peer serves a set matching neither the accumulated multiset nor the header."
    echo "A node syncing from it rewrites its own trust anchor and thereafter runs with"
    echo "UTXO commitment, accepted-ID merkle root, coinbase and missing-input checks all"
    echo "tolerated. Do not bootstrap from it. Keep this datadir and log."
    exit 1 ;;
  *)
    exit 2 ;;
esac
