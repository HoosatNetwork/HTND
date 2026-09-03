# C1 design: deriving a header-matching UTXO set

Design only. No implementation, no consensus-rule change, no Stage B/C.

**C1 cannot run against the live network today.** It requires an archival datadir already on
disk that carries genesis-anchored history, or new requester code *plus* a peer that has both
genesis-anchored headers and pre-pruning-point bodies — and no such peer has been observed.
`--archival` on a *new* node does not help: an empty archival datadir still performs
headers-proof IBD and imports a peer's pruning-point UTXO set, inheriting exactly the lineage
C1 exists to escape. Section 0 gives the evidence.

---

## Why C1 is now required rather than contingent

The previous C5 run compared two peers that had served a byte-identical snapshot, so its
agreement proved nothing. The run of 2026-09-02T18:21Z did not have that problem — the
fingerprint ledger records two genuinely different imports:

| peer | pruning point | entries | bucket multiset |
|---|---|---:|---|
| explorer-two.hoosat.fi:42421 | `1db42d81…` | 16,499,221 | `fa2435ef…` |
| 192.168.1.170:42421 | `f70e65ba…` | 15,679,214 | `026c0ba9…` |

Both synced on to the **same** pruning point `fb6e3257…`, and then disagreed:

- circulating supply differs by **10,063,470,569,211 sompi (~100,634.7 HTN)**
- for one address: 511 outpoints only on node0, 287 only on node1, 0 shared entries differing
- **796 differing outpoints spanning DAA 219,576,883 … 221,497,047** — up to 1.95 M DAA below
  the tip, all non-coinbase

The virtual DAA gap between the two nodes was 1,358 (~4.5 minutes at 5 BPS), which cannot
explain divergence reaching almost two million DAA back.

**Two independently-sourced nodes, same pruning point, disagreeing deep in history.** The
network does not agree on UTXO state. There is no snapshot to adopt, so a header-matching set
has to be derived. The three remaining C5 targets are no longer needed to settle the question;
they would only add data points.

---

## 0. Can C1 obtain its inputs?

| | input | status | evidence |
|---|---|---|---|
| **H1** | historical headers to genesis, from any seed | **EXISTS-BUT-BROKEN** | `handle_request_headers.go:44-95` will serve any range, and `deleteBlock` (`pruningmanager.go:598-608`) never deletes `blockHeaderStore` — so a node that *once had* genesis-anchored headers keeps them. But a node bootstrapped by headers proof never had them: `syncPruningPointFutureHeaders` (`ibd.go:610-622`) requests headers only from `highestKnownSyncerChainHash` upward. Every surveyed peer bootstrapped that way. |
| **H2** | historical bodies to genesis, from an archival seed | **EXISTS-BUT-BROKEN** | Serve side is unrestricted: `HandleIBDBlockRequests` (`handle_ibd_block_requests.go:52-56`) answers any hash via `GetBlock`, with no pruning-point check. Request side does not exist: `missingBlockBodyHashes` (`syncmanager/antipast.go:450-468`) anchors at the pruning point. The wire format supports it; nothing asks. |
| **H3** | historical bodies to genesis, from a pruned seed | **DOES-NOT-EXIST — and fails silently** | On a `GetBlock` miss the handler falls back to `GetBlockEvenIfHeaderOnly` (`handle_ibd_block_requests.go:58-64`), which returns `&externalapi.DomainBlock{Header: header}` (`consensus.go:513`) — **a block with no transactions**, sent as an ordinary `MsgIBDBlock`. No error is raised. |
| **H4** | a blocks+headers checkpoint that is not a UTXO bucket | **DOES-NOT-EXIST** | No export/import tooling in the tree. The only bulk transfer is `domain.migrate()` (`domain/migrate.go:11`, called from `domain/domain.go:218`), which copies blocks between two in-process consensus instances **and then imports the pruning-point UTXO set** (`migrate.go:194-199`) — reintroducing the lineage. Not operator-reachable, and not blocks-only. |

**H3 is the trap worth repeating.** Asking a pruned peer for a pre-pruning-point body does not
error; it returns a header wearing a block's clothes. Any C1 fetcher must reject a block whose
`Transactions` is empty when a body was expected, or it will silently replay an empty chain and
produce a confidently wrong answer.

Since H2's request side does not exist and H4 does not exist, **C1's input must come from disk**:
an archival datadir that has been resolving blocks since before the horizon of interest.

---

## 1. What "derived" means

### The accumulator

Start from an **empty** MuHash. Confirmed correct: mainnet genesis commits
`muhash.EmptyMuHashHash` (`dagconfig/genesis.go:70`), and `consensus_state_manager.go:178` stages
`multiset.New()` for the genesis hash — genesis's own coinbase is deliberately *not* in the UTXO
set.

### The walk — this is the part that is easy to get wrong

Header commitments correspond to `calculateMultiset` (`multisets.go:14`), which is:

