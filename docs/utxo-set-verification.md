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
tools/c5survey/c5-survey.sh --peer 51.89.232.58:42421
```

**The script builds `htnd` and `htnctl` from the working tree and uses those.** The verdict
lines it greps for are the ones the current source emits; surveying with a stale binary from
`PATH` is the classic way to get a confident, wrong answer. It prints the built version and the
source commit — including whether the tree is dirty — before it starts:

```
Built from this tree:
  htnd    : .../bin/htnd  version htnd version 2.16.0-7ffecb391-dirty
  source  : 7ffecb391 (working tree has uncommitted changes)
```

Pass `--htnd PATH --htnctl PATH` to survey with prebuilt binaries instead; the output then says
plainly that they were **not** built from this tree.

Rules the script enforces, and why:

- **Isolated datadir, isolated p2p and RPC ports.** It never touches a production datadir.
- **`--connect` pins exactly one peer.** No DNS seeds, no discovery — so the imported set
  provably came from the peer being classified.
- **One peer per datadir, never reused.** A second import starts from state the first one
  already installed, so the answer would be about the datadir, not the peer.
- **`--utxoindex` is not enabled for classification and is not needed.** The pruning-point UTXO
  set is imported and checked before any index is built, so the verdict does not depend on it.
  `--compare` does enable it, because `GetCoinSupply` and `GetUtxosByAddresses` require it.
- **`--archival` is not needed.** C5 classifies the exporter, nothing more.
- **The datadir and log are always kept, especially on `FAIL`** — they are the evidence.

Exit codes: `0` PASS or PASS_DEDUP, `1` FAIL, `2` OTHER (timeout, refused, crash).

### Reading the result

A `PASS`/`PASS_DEDUP` peer is a candidate bootstrap source. A `FAIL` peer is one that will put
any node syncing from it into the offset regime — the exact mechanism observed in production,
where a public peer's set matched neither the accumulated multiset nor the header, the importing
node rewrote its trust anchor, and four checks went quiet within sixty seconds.

If **every** peer surveyed returns `FAIL`, C5 is empty — but that is *not yet* enough to
commit to C1. See the next section first.

---

## 3a. `--compare`: do two peers agree with each other?

A peer failing against its **own** pruning point header does not tell you whether two peers
agree with **each other**. That second question is the fork in the road, and a run of all-`FAIL`
classifications cannot answer it — the surveyed peers will generally be at different pruning
points with different entry counts.

```bash
tools/c5survey/c5-survey.sh --compare \
  --peer peer-a:42421 --peer peer-b:42421
```

This classifies both peers, then syncs both fully (`--utxoindex` is enabled in this mode, since
`GetCoinSupply` and `GetUtxosByAddresses` need it) and diffs the UTXO state they produced:
circulating supply, and the outpoint set for `--address` including `blockDaaScore` per entry.

Reading the verdict:

| result | meaning | what to do |
|---|---|---|
| **AGREE, different snapshots** | two independent derivations landed on the same set — real evidence the network agrees on state and the *commitment rule* is what diverged | look at how multisets are computed. An archival replay would reproduce the same disagreement |
| **AGREE, identical snapshots** | the peers handed over the same bytes — agreement by shared lineage, not independent confirmation | **proves nothing about correctness.** Survey peers with unrelated histories |
| **DISAGREE**, deep in history | state itself has diverged between peers | C1 is the only anchor, and the version leak in §6 becomes critical path |
| **DISAGREE**, only near the tip | ordinary lag: the two nodes are a few blocks apart | not evidence of anything; re-run when both are settled |

The script tells those first two apart for you. The classification line carries the snapshot's
fingerprint — how many entries arrived and what they hash to — and if both peers report the
same fingerprint, the run says so and refuses to treat the agreement as confirmation:

```
NOTE: both peers served an IDENTICAL pruning-point snapshot (15679214 entries,
      multiset 026c0ba9...).
      They did not independently arrive at the same state - they handed over the same
      bytes. Agreement below is therefore evidence of a shared export ancestor, NOT
      evidence that the state is correct.
```

That case exits non-zero, like a disagreement, because it has not answered the question.

### Fingerprints and shared export lineage

Every classification now prints the snapshot's fingerprint and appends it to
`<workdir>/fingerprints.tsv`, which accumulates across invocations:

```
        fingerprint:
          pruningPoint     f70e65ba9b4a84332efa1afdecde08ccc9eac497315d543f30f1f5ecb0ff99ec
          entries          15679214
          bucketMultiset   026c0ba996ba47337bf9ddfb8aee47034e8b15aa2c17aae786dbe1c8ce941af9
          headerCommitment 5cb21ed4e4acbae5d86244b84ee75b8586f15cae0b4542e3405257c51a8d01b6
