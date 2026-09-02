#!/usr/bin/env bash
#
# c5-survey.sh - classify peers by whether the pruning-point UTXO set they serve hashes
#                to that pruning point's own header UTXO commitment, and optionally diff
#                the UTXO state two peers actually hand you.
#
# htnd already runs the classification test on every import, in
# verifyAndRepairImportedPruningPointUTXOSet, and reports the outcome in three
# distinguishable log lines. This script drives one IBD per peer in an isolated datadir
# and reads those verdicts back. It changes no consensus code and touches no existing
# datadir.
#
# It BUILDS htnd and htnctl from the working tree by default, so the verdict lines it
# greps for are the ones the current source actually emits. A stale binary on PATH is
# the classic way to get a confident, wrong answer here.
#
# Usage:
#   Classify one peer:
#     ./c5-survey.sh --peer explorer-two.hoosat.fi:42421
#
#   Classify two peers, then diff the UTXO state they produced:
#     ./c5-survey.sh --compare --peer peer-a:42421 --peer peer-b:42421
#
#   Options:
#     --peer HOST:PORT     peer to survey; repeat for --compare (exactly two)
#     --compare            after classifying, sync both fully and diff their UTXO state
#     --address ADDR       address whose outpoint set is diffed in --compare mode
#     --htnd PATH          use this prebuilt htnd instead of building (also needs --htnctl)
#     --htnctl PATH        use this prebuilt htnctl
#     --no-build           alias for "I supplied --htnd/--htnctl, do not build"
#     --workdir DIR        where run directories go (default ./c5-runs)
#     --timeout SECONDS    per-peer wait for an import verdict (default 5400)
#     --sync-timeout SEC   per-peer wait for full sync in --compare mode (default 21600)
#     --base-port N        first port used; each node takes N+2i (rpc) and N+2i+1 (p2p)
#     --require-independence   exit 3 if this peer serves a snapshot already recorded from
#                              a different peer (single-peer mode; always on for --compare)
#     --allow-shared-lineage   in --compare, accept agreement even when both peers served
#                              the identical snapshot (default: refuse, it proves nothing)
#
# Exit codes:
#   single-peer mode          0 PASS or PASS_DEDUP, 1 FAIL, 2 OTHER
#   --compare mode            0 the two nodes' UTXO state agrees
#                             1 it does not (or a peer FAILed classification)
#                             2 the comparison could not be completed
#
# Datadirs and logs are ALWAYS kept. On FAIL they are the evidence.

set -uo pipefail

PEERS=()
COMPARE=0
ADDRESS="hoosat:qz2mys3hdthqkgmpyel30xmfhvjhdej8h84yn2w7knvze38nfqs9s8k8z8n92"
HTND_BIN=""
HTNCTL_BIN=""
NO_BUILD=0
WORKDIR="./c5-runs"
TIMEOUT_SECONDS=5400
SYNC_TIMEOUT_SECONDS=21600
BASE_PORT=42620
REQUIRE_INDEPENDENCE=0
ALLOW_SHARED_LINEAGE=0

usage() { sed -n '2,45p' "$0" | sed 's/^# \{0,1\}//'; exit 2; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --peer)         PEERS+=("${2:-}"); shift 2 ;;
    --compare)      COMPARE=1; shift ;;
    --address)      ADDRESS="${2:-}"; shift 2 ;;
    --htnd)         HTND_BIN="${2:-}"; NO_BUILD=1; shift 2 ;;
    --htnctl)       HTNCTL_BIN="${2:-}"; NO_BUILD=1; shift 2 ;;
    --no-build)     NO_BUILD=1; shift ;;
    --workdir)      WORKDIR="${2:-}"; shift 2 ;;
    --timeout)      TIMEOUT_SECONDS="${2:-}"; shift 2 ;;
    --sync-timeout) SYNC_TIMEOUT_SECONDS="${2:-}"; shift 2 ;;
    --base-port)    BASE_PORT="${2:-}"; shift 2 ;;
    --require-independence) REQUIRE_INDEPENDENCE=1; shift ;;
    --allow-shared-lineage) ALLOW_SHARED_LINEAGE=1; shift ;;
    -h|--help)      usage ;;
    *) echo "unknown argument: $1" >&2; usage ;;
  esac
done

