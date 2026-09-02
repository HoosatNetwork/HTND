# Pruning-point UTXO set: verification, the C5 survey, and the wipe policy

Operator guide for the observation-only work in *Stage A*. Nothing described here changes
what a node builds, stores, serves or imports. It exists so that a fact which was previously
invisible — whether the pruning-point UTXO set a node **serves** reconciles with that pruning
point's own header UTXO commitment — is durable, greppable, and safe to act on.

---

## 0. Read this before trusting any log line

Until Stage A, `updatePruningPoint` printed

```
Validating the UTXO set fits commitment
```

*outside* the `shouldSanityCheckPruningUTXOSet` guard on the following line. That flag
(`--enable-sanity-check-pruning-utxo`) is hidden and default-off, so on a normal node the line
appeared on every pruning-point advancement while the check itself never ran.

**If you are reading a log from before this change, that line proves nothing.** After it, the
line is printed only when the check actually runs, and the skip path says so at debug level.

---

## 1. The always-on status line

Every boot now prints one line after the pruning point is loaded, with no flag required:

```
Pruning point UTXO set: pp=<hash> header=<16hex> bucket=<16hex> perBlock=<16hex|n/a> \
    diffChain=<16hex|n/a> marker=<verified|unverified|unknown> checkedAtDAA=<n> entries=<n>
```

| field | producer | notes |
|---|---|---|
| `header` | `blockHeaderStore.BlockHeader(pp).UTXOCommitment()` | the only externally anchored value |
| `bucket` | fresh MuHash over `pruningStore`'s served bucket | from the marker; not rescanned at boot |
| `perBlock` | `calculateMultiset` (`multisets.go`) via `multiSetStore` | a running sum rooted at the import anchor, **not** an independent reconstruction |
| `diffChain` | `restorePastUTXO` | only ever computed under `--enable-utxo-debug-diagnostics`; `n/a` otherwise |
| `marker` | the persisted `pruning-utxo-verified` record | `unknown` when absent **or when it belongs to an older pruning point** |

The line is cheap by design: it reports the persisted marker rather than re-hashing ~14M
entries. When no marker exists for the current pruning point it says so and starts the scan in
the background — boot is never delayed, and the verdict is logged when it completes and printed
directly on the next boot.

`marker=unverified` means: **this node is serving a bucket that does not reconcile with its own
header commitment.** It keeps serving it. This release only records the fact.

### The memoisation gotcha

`VerifyCurrentPruningPointUTXOSet` (the `--enable-utxo-debug-diagnostics` path) memoises on the
pruning point and skips the rescan on a later boot. That skip now recalls the marker's numbers
and labels them explicitly:

```
RECALLED verdict from that run (not recomputed now): header=... bucket=... marker=...
```

**A recalled verdict is not a fresh pass.** If you need a current one, wait for the pruning
point to advance, or read the always-on line above, which is keyed to the *current* pruning
point and reports `unknown` rather than a stale verdict.

---

## 2. Is this datadir latched?

A node whose import rewrote its own trust anchor runs with UTXO commitment, accepted-ID merkle
root, coinbase and missing-input checks all tolerated, for the life of that pruning point.

```bash
grep -c "on an offset UTXO baseline"                          htnd.log
grep -c "still does not match its header after recomputation" htnd.log
grep -c "is being TOLERATED"                                  htnd.log
grep -c "block-transaction-missing-input"                     htnd.log
```

Any non-zero count on the first two means latched. A non-zero fourth count means transactions
were skipped outright — those are missing coins.

New in Stage A, and worth grepping for as well:

```bash
grep "is MISSING from the accumulated UTXO diff after AddTransaction" htnd.log
```

That warn fires when a **non-coinbase** output that `AddTransaction` accepted without error
never reached the diff. The check existed before but was gated on `isCoinbase`, which is
backwards for this failure: every divergent outpoint measured in the incident that motivated
this work was non-coinbase.

---

## 3. The C5 survey

**Question:** does any peer serve a pruning-point UTXO set that reconciles with its own header
commitment? If even one does, it is a bootstrap source and the whole "nobody can be the first
good node" problem disappears.

`verifyAndRepairImportedPruningPointUTXOSet` already performs exactly this test on every
import, and reports one of three lines from
`domain/consensus/processes/consensusstatemanager/import_pruning_utxo_set.go`:

| verdict | log line |
|---|---|
| `PASS` | `Imported pruning point ... UTXO set matches its own header commitment ...` |
| `PASS_DEDUP` | `Repaired imported pruning point ... a fresh multiset over the ... stored entries matches the header commitment` |
| `FAIL` | `... still does not match its header after recomputation` |

`PASS_DEDUP` is a pass: it means chunk re-delivery double-counted the accumulator while the
outpoint-keyed set itself was fine.

### Running it

```bash
tools/c5survey/c5-survey.sh --peer 51.89.232.58:42421 --htnd ./htnd
```

Rules the script enforces, and why:

- **Isolated datadir, isolated p2p and RPC ports.** It never touches a production datadir.
- **`--connect` pins exactly one peer.** No DNS seeds, no discovery — so the imported set
  provably came from the peer being classified.
- **One peer per datadir, never reused.** A second import starts from state the first one
  already installed, so the answer would be about the datadir, not the peer.
- **`--utxoindex` is not enabled and not needed.** The pruning-point UTXO set is imported and
  checked before any index is built; the verdict does not depend on it.