```

If those exact bytes were already recorded from a **different** peer, the run says so:

```
        SHARED_EXPORT: these exact bytes were already recorded from explorer-one.hoosat.fi:42421.
                       This peer is another copy of that export, not new evidence.
```

Re-running the *same* peer is not flagged — the ledger excludes the peer being surveyed.

`--require-independence` turns SHARED_EXPORT into exit code 3 in single-peer mode, so a
scripted sweep stops when it starts finding copies. In `--compare`, independence is required
by default and a shared snapshot exits non-zero; `--allow-shared-lineage` relaxes that when
you deliberately want the comparison anyway.

**Stop a survey run as soon as the fingerprint matches one you already have.** It is another
copy of the same export and will only produce another FAIL.

### The monotonicity check

Every `--compare` run prints its circulating supply and virtual DAA score. **Circulating
supply is coinbase-only and never decreases**, so a later measurement reporting *less* supply
than an earlier one proves one of the two is wrong — without needing to know which set is
right, and regardless of how well any two peers agree with each other.

Record the pair from every run. It is the cheapest check available and nothing was running it.

The report separates those last two for you: it counts how many differing outpoints sit more
than 10,000 DAA below the tip, prints their DAA range, and splits them coinbase vs regular.
Divergence deep in history is the signature that matters — in the incident that motivated this
work it was 163 outpoints spanning ~254,000 DAA, none within ~29,700 DAA of the tip, all
non-coinbase.

### Why not just measure the offset directly?

The obvious test is "is the offset a constant delta?" — which is exactly what
`blockInheritsKnownUTXOCommitmentOffset`'s comment asserts ("because MuHash is homomorphic,
that fixed offset propagates verbatim to every descendant") and never checks.

**It is not computable.** Block headers carry only the MuHash *hash*, and hashes are not
invertible, so one multiset cannot be subtracted from another. That assertion has been
load-bearing for the entire toleration regime and cannot be verified as written. Comparing the
sets two peers actually serve is the computable substitute, which is why `--compare` exists.

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

## 6. What GetCoinSupply actually is, and why the supply drop is not issuance

`HandleGetCoinSupply` returns `UTXOIndex.GetCirculatingSompiSupply()`, which reads one stored
counter (`utxo-index-circulating-supply`, `domain/utxoindex/store.go:827`). That counter is the
running **sum of amounts in the UTXO index** — reset to 0 and re-accumulated over the virtual
UTXO set by `UTXOIndex.Reset()`, then moved by `toAdd − toRemove` on every update.

**It is not minted supply.** It is the current unspent total, so it is not monotonic by
definition — only by argument: spending is value-preserving, and each accepted chain block adds
its subsidy, so in the absence of value destruction the total only grows.

There is exactly one value-destroying path in this fork, and it is not an explicit burn:
`calcMergedBlockReward` (`coinbasemanager.go`) returns `0, nil` when the merged block is not in
`mergingBlockDAAAddedBlocksSet`, so that block's subsidy *and the fees of its accepted
transactions* are never paid to any coinbase. Fees leave the UTXO set when the transaction is
accepted; if no coinbase re-mints them, they are gone. `grep -i burn` over `domain/` finds
nothing else.

### The bound

Mainnet block version 9 uses `TargetTimePerBlock[8] = 200ms` (`dagconfig/params.go`), so
`calcDeflationaryPeriodBlockSubsidy` gives `blocksPerYear = 31,557,600 / 0.2 = 157,788,000` and
lands in deflationary year 2 at both DAA 221,433,570 and 221,482,353:
`subsidyByDeflationaryYearTable[2] × 0.2 = 1,333,333,333 sompi` (13.3333 HTN) per block.

Over the 48,783 DAA between the two measurements (~2.71 h at 5 BPS):

| assumption | upper bound on new issuance | vs the observed 25,774,304,039,058,493 |
|---|---:|---:|
| 1 block per DAA, actual subsidy | 65,043,999,983,739 | 396× short |
| 1 block per DAA, max subsidy ever (`table[0]`) | 97,566,000,000,000 | 264× short |
| 5 blocks per DAA, actual subsidy | 325,219,999,918,695 | 79× short |
| 5 blocks per DAA, max subsidy ever | 487,830,000,000,000 | **53× short** |

DAA score increments by the count of DAA-added blocks, so "1 block per DAA" is the honest
figure; the 5× rows exist only to show the conclusion survives a deliberately absurd
over-count.

**Verdict: issuance cannot explain the gap, and neither can burned fees** — that would require
257.7 M HTN, 3.7% of supply, to be paid in fees and destroyed inside 2.71 hours. The old pair
was inflated, the fresh pair dropped coins, or both. The `--compare` monotonicity line exists to
make this check automatic.

---

## 7. Can any peer still export the old pruning point?

**No.** `pruningStore.UpdatePruningPointUTXOSet` (`pruning_store.go:157`) applies the
previous-to-current diff **in place** to the single `pruning-point-utxo-set` bucket: deletes for
`ToRemove`, puts for `ToAdd`, one bucket, no per-pruning-point versioning. Once a node advances
to a new pruning point, the previous set is gone from that node.

Serving is pinned to the current one too. `consensus.GetPruningPointUTXOs`
(`consensus.go:706`) reads the current pruning point and returns `ErrWrongPruningPointHash`
unless the requested hash equals it:

```go
if !expectedPruningPointHash.Equal(pruningPointHash) {
    return nil, errors.Wrapf(ruleerrors.ErrWrongPruningPointHash, ...)
}
```

`--archival` does not change this — `deleteBlock` preserves *blocks*, not the bucket.

**Implication.** Every seed that has advanced past `dad52459…` can only serve the current
lineage. No survey, however many peers it covers, can ever produce an independent snapshot of
`dad52459…`, so **C5 cannot validate the old production pair in place**. What C5 can still do is
sort today's exporters into "another copy of `026c0ba9…`" versus "something else at the current
pruning point" — worth knowing, but it cannot by itself establish that any set is correct.

A header-matching set that is not simply another copy of `026c0ba9…` therefore has to be
*derived*, not fetched. That is C1.

---

## 8. C1 readiness (documentation only — no C1 implementation)

### `--archival` is mandatory and cannot be retrofitted

`pruningManager.deleteBlock` (`pruningmanager.go:584`) stages `StatusHeaderOnly` and then,
unless `pm.isArchivalNode`, deletes `multiSetStore`, `acceptanceDataStore`, `blocksStore`,
`utxoDiffStore` and `daaBlocksStore` for every block below the pruning point. A pruned node has
no bodies to replay, and the P2P protocol offers no way to re-fetch bodies below the pruning
point. Check before planning anything:

```bash
ps -o args= -p "$(pgrep -f 'HTND.*<your-port>')" | tr ' ' '\n' | grep -E 'archival|appdir'
```

### The ambient block-version leak — confirmed present at HEAD

`constants.GetBlockVersion()` is a process-global one-way ratchet set from `ibd.go` and
`handle_relay_invs.go`. Three consensus paths read it instead of the block's own header version,
all still present:

| site | code |
|---|---|
| `consensusstatemanager/verify_and_build_utxo.go:359` | `if constants.GetBlockVersion() < 5 { sort.Slice(acceptedTransactions, ...) }` |
| `blockvalidator/block_body_in_isolation.go:231` | `mass > v.maxBlockMass[constants.GetBlockVersion()-1]` |
| `consensusstatemanager/pick_virtual_parents.go:43,51,53,65` | `csm.maxBlockParents[constants.GetBlockVersion()-1]` |

Replaying v1–v4 history in a process ratcheted to v9 computes the wrong accepted-ID merkle root
and the wrong limits. `coinbasemanager` was already converted to take each block's own version
(see its comments at `:104` and `:294`, and `payload.go:106/135/169`); these have not been.

**Not fixed here, and not a one-liner.** The merkle-root site needs the version of the block
whose acceptance data is being hashed, which `calculateAcceptedIDMerkleRoot` does not currently
receive; `pick_virtual_parents` runs for the *virtual*, which has no header at all, so "use
`header.Version()`" has no obvious answer there. Each needs its own decision, and changing them
changes which blocks are accepted — that must not ride along with a UTXO-accounting fix.

### What "header-matching" would mean for C1

Walk block bodies from genesis using **each block's own header version** rather than the ambient
ratchet; accumulate the MuHash with `utxo.SerializeUTXO` as blocks are applied; at each pruning
point compare the accumulated value to that pruning point header's `UTXOCommitment()`; and only
if it matches, persist the resulting set as the served bucket and mark the Stage A marker
`verified`. The first pruning point where it does *not* match localises when the network's
committed rule and the current code diverged — which is the actual question underneath all of
this.

### Is genesis→current archival replay an operator path today?

**No — it would be new code.** Nothing in the tree replays from genesis. `RecoverUTXOIfRequired`
resumes an interrupted pruning-point import; `repairPruningPointUTXOSet` rebuilds the bucket from
`RestorePastUTXOSetIterator`, which is the diff-chain walk already observed to disagree with both
the bucket and the header; `updatePruningPoint` derives one pruning-point-to-pruning-point diff.
A node started on an empty archival datadir still performs headers-proof IBD and imports a
pruning-point UTXO set from a peer — i.e. it inherits whatever lineage that peer has, which is
exactly what C1 is meant to avoid. Making C1 real means a mode that declines the pruning-point
import and applies bodies forward from genesis, plus the version-leak fixes above. That is a
project, not a flag.
