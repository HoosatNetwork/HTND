# Runbook: blocks disqualified for a UTXO commitment mismatch

**Symptom.** A node marks blocks `StatusDisqualifiedFromChain` and stops advancing, while other
nodes accept the very same blocks and the very same transactions. Often phrased as: *"why does my
node go into disqualified mode even though everyone else accepts this transaction?"*

**The block is almost certainly fine. The node's starting UTXO snapshot is not.**

This is HTN-002. It is a **known, deliberately unfixed condition** — the tolerance that papers over
it is load-bearing on today's mainnet, and removing it is a coordinated network decision, not an
operator action. This runbook explains how to identify which side of the split you are on, what you
can safely do, and what you must not do.

---

## First: which baseline is this node on?

```sh
htnctl get-info
```

Look at `isUtxoSetVerified`:

- **`true`** — this node's pruning-point UTXO set hashes to the commitment it is supposed to hash
  to. It is a **strict** node. It will disqualify blocks mined by tolerant nodes.
- **`false`** — this node is on an **offset baseline**. It tolerates per-block commitment mismatches
  and will accept blocks that strict nodes reject.

Confirm in the log:

```sh
docker logs htnd 2>&1 | grep -i "offset UTXO baseline\|does not match its own header"
```

The two lines that matter:

```
Imported pruning point <hash> UTXO set does not match its own header
this node is on an offset UTXO baseline; inherited per-block commitment mismatches are tolerated
```

---

## Mechanism

MuHash is **homomorphic**. An offset introduced at import carries unchanged into every block
resolved forward from it.

1. A node imports a pruning-point UTXO set that does not match its header commitment. It is accepted
   anyway, because refusing is off by default
   (`refuseMismatchedImportedPruningPointUTXOSet`, wired to the hidden
   `--enable-sanity-check-pruning-utxo`).
2. The node repairs its trust anchor to its own recomputed multiset and continues.
3. From then on, every block it resolves recomputes a UTXO commitment that differs from what the
   block's miner committed to, by exactly that offset.
4. On a **strict** node, `validateUTXOCommitment` raises a `RuleError`, and
   `resolveSingleBlockStatus` turns that into `StatusDisqualifiedFromChain`.
5. Disqualification then **cascades by inheritance** to every descendant without any of them being
   individually checked — which is why a whole segment of chain presents as a single failure.

A node whose baseline is correct recomputes the same commitment the miner did and accepts the same
block. So the identical block is valid on one node and disqualified on another, with no insert error
on either, purely because of where each node's UTXO snapshot started.

### It spreads

A node on an offset baseline serves that baseline onward:

```
the UTXO set this node now serves does NOT match the chain's commitment for it.
Every peer that syncs from this node inherits this set, gap included.
```

Any peer syncing from it inherits the offset. That is HTN-005's half of the loop, and it means the
population of tolerant nodes grows over time rather than shrinking.

### Measured reality

This is not hypothetical, and not rare. On a mainnet node, forensics over a clean-shutdown datadir
copy found the served pruning-point bucket (21,679,714 entries) hashing to the stored multiset
`1437ecba…` while the header commits to `dc0a1e17…`. A later live occurrence recomputed a fresh
multiset over 22,812,225 deduplicated entries as `ce5ebf4c…` against a header commitment of
`35b9c0b4…` — confirming the served set is genuinely incomplete, not a double-counted chunk.

It has also been reproduced deterministically: a syncee that imports a pruning-point set with **one
UTXO changed by one sompi** accepts it, reports an offset baseline, and then mines blocks that are
`Valid` on itself and `DisqualifiedFromChain` on a strict genesis-synced node.

---

## Establishing exactly what your node holds

`isUtxoSetVerified` gives you a yes/no. When you need to compare your set against someone else's —
which is the only way to tell "everyone has the same offset" from "each node has a different one" —
use the canonical artefact tool on a **copy** of a cleanly shut down datadir:

```sh
# Never against a live node's directory: pebble replays its WAL on open.
utxoforensics -db /path/to/datadir-COPY -canonical -canonical-out utxo-set.bin
```

It prints the entry count, the MuHash, and the SHA-256 of a canonical encoding, and says whether the
set matches the pruning point's header commitment:

```
  entries:       22812225
  muhash:        ce5ebf4c...
  encoding-sha256: 9f3a...
  => DOES NOT match the pruning point's header commitment
```

The output is deterministic: two people on two machines, from copies of the same datadir, get
byte-identical values, so a mismatch is a real difference rather than a difference in method. The
MuHash is computed with the same serialization consensus uses, so it is directly comparable with a
header's `UTXOCommitment`; the encoding hash is reproducible by anyone with `sha256sum` against the
written file, without running the tool.

The tool deliberately does **not** decide whether the historical header commitment or a recomputed
one is authoritative. That is the rebaseline decision described below.

## What you can do

### If this node is strict and is disqualifying the network's chain

You are the minority. The chain the rest of the network is building is, from your node's point of
view, committing to the wrong UTXO state — and your node is right, but alone.

Practical options:

- **Resync from a peer.** You will most likely inherit the offset baseline and rejoin consensus with
  everyone else. This is what most operators end up doing. It is not a fix; it is joining the
  majority.
- **Stay strict and stop serving.** Correct, and currently useless for following the chain.

Do **not** interpret a strict node's disqualifications as evidence of an attack or of bad
transactions. The transactions are fine.

### If this node is tolerant

Nothing to do. It is in the same state as the rest of the network. Record `isUtxoSetVerified` so the
split can be measured — sizing that split across mainnet is the input the real fix is waiting on.

---

## What not to do

**Do not enable `--enable-sanity-check-pruning-utxo`.** It makes the node refuse a mismatched
imported set. Since no node currently serves a commitment-matching pruning-point set, this does not
make your node correct — it makes it unable to sync at all. This was explicitly decided: *"the
import commitment check cannot be turned back on — no node serves a correct (commitment-matching)
pruning point set."*

**Do not use `--repair-block-statuses` to clear disqualifications.** It will appear to work, because
it re-marks every block that is neither invalid nor header-only as UTXO-valid. It does not fix the
baseline, and it leaves blocks with no stored multiset — which breaks block template building
entirely. See [stratum-zero-hashrate.md](stratum-zero-hashrate.md) for the outage that caused.

**Do not "fix" the tolerance in code.** Removing it disqualifies the chain of every node currently
running on an offset baseline, which is most of them.

---

## The actual fix, and why it is not here

The honest fix is a **coordinated rebaseline**: ship a checkpointed pruning point and its UTXO
commitment in a release, so every node starts from the same, agreed snapshot, and then make validity
independent of local baseline health.

That requires choosing the checkpoint block and deciding whether the historical header commitment or
a newly computed one is authoritative. Both are maintainer decisions and neither can be invented by
an operator or a patch.

The consensus-side groundwork is gated and dormant on this branch (Workstream C): strict UTXO
commitment enforcement and refusing mismatched imports are implemented as version-keyed predicates
that are **off for every block version that exists today**, so they change nothing until an
activation score is chosen by the release captain.

---

## Related

- **HTN-005** — the serving half: a node that inherited an offset serves it onward.
- **HTN-004** — the transaction-level mechanism by which tolerant nodes with *different* offsets
  drift apart from each other.
- **HTN-208** — a block above an imported pruning point being disqualified by a mismatch the
  toleration could not recognise. Fixed.