- **`--archival` is not needed.** C5 classifies the exporter, nothing more.
- **The datadir and log are always kept, especially on `FAIL`** — they are the evidence.

Exit codes: `0` PASS or PASS_DEDUP, `1` FAIL, `2` OTHER (timeout, refused, crash).

### Reading the result

A `PASS`/`PASS_DEDUP` peer is a candidate bootstrap source. A `FAIL` peer is one that will put
any node syncing from it into the offset regime — the exact mechanism observed in production,
where a public peer's set matched neither the accumulated multiset nor the header, the importing
node rewrote its trust anchor, and four checks went quiet within sixty seconds.

If **every** peer surveyed returns `FAIL`, C5 is empty and the fallback is C1 — see below.

---

## 4. Live-node probes that require no wipe

These are read-only and safe on production nodes.

```bash
ADDR=hoosat:qz2mys3hdthqkgmpyel30xmfhvjhdej8h84yn2w7knvze38nfqs9s8k8z8n92

for P in :42720 :42820; do
  echo "== $P =="
  htnctl GetCoinSupply                     -a -s $P   # circulatingSompi must match across nodes
  htnctl GetBlockDagInfo                   -a -s $P   # pruningPointHash, virtualDaaScore, virtualParentHashes
  htnctl GetSelectedTipHash                -a -s $P
  htnctl GetVirtualSelectedParentBlueScore -a -s $P
  htnctl GetTransactionStatus c4b85c94c0fb22b8c8ded09ba4c55aaf1f93f128974d8d5678b2072b6db7dfd2 -a -s $P
  htnctl GetTransactionStatus c64133f732275fdd8d8fd674f272a990ecf1155e6739a7c4cdcf74e64b2c268d -a -s $P
done
```

Then the address itself. `Limit 0` means unlimited (`UTXODefaultMaxLimit` defaults to 0):

```bash
for P in :42720 :42820; do
  htnctl GetUtxosByAddresses "$ADDR" 0 -a -s $P \
    | jq -r '.getUtxosByAddressesResponse.entries[]
        | "\(.outpoint.transactionId):\(.outpoint.index // 0)\t\(.utxoEntry.amount)\t\(.utxoEntry.blockDaaScore)"' \
    | sort > "set$P.tsv"
  awk -F'\t' '{s+=$2} END{print "'"$P"' outpoints:", NR, "sum:", s}' "set$P.tsv"
done
diff set:42720.tsv set:42820.tsv | head

# is the disputed output present?
grep -c '^c4b85c94c0fb22b8c8ded09ba4c55aaf1f93f128974d8d5678b2072b6db7dfd2:0' set:42720.tsv set:42820.tsv
```

The pruning point was `dad52459d17a748a66e32ba4c25adc37cf74647dfeb497aaa464e04d216e2ffd` and
identical on both nodes when this was written. If it has moved, say so when reporting — the
comparison is only meaningful between nodes at the same pruning point.

---

## 5. Wipe policy

**Destroy a datadir only after all three of:**

1. a peer or datadir has classified **PASS** or **PASS_DEDUP**; and
2. `GetUtxosByAddresses` outpoint-set equality against that node — **including `blockDaaScore`
   per entry**, not just the sum; and
3. `GetCoinSupply` equality against that node.

A matching sum with a differing outpoint set is a two-sided error, which is exactly the shape
seen in the incident. The sum alone is not evidence.

**A cached "already checked" line does not satisfy (1).** See the memoisation gotcha in §1.

Until all three hold, every existing datadir is evidence. The divergence measured in the
incident was frozen — nothing differed within ~29,700 DAA of the tip — so there is no urgency
that justifies destroying it.

---

## 6. If C5 comes back empty: C1, and what blocks it

C1 is a replay from genesis on a fresh datadir, cutting a new pruning-point bucket from the
resulting virtual UTXO set. It is the fallback, not the plan, and it has two prerequisites that
are **not** addressed by Stage A:

**`--archival` is mandatory and cannot be retrofitted.** `pruningManager.deleteBlock` stages
`StatusHeaderOnly` and then, unless `isArchivalNode`, deletes `multiSetStore`,
`acceptanceDataStore`, `blocksStore`, `utxoDiffStore` and `daaBlocksStore` for every block below
the pruning point. A pruned node has no bodies to replay and the P2P protocol offers no way to
re-fetch them below the pruning point. Check before planning anything:

```bash
ps -o args= -p "$(pgrep -f 'HTND.*<your-port>')" | tr ' ' '\n' | grep -E 'archival|appdir'
```

**The ambient block-version leak must be fixed first — filed here, deliberately not fixed in
this PR.** `constants.GetBlockVersion()` is a process-global one-way ratchet, set from
`ibd.go` and `handle_relay_invs.go` (whose own comment describes it as such). Several consensus
paths read it instead of the block's own header version:

- `calculateAcceptedIDMerkleRoot` branches on `constants.GetBlockVersion() < 5` to decide
  whether to sort accepted transactions;
- `blockvalidator/block_body_in_isolation.go` indexes `maxBlockMass` by it;
- `consensusstatemanager/pick_virtual_parents.go` indexes `maxBlockParents` by it.

Replaying v1–v4 history in a process that has ratcheted to v9 therefore computes the wrong
accepted-ID merkle root and the wrong limits. `coinbasemanager` has already been converted to
take each block's own version (see its comments); these three call sites have not. **C1 is not
safe until they are.**
