#!/bin/sh
# Read-only htnd diagnostic.
#
# Collects, in one run, what is needed to tell apart the different problems that all present as
# "mining is not working". It only talks to the node over RPC and reads its logs. It never opens,
# copies or modifies the datadir, and it is safe to run against a live node.
#
# Usage:
#   sh deploy/diagnose-htnd.sh                         # node's RPC on localhost, logs from docker
#   HTND_CONTAINER=htnd-public sh deploy/diagnose-htnd.sh
#   HTND_RPC=127.0.0.1:42420 HTND_LOG=/var/log/htnd.log sh deploy/diagnose-htnd.sh
#
# Paste the whole output back.

RPC="${HTND_RPC:-localhost}"
CONTAINER="${HTND_CONTAINER:-htnd}"
LOGFILE="${HTND_LOG:-}"
HTNCTL="${HTNCTL:-htnctl}"

# -a: htnctl otherwise refuses to talk to a node built from a different commit, which is exactly the
# situation being diagnosed.
rpc() {
  "$HTNCTL" -a -s "$RPC" "$@" 2>&1
}

logs() {
  if [ -n "$LOGFILE" ]; then
    tail -n 20000 "$LOGFILE" 2>/dev/null
  else
    docker logs --tail 20000 "$CONTAINER" 2>&1
  fi
}

section() {
  echo
  echo "=================== $1"
}

section "1. Which build is running"
logs | grep -m 3 -E "Version [0-9]" || echo "(no Version line in the retained logs)"
echo "Expected for the HTN-204 fix: a build from commit 15dee9d50 or later on remediation/sept-2026."

section "2. Node state"
rpc GetInfo

section "3. DAG and difficulty"
# difficulty 65536.01 on mainnet is exactly genesis difficulty (HTN-204).
# difficulty 1 means the target is at powMax, the floor (HTN-228) - a hashrate question, not a bug.
rpc GetBlockDagInfo

section "4. Estimated network hashrate"
rpc EstimateNetworkHashesPerSecond 1000

section "5. Is the node producing block templates at all"
logs | grep -c "does not exist in db" | sed 's/^/"does not exist in db" occurrences: /'
logs | grep -m 3 "does not exist in db"

section "6. Repair flags left in the launch command"
logs | grep -m 2 -E "repair-block-statuses is set|repair-missing-multisets is set" \
  || echo "(no repair-flag warning - good, or the build predates the warning)"
logs | grep -m 2 "RepairMissingMultisets marked"

section "7. UTXO baseline"
logs | grep -m 3 -E "offset UTXO baseline|does not match its own header"

section "8. Blocks accepted and rejected in the retained logs"
echo "accepted (valid / pending verification):"
logs | grep -cE "StatusUTXOValid|UTXOPendingVerification"
echo "rejected / invalid:"
logs | grep -ciE "block rejected|StatusInvalid|DisqualifiedFromChain"
logs | grep -m 5 -iE "block rejected"

section "9. Most recent warnings and errors"
logs | grep -E "\[(WRN|ERR|CRT)\]" | tail -n 25

section "10. Most recent log lines"
logs | tail -n 15

echo
echo "=================== done"