if [[ ${#PEERS[@]} -eq 0 ]]; then
  echo "at least one --peer is required" >&2; usage
fi
if [[ $COMPARE -eq 1 && ${#PEERS[@]} -ne 2 ]]; then
  echo "--compare needs exactly two --peer arguments, got ${#PEERS[@]}" >&2; exit 2
fi
if [[ $COMPARE -eq 0 && ${#PEERS[@]} -ne 1 ]]; then
  echo "without --compare, pass exactly one --peer (one peer per datadir)" >&2; exit 2
fi
if ! command -v python3 >/dev/null 2>&1; then
  echo "python3 is required (sompi totals exceed what awk's doubles can hold exactly)" >&2; exit 2
fi

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "$SCRIPT_DIR/../.." && pwd)"

RUN_STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
RUN_ROOT="$WORKDIR/survey-$RUN_STAMP"
mkdir -p "$RUN_ROOT" || { echo "could not create $RUN_ROOT" >&2; exit 2; }

# ---------------------------------------------------------------------------
# Build from the working tree
# ---------------------------------------------------------------------------
BIN_DIR="$RUN_ROOT/bin"
if [[ $NO_BUILD -eq 1 ]]; then
  [[ -n "$HTND_BIN" ]]   || { echo "--no-build/--htnctl given without --htnd" >&2; exit 2; }
  [[ -n "$HTNCTL_BIN" ]] || { echo "--no-build/--htnd given without --htnctl" >&2; exit 2; }
  echo "Using prebuilt binaries (NOT built from this tree):"
else
  command -v go >/dev/null 2>&1 || { echo "go toolchain not found and --htnd not given" >&2; exit 2; }
  mkdir -p "$BIN_DIR"
  HTND_BIN="$BIN_DIR/htnd"
  HTNCTL_BIN="$BIN_DIR/htnctl"
  echo "Building from $REPO_ROOT ..."
  if ! ( cd "$REPO_ROOT" && go build -o "$(realpath -m "$HTND_BIN")" . ); then
    echo "htnd build failed" >&2; exit 2
  fi
  if ! ( cd "$REPO_ROOT" && go build -o "$(realpath -m "$HTNCTL_BIN")" ./cmd/htnctl ); then
    echo "htnctl build failed" >&2; exit 2
  fi
  echo "Built from this tree:"
fi

GIT_DESCRIBE="$( cd "$REPO_ROOT" && git rev-parse --short HEAD 2>/dev/null )"
GIT_DIRTY=""
if [[ -n "$GIT_DESCRIBE" ]] && ! ( cd "$REPO_ROOT" && git diff --quiet HEAD 2>/dev/null ); then
  GIT_DIRTY=" (working tree has uncommitted changes)"
fi
echo "  htnd    : $HTND_BIN  version $("$HTND_BIN" --version 2>/dev/null | head -1)"
echo "  htnctl  : $HTNCTL_BIN"
echo "  source  : ${GIT_DESCRIBE:-unknown}${GIT_DIRTY}"
echo

# ---------------------------------------------------------------------------
# The three verdict lines, verbatim from
# domain/consensus/processes/consensusstatemanager/import_pruning_utxo_set.go
# ---------------------------------------------------------------------------
PASS_RE='UTXO set matches its own header commitment'
PASS_DEDUP_RE='Repaired imported pruning point .* matches the header commitment'
FAIL_RE='still does not match its header after recomputation'

# A snapshot's fingerprint is (pruning point, entry count, bucket multiset, header
# commitment). Two peers reporting the SAME fingerprint did not independently arrive at the
# same answer - they served the same bytes, which means a shared export ancestor rather
# than independent agreement. Surveying more copies of one export produces more FAILs and
# no new evidence, so the fingerprint is extracted, printed, and remembered across runs.
#
# Fields come from the verdict line plus the chunk-transfer line, all in
# domain/consensus/processes/consensusstatemanager/import_pruning_utxo_set.go and ibd.go:
#   FAIL       "... pruning point <PP> UTXO set still does not match its header after
#               recomputation (header <HDR>, fresh multiset over <N> stored entries <MS>)"
#   PASS       "... pruning point <PP> UTXO set matches its own header commitment <HDR>"
#   PASS_DEDUP "Repaired imported pruning point <PP> ... over the <N> stored entries
#               matches the header commitment <HDR>"
# On PASS and PASS_DEDUP the bucket multiset equals the header commitment by definition.
FP_PP=""; FP_ENTRIES=""; FP_MULTISET=""; FP_HEADER=""

extract_fingerprint() {  # extract_fingerprint <index>
  local index="$1" line="${NODE_DETAIL[$1]}"
  FP_PP=""; FP_ENTRIES=""; FP_MULTISET=""; FP_HEADER=""

  FP_PP="$(sed -n 's/.*pruning point \([0-9a-f]\{64\}\).*/\1/p' <<<"$line" | head -1)"

  # Entry count: the transfer line is authoritative and present for every verdict.
  FP_ENTRIES="$(match_first "$index" 'Finished receiving the UTXO set\. Total UTXOs: [0-9]' \
                | sed -n 's/.*Total UTXOs: \([0-9]\{1,\}\).*/\1/p')"
  [[ -n "$FP_ENTRIES" ]] || FP_ENTRIES="$(sed -n 's/.*over \(the \)\{0,1\}\([0-9]\{1,\}\) stored entries.*/\2/p' <<<"$line" | head -1)"

  case "${NODE_VERDICT[$index]}" in
    FAIL)
      FP_HEADER="$(sed -n 's/.*(header \([0-9a-f]\{64\}\),.*/\1/p' <<<"$line" | head -1)"
      FP_MULTISET="$(sed -n 's/.*stored entries \([0-9a-f]\{64\}\).*/\1/p' <<<"$line" | head -1)"
      ;;
    PASS|PASS_DEDUP)
      FP_HEADER="$(sed -n 's/.*commitment \([0-9a-f]\{64\}\).*/\1/p' <<<"$line" | head -1)"
      FP_MULTISET="$FP_HEADER"
      ;;
  esac
}

fingerprint_key() { printf '%s/%s/%s' "${FP_PP:-?}" "${FP_ENTRIES:-?}" "${FP_MULTISET:-?}"; }

# The ledger lives beside the run directories so it accumulates across invocations - that is
# the whole point, since the question is whether a NEW peer is another copy of one already
# surveyed.
LEDGER="$WORKDIR/fingerprints.tsv"

record_fingerprint() {  # record_fingerprint <index>
  local index="$1"
  [[ -n "$FP_PP" ]] || return 0
  if [[ ! -f "$LEDGER" ]]; then
    printf 'timestamp\tpeer\tverdict\tpruningPoint\tentries\tbucketMultiset\theaderCommitment\n' > "$LEDGER"
  fi
  printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
    "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "${PEERS[$index]}" "${NODE_VERDICT[$index]}" \
    "$FP_PP" "${FP_ENTRIES:-?}" "${FP_MULTISET:-?}" "${FP_HEADER:-?}" >> "$LEDGER"
}

# prior_peers_with_same_fingerprint <index> - other peers already recorded serving these
# exact bytes. Excludes this peer, so re-running the same peer is not mistaken for lineage.
prior_peers_with_same_fingerprint() {
  local index="$1"
  [[ -f "$LEDGER" && -n "$FP_PP" ]] || return 0
  awk -F'\t' -v pp="$FP_PP" -v en="${FP_ENTRIES:-?}" -v ms="${FP_MULTISET:-?}" -v me="${PEERS[$index]}" \
    'NR>1 && $4==pp && $5==en && $6==ms && $2!=me {print $2}' "$LEDGER" | sort -u
}

declare -a NODE_DIR NODE_LOG NODE_PID NODE_RPC NODE_VERDICT NODE_DETAIL

cleanup_all() {
  local i
  for i in "${!NODE_PID[@]}"; do
    local pid="${NODE_PID[$i]}"
    [[ -n "$pid" ]] || continue
    if kill -0 "$pid" 2>/dev/null; then
      kill "$pid" 2>/dev/null
      local waited=0
      while kill -0 "$pid" 2>/dev/null && (( waited < 60 )); do sleep 1; waited=$((waited+1)); done
      kill -9 "$pid" 2>/dev/null
    fi
  done
}
trap cleanup_all EXIT INT TERM

# start_node <index> <peer>
start_node() {
  local index="$1" peer="$2"
  local safe_peer; safe_peer="$(printf '%s' "$peer" | tr -c 'A-Za-z0-9' '_')"
  local dir="$RUN_ROOT/node${index}-${safe_peer}"
  local data_dir="$dir/datadir"
  local log_file="$dir/htnd.log"
  local rpc_port=$(( BASE_PORT + index * 2 ))
  local p2p_port=$(( BASE_PORT + index * 2 + 1 ))

  mkdir -p "$data_dir" || return 1

  # --utxoindex is only needed for --compare (GetCoinSupply / GetUtxosByAddresses). It is
  # NOT needed to classify a peer: the pruning-point set is imported and checked before any
  # index exists. Enabling it in compare mode costs sync time, so it is conditional.
  local index_flag=()
  [[ $COMPARE -eq 1 ]] && index_flag=(--utxoindex)

  # --connect pins exactly this peer: no DNS seeds, no discovery, so whatever gets imported
  # provably came from the peer being classified.
  "$HTND_BIN" \
    --appdir="$data_dir" \
    --connect="$peer" \
    --listen="0.0.0.0:$p2p_port" \
    --rpclisten="0.0.0.0:$rpc_port" \
    --loglevel=info \
    "${index_flag[@]}" \
    >"$log_file" 2>&1 &

  NODE_DIR[$index]="$dir"
  NODE_LOG[$index]="$log_file"
  NODE_PID[$index]=$!
  NODE_RPC[$index]=":$rpc_port"
  NODE_VERDICT[$index]="OTHER"
  NODE_DETAIL[$index]="import did not reach a verdict"

  echo "node$index  peer=$peer  rpc=:$rpc_port  p2p=:$p2p_port"
  echo "          datadir=$data_dir"
  echo "          log=$log_file"
}

# match_first <index> <regex> - searches both the captured console output and htnd's own
# rotating log under the appdir, so a change in either destination cannot make a real
# verdict look like a timeout.
match_first() {
  local index="$1" regex="$2"
  grep -h -m1 -E "$regex" \
    "${NODE_LOG[$index]}" "${NODE_DIR[$index]}/datadir/hoosat-mainnet/logs/htnd.log" \
    2>/dev/null | head -1
}

# await_verdict <index>
await_verdict() {
  local index="$1" elapsed=0 hit=""
  while (( elapsed < TIMEOUT_SECONDS )); do
    if hit="$(match_first "$index" "$PASS_RE")" && [[ -n "$hit" ]]; then
      NODE_VERDICT[$index]="PASS"; NODE_DETAIL[$index]="$hit"; return 0
    fi
    if hit="$(match_first "$index" "$PASS_DEDUP_RE")" && [[ -n "$hit" ]]; then
      NODE_VERDICT[$index]="PASS_DEDUP"; NODE_DETAIL[$index]="$hit"; return 0
    fi
    if hit="$(match_first "$index" "$FAIL_RE")" && [[ -n "$hit" ]]; then
      NODE_VERDICT[$index]="FAIL"; NODE_DETAIL[$index]="$hit"; return 0
    fi
    if ! kill -0 "${NODE_PID[$index]}" 2>/dev/null; then
      NODE_DETAIL[$index]="htnd exited before reaching a verdict; see ${NODE_LOG[$index]}"
      return 0
    fi
    sleep 5
    elapsed=$(( elapsed + 5 ))
  done
  NODE_DETAIL[$index]="timed out after ${TIMEOUT_SECONDS}s without an import verdict"
}

rpc() {  # rpc <index> <htnctl args...>
  local index="$1"; shift
  "$HTNCTL_BIN" "$@" -a -s "${NODE_RPC[$index]}" 2>/dev/null
}

# await_sync <index> - GetInfo.isSynced is htnd's own answer to "am I caught up".
await_sync() {
  local index="$1" elapsed=0
  while (( elapsed < SYNC_TIMEOUT_SECONDS )); do
    if ! kill -0 "${NODE_PID[$index]}" 2>/dev/null; then
      echo "node$index exited before finishing sync" >&2; return 1
    fi
    local synced
    synced="$(rpc "$index" GetInfo | python3 -c \
      'import json,sys
try: print(str(json.load(sys.stdin)["getInfoResponse"].get("isSynced", False)).lower())
except Exception: print("unknown")' 2>/dev/null)"
    if [[ "$synced" == "true" ]]; then return 0; fi
    sleep 30
    elapsed=$(( elapsed + 30 ))
  done
  echo "node$index did not report isSynced within ${SYNC_TIMEOUT_SECONDS}s" >&2
  return 1
}

# ---------------------------------------------------------------------------
# Phase 1: classify every peer
# ---------------------------------------------------------------------------
if [[ $COMPARE -eq 1 ]]; then
  echo "Mode: --compare. Both nodes run with --utxoindex (GetCoinSupply and"
  echo "GetUtxosByAddresses need it) and must sync fully, which takes far longer than"
  echo "classification alone. --archival is still not needed."
else
  echo "Mode: classify one peer. --utxoindex is NOT enabled and is not needed: the"
  echo "pruning-point UTXO set is imported and checked before any index exists."
  echo "--archival is not needed either."
fi
echo

echo "=== classification ==="
for i in "${!PEERS[@]}"; do
  start_node "$i" "${PEERS[$i]}" || { echo "could not start node$i" >&2; exit 2; }
done
echo
declare -a NODE_FPKEY NODE_SHARED_WITH
for i in "${!PEERS[@]}"; do
  await_verdict "$i"
  echo "node$i  peer=${PEERS[$i]}  VERDICT: ${NODE_VERDICT[$i]}"
  echo "        ${NODE_DETAIL[$i]}"

  extract_fingerprint "$i"
  NODE_SHARED_WITH[$i]="$(prior_peers_with_same_fingerprint "$i" | paste -sd, -)"
  record_fingerprint "$i"
  NODE_FPKEY[$i]="$(fingerprint_key)"

  echo "        fingerprint:"
  echo "          pruningPoint     ${FP_PP:-n/a}"
  echo "          entries          ${FP_ENTRIES:-n/a}"
  echo "          bucketMultiset   ${FP_MULTISET:-n/a}"
  echo "          headerCommitment ${FP_HEADER:-n/a}"
  if [[ -n "${NODE_SHARED_WITH[$i]}" ]]; then
    echo "        SHARED_EXPORT: these exact bytes were already recorded from ${NODE_SHARED_WITH[$i]}."
    echo "                       This peer is another copy of that export, not new evidence."
  fi
done
echo
echo "Fingerprint ledger: $LEDGER"
echo

if [[ $COMPARE -eq 0 ]]; then
  cleanup_all; trap - EXIT INT TERM
  echo "=================================================================="
  echo "C5 VERDICT: ${NODE_VERDICT[0]}   peer=${PEERS[0]}"
  echo "log kept at: ${NODE_LOG[0]}"
  echo "=================================================================="
  if [[ -n "${NODE_SHARED_WITH[0]}" ]]; then
    echo
    echo "SHARED_EXPORT: identical to the snapshot already served by ${NODE_SHARED_WITH[0]}."
    if [[ $REQUIRE_INDEPENDENCE -eq 1 ]]; then
      echo "--require-independence was given, so this run did not add evidence. Exiting 3."
      exit 3
    fi
    echo "Survey a peer with an unrelated history to learn anything new."
  fi
  case "${NODE_VERDICT[0]}" in
    PASS|PASS_DEDUP)
      echo
      echo "This peer serves a pruning-point UTXO set that reconciles with its own header"
      echo "commitment. It is a candidate bootstrap source."
      exit 0 ;;
    FAIL)
      echo
      echo "This peer serves a set matching neither the accumulated multiset nor the header."
      echo "A node syncing from it rewrites its own trust anchor and thereafter runs with"
      echo "UTXO commitment, accepted-ID merkle root, coinbase and missing-input checks all"
      echo "tolerated. Do not bootstrap from it. Keep this datadir and log."
      exit 1 ;;
    *) exit 2 ;;
  esac
fi

# ---------------------------------------------------------------------------
# Phase 2: --compare. Sync both fully, then diff the UTXO state they produced.
#
# This is the question two FAIL classifications cannot answer on their own: a peer failing
# against its OWN pruning point header does not tell you whether two peers agree with EACH
# OTHER. If their sets match, the network agrees on state and the commitment rule is what
# diverged. If their sets differ, state itself has diverged.
#
# Note the delta test you would reach for first - "is the offset a constant?" - is not
# computable: headers carry only the MuHash hash, and hashes are not invertible, so one
# multiset cannot be subtracted from another. Comparing the sets is the computable
# substitute.
# ---------------------------------------------------------------------------
echo "=== waiting for both nodes to finish syncing (up to ${SYNC_TIMEOUT_SECONDS}s each) ==="
for i in 0 1; do
  echo "node$i syncing from ${PEERS[$i]} ..."
  if ! await_sync "$i"; then
    echo "COMPARISON INCOMPLETE: node$i never reported isSynced. Datadirs kept." >&2
    exit 2
  fi
  echo "node$i is synced."
done
echo

SHARED_SNAPSHOT=0
if [[ -n "${NODE_FPKEY[0]}" && "${NODE_FPKEY[0]}" == "${NODE_FPKEY[1]}" && "${NODE_FPKEY[0]}" != "?/?/?" ]]; then
  SHARED_SNAPSHOT=1
  echo "SHARED_EXPORT: both peers served an IDENTICAL pruning-point snapshot"
  echo "               ${NODE_FPKEY[0]}"
  echo "               They did not independently arrive at the same state - they handed over"
  echo "               the same bytes. Agreement below is evidence of a shared export ancestor,"
  echo "               NOT evidence that the state is correct."
  if [[ $ALLOW_SHARED_LINEAGE -eq 1 ]]; then
    echo "               --allow-shared-lineage given: continuing and reporting it as agreement."
    SHARED_SNAPSHOT=0
  fi
  echo
fi

for i in 0 1; do
  rpc "$i" GetBlockDagInfo > "$RUN_ROOT/daginfo.$i.json"
  rpc "$i" GetCoinSupply   > "$RUN_ROOT/coinsupply.$i.json"
  rpc "$i" GetUtxosByAddresses "$ADDRESS" 0 > "$RUN_ROOT/utxos.$i.json"
done

python3 - "$RUN_ROOT" "${PEERS[0]}" "${PEERS[1]}" "$ADDRESS" "$SHARED_SNAPSHOT" <<'PYTHON'
import json, sys

run_root, peer0, peer1, address = sys.argv[1:5]
shared_snapshot = sys.argv[5] == "1"

def load(name, index):
    with open(f"{run_root}/{name}.{index}.json") as handle:
        return json.load(handle)

def entries(index):
    payload = load("utxos", index).get("getUtxosByAddressesResponse", {})
    result = {}
    for entry in payload.get("entries") or []:
        outpoint = entry["outpoint"]
        key = f"{outpoint['transactionId']}:{outpoint.get('index', 0)}"
        utxo = entry["utxoEntry"]
        result[key] = (int(utxo["amount"]), int(utxo["blockDaaScore"]),
                       bool(utxo.get("isCoinbase", False)))
    return result

dag = [load("daginfo", i).get("getBlockDagInfoResponse", {}) for i in (0, 1)]
supply = [int(load("coinsupply", i)["getCoinSupplyResponse"]["circulatingSompi"]) for i in (0, 1)]
sets = [entries(0), entries(1)]
virtual_daa = [int(dag[i].get("virtualDaaScore", 0)) for i in (0, 1)]

print("=" * 74)
print("C5 COMPARE")
print(f"  node0 peer={peer0}")
print(f"  node1 peer={peer1}")
print(f"  address={address}")
print("=" * 74)

print("\n-- chain position --")
for i in (0, 1):
    print(f"  node{i}: pruningPoint={dag[i].get('pruningPointHash','?')}")
    print(f"         virtualDaaScore={virtual_daa[i]}  blockCount={dag[i].get('blockCount','?')}")
same_pp = dag[0].get("pruningPointHash") == dag[1].get("pruningPointHash")
daa_gap = abs(virtual_daa[0] - virtual_daa[1])
print(f"  same pruning point: {same_pp}   virtual DAA gap: {daa_gap}")

print("\n-- circulating supply --")
for i in (0, 1):
    print(f"  node{i}: {supply[i]:,}")
supply_delta = supply[0] - supply[1]
print(f"  delta : {supply_delta:,} sompi ({supply_delta / 1e8:,.8f} HTN)")

print(f"\n-- outpoint set for {address} --")
only0 = {k: v for k, v in sets[0].items() if k not in sets[1]}
only1 = {k: v for k, v in sets[1].items() if k not in sets[0]}
shared_differ = {k: (sets[0][k], sets[1][k])
                 for k in sets[0].keys() & sets[1].keys() if sets[0][k] != sets[1][k]}

for i in (0, 1):
    print(f"  node{i}: {len(sets[i])} outpoints, sum {sum(a for a, _, _ in sets[i].values()):,}")
print(f"  only on node0 : {len(only0)} outpoints, sum {sum(a for a, _, _ in only0.values()):,}")
print(f"  only on node1 : {len(only1)} outpoints, sum {sum(a for a, _, _ in only1.values()):,}")
print(f"  shared outpoints whose entry differs: {len(shared_differ)}")
for key, (a, b) in list(shared_differ.items())[:5]:
    print(f"    {key}  node0 amount={a[0]} daa={a[1]}  node1 amount={b[0]} daa={b[1]}")

# Divergence close to the tip is ordinary - the two nodes are simply a few blocks apart.
# Divergence deep in history is not, and it is the signature that matters.
tip = max(virtual_daa) if max(virtual_daa) else 0
TIP_WINDOW = 10000
deep = [(k, v) for k, v in list(only0.items()) + list(only1.items()) if tip - v[1] > TIP_WINDOW]
if deep:
    daas = [v[1] for _, v in deep]
    print(f"\n  {len(deep)} of the differing outpoints are more than {TIP_WINDOW} DAA below the tip")
    print(f"  (DAA range {min(daas)} .. {max(daas)}, tip {tip}) - not explainable by tip lag.")
    coinbase_count = sum(1 for _, v in deep if v[2])
    print(f"  of those, coinbase: {coinbase_count}, regular: {len(deep) - coinbase_count}")

agree = (not only0 and not only1 and not shared_differ and supply_delta == 0)
print("\n" + "=" * 74)
if agree and shared_snapshot:
    print("VERDICT: the two nodes agree - but they were handed the SAME snapshot.")
    print("  Both peers served byte-identical pruning-point sets, so this is agreement by")
    print("  shared lineage, not two independent derivations landing on the same answer.")
    print("  It does NOT establish that the state is correct, and it does NOT rule out C1.")
    print("  Survey peers with unrelated histories before drawing a conclusion.")
elif agree:
    print("VERDICT: the two nodes AGREE on UTXO state, from different snapshots.")
    print("  Two independent derivations landed on the same set, which is real evidence that")
    print("  the network agrees on state and that what diverged is the commitment rule.")
    print("  Confirm with the supply check below before acting on it.")
else:
    print("VERDICT: the two nodes DISAGREE on UTXO state.")
    if deep:
        print("  The disagreement reaches deep into history, so it is not tip lag.")
    print("  State itself has diverged between peers. An archival replay from genesis (C1)")
    print("  is the only anchor, and the ambient constants.GetBlockVersion() leak documented")
    print("  in docs/utxo-set-verification.md section 6 becomes critical path.")

# Circulating supply is coinbase-only, so it never decreases. Comparing this run against an
# earlier measurement at a LOWER virtual DAA score is the one cheap check that can prove a
# set is wrong without knowing which set is right - and nobody was running it.
print("\n-- monotonicity check --")
print(f"  This run: {max(supply):,} sompi at virtual DAA {max(virtual_daa)}.")
print("  Circulating supply only ever grows. If you have an EARLIER measurement (lower")
print("  virtual DAA) reporting MORE supply than this, then one of the two is wrong -")
print("  regardless of how well any two peers agree with each other. Record this pair and")
print("  compare it against your next run.")
print("=" * 74)

sys.exit(0 if (agree and not shared_snapshot) else 1)
PYTHON
COMPARE_STATUS=$?

cleanup_all; trap - EXIT INT TERM

echo
echo "Datadirs and logs kept under: $RUN_ROOT"
for i in 0 1; do
  echo "  node$i (${PEERS[$i]}): classification=${NODE_VERDICT[$i]}  ${NODE_DIR[$i]}"
done

if [[ "${NODE_VERDICT[0]}" == "FAIL" || "${NODE_VERDICT[1]}" == "FAIL" ]]; then
  echo
  echo "At least one peer FAILed classification, so neither node is a bootstrap source"
  echo "regardless of whether they agree with each other."
  exit 1
fi
exit $COMPARE_STATUS