> the **selected-parent chain**, and at each chain block the **entire merge set's accepted
> transactions**.

`multisets.go:38` seeds from the selected parent's multiset, then iterates `acceptanceData` —
which holds one `BlockAcceptanceData` per merge-set block, built by `applyMergeSetBlocks` over
`GetSortedMergeSet(blockHash)`. Each accepted transaction is folded in with
`addTransactionToMultiset`, stamped with its *creating* block's DAA score.

So C1 iterates chain blocks in selected-parent order, and for each one applies every accepted
transaction across its whole merge set, in merge-set order. **A selected-parent-only walk will
not match header commitments** — it would omit every transaction accepted from a merged block,
which is most of them.

C1 maintains two things in lockstep over that walk: the MuHash, and a real outpoint→entry map.
The map is the deliverable; the MuHash is what gets compared to headers. Because both are driven
by the same accepted-transaction stream, the map hashes to the MuHash by construction — which is
precisely what the served bucket has never been able to guarantee.

### Outputs, in order of preference

1. **A pruning-point bucket at the current PP whose MuHash equals `header.UTXOCommitment()`**,
   plus the Stage A marker set to `verified`.
2. **A rebuilt virtual UTXO set and index counter** consistent with that bucket after applying
   PP→virtual.
3. **The first pruning point whose commitment diverges**, with its DAA score and hash — the
   corruption horizon.

**A C1 run that cannot name the first diverging pruning point is incomplete**, and (iii) is the
output that survives even total failure of (i) and (ii). It is the thing that tells us whether
the network's committed rule and the current code parted company at a specific height, or were
never in agreement.

---

## 2. Version and rule selection during replay

Replay must never read `constants.GetBlockVersion()` — it is a process-global one-way ratchet
that will be at 9 while replaying v1 blocks. Replay-only sources:

| leak site | replay-only version source |
|---|---|
| `verify_and_build_utxo.go:359` — `calculateAcceptedIDMerkleRoot`'s `< 5` sort | version of the block **whose acceptance data is being hashed**, threaded in as a parameter. The function does not receive it today; C1's own copy takes it explicitly. |
| `block_body_in_isolation.go:231` — `maxBlockMass[...]` | version of the block **being validated**, from its own header. |
| `pick_virtual_parents.go:43,51,53,65` — `maxBlockParents[...]` | **not needed.** See below. |

### `pick_virtual_parents` — the "hard one" dissolves

It only chooses the parents of the **virtual**. Historical chain blocks do not need it: their
merge sets come from their own headers' parent lists plus GhostDAG, and GhostDAG is a pure
function of the header DAG and K. C1's historical walk therefore never calls
`pickVirtualParents` at all, and cannot silently change which parents live consensus would pick.

It is needed exactly once, at the very end, if C1 goes on to produce output (ii) and set a
virtual — and at that point the ambient version *is* the current version, so the live rule is
already correct. **No change to live consensus, and no replay-only override, is required here.**

---

## 3. Product: UTXO + acceptance replay over a fixed header DAG

Two candidate products:

- **Full re-validation** — recompute topology, GhostDAG, acceptance and UTXO from bodies.
- **UTXO-replay over stored DAG decisions** — reuse the archival node's stored acceptance data
  and only rebuild the UTXO set and multiset.

**Recommendation: replay acceptance and UTXO over a *fixed header DAG*** — that is, take
topology and GhostDAG as given by the stored headers (they are derivable from headers alone and
were never the fault), but **re-derive acceptance from bodies rather than trusting stored
acceptance data.**

This is a deliberate amendment to the suggested product, for one reason. Stored acceptance data
was produced by `maybeAcceptTransaction`, which swallows errors into "not accepted"
(`calculate_past_utxo.go:362-365`, `:407-412`) and by `validateBlockTransactionsAgainstPastUTXO`,
which skips transactions with missing inputs whenever the offset flag is latched. On a node that
spent months in that regime, acceptance data is one of the contaminated artifacts. Replaying it
verbatim would faithfully reproduce the contamination and tell us nothing.

Re-deriving acceptance is also cheaper than it sounds, because it does *not* require
re-validating topology: parents come from headers, GhostDAG is deterministic, and the only work
is walking each chain block's merge set and deciding acceptance against the UTXO map we are
already maintaining. What we are declining to re-derive is the DAG shape — which is right,
because the fault under investigation is a UTXO/diff-algebra fault, not a GhostDAG fault.

If stored acceptance data *is* present and trusted for some prefix, C1 may use it there as a
fast path; the divergence horizon (output iii) is what tells you where trust ends.

---

## 4. Store copy / wipe list

C1 runs against a copy of an archival datadir. Copying the wrong store silently reintroduces
`026c0ba9…` and invalidates the whole exercise.

**Copy (inputs):**

| store | why |
|---|---|
| `blockStore` | the bodies — the only irreplaceable input |
| `blockHeaderStore` | topology, versions, and every `UTXOCommitment` C1 checks against |
| `ghostdagDataStore` | merge sets and selected parents; derivable from headers, copied to save time |
| `reachabilityDataStore` | supports the topology queries |
| `daaBlocksStore` | DAA scores, which are stamped into UTXO entries |
| pruning-point **index** (`pruning-block-index`, `pruning-point-by-index`) | which hashes were pruning points, so C1 knows where to compare |
| `acceptanceDataStore` | optional fast-path input only, never trusted past the divergence horizon |

**Wipe — never copy:**

| store | why |
|---|---|
| `consensusStateStore` `virtual-utxo-set` | the drifted virtual set |
| `pruningStore` `pruning-point-utxo-set` | the served bucket; this *is* `026c0ba9…` |
| `utxoDiffStore` | the diff chain, already shown to disagree with both bucket and header |
| `multisetStore` | the incremental chain rooted at a rewritten anchor |
| `utxo-index` (whole database) | a projection of the above, including the supply counter |
| `pruning-utxo-verified` (Stage A marker) | a verdict about a bucket C1 is discarding |
| `imported-pruning-point-utxos`, `imported-pruning-point-multiset` | import residue |

---

## 5. Success criteria and stop conditions

**Success:** at the current pruning point, derived MuHash equals `header.UTXOCommitment()`; the
derived set is persisted as the bucket; the Stage A marker reads `verified`; `UTXOIndex.Reset()`
rebuilt from that virtual; and `GetCoinSupply` equals the derived sum.

**Stop and report — do not continue past any of these:**

1. **First pruning-point commitment mismatch.** Report PP hash, DAA score, expected vs derived.
   This is output (iii) and is the run's most valuable result even when everything else fails.
2. **Missing body** — including a block that arrived or loaded with empty `Transactions` where a
   body was expected (the H3 trap).
3. **`AddTransaction` drops a non-coinbase output** — the P2b condition Stage A already detects
   and logs; in C1 it is fatal, not a warning.
4. **`calcMergedBlockReward` returns 0** for a block the header path expected to mint, i.e. a
   merged block outside `mergingBlockDAAAddedBlocksSet` whose subsidy the coinbase nonetheless
   paid. That is the one value-destroying path in this fork and it must not pass silently.

### Cost, measured rather than guessed

From the 2026-09-02T18:21Z run with the current binary against a real peer:

- headers-proof IBD end to end: **66 minutes** (21:21:23 → 22:27:23), including an 8-minute
  transfer of a 15,679,214-entry UTXO set and 243,661 bodies
- peak body insertion, `updateVirtual=false`: **~2,500 blocks/s**
- body application including UTXO resolution: **~451 blocks/s**
- datadir: **4.5 GB** for 194,326 retained blocks plus the UTXO set (~14.6 KB/block)

The chain to replay is ~221.5 M blocks (virtual DAA 221,525,376):

| | estimate |
|---|---|
| C1 at insert-only peak (a floor C1 can never reach — it does no UTXO work) | **~25 hours** |
| C1 at the measured resolve rate | **~5.7 days** |
| archival block store required | **~3 TB** |

**Order of magnitude: days of compute and terabytes of disk, against 66 minutes for a normal
sync.** That is a factor of ~100 in time and is the single biggest practical argument for
exhausting every alternative — including the three remaining C5 targets — before committing to
C1.

---

## 6. Operator sketch (shape only — no implementation)

```
# 1. Snapshot an archival datadir. Copy inputs only; see section 4.
htnd-c1 prepare \
    --from /mnt/archival/.htnd/hoosat-mainnet \
    --to   /mnt/c1/work \
    --wipe-derived                     # drops virtual, bucket, diffs, multisets, index

# 2. Replay. Resumable, because it will run for days.
htnd-c1 derive \
    --datadir /mnt/c1/work \
    --from-genesis \
    --stop-on-first-divergence \       # output (iii)
    --checkpoint-every 1000000

# 3. Report without writing anything.
htnd-c1 verify --datadir /mnt/c1/work --dry-run

# 4. Only if step 2 reached the current pruning point with a match.
htnd-c1 commit --datadir /mnt/c1/work --set-verified-marker
```

`derive` never opens a network socket. `commit` is the only step that writes a bucket or the
Stage A marker, and refuses unless the derived MuHash equals the pruning point header's
commitment.

---

## Footer: this does not unblock Stage B

Stage B (refusing to export an unverified bucket) and Stage C (refusing to import one, deleting
the offset latch) **remain blocked until a successful C1 node exists.** Until then:

- `GetPruningPointUTXOs` must keep serving whatever bucket a node has. Every node on the network
  currently fails the check; refusing to export today partitions IBD entirely.
- The Stage A marker stays observation-only. It becomes meaningful the moment one node can set it
  to `verified` from a derived set rather than a copied one.
