# Exodus pruning point candidate tooling (`htnexodus`)

> Status: experimental. On `GhostDAG`, `htnexodus` currently ports only the **read-only**
> commands: `create`, `verify`, and `diff`. It does **not** change any consensus rules and does
> **not** ship, embed, or require any checkpoint in the node binary or `dagconfig`. The
> state-mutating `import` command is deliberately **not yet ported on this branch**.

## Background

Mainnet has had repeated chain stalls that have left some outpoints unrecoverable, making the
locally-calculated pruning-point UTXO set unreliable on some nodes. An "exodus pruning point" is
a proposed remedy: a manually authored, community-vetted UTXO set checkpoint at a specific
DAA score/block.

Before that can happen, the community needs a way to:

1. generate a candidate bundle from any node's own synced state,
2. verify a candidate bundle is internally self-consistent, and
3. diff two candidates (or a candidate against a live node) to find and reconcile
   disagreements between independently-run nodes.

That is what the read-only part of `htnexodus` (`cmd/htnexodus`) does.

## Building

```sh
go build -o htnexodus ./cmd/htnexodus
```

## Bundle format

A candidate bundle is a plain directory:

```
<bundle-dir>/
  manifest.json
  chunks/
    00000000.chunk
    00000001.chunk
    ...
```

- `manifest.json` records the target block hash/DAA score, the computed UTXO set commitment
  (same multiset construction the node itself uses to validate a pruning point - see
  `pruningmanager.validateUTXOSetFitsCommitment`), tool/node version, generation timestamp, an
  optional free-text operator note, and, for every chunk, its entry count and SHA-256 digest.
- Each `.chunk` file is a flat sequence of length-prefixed records:
  `[4 bytes little-endian length N][N bytes: utxo.SerializeUTXO(entry, outpoint)]`, i.e. the
  exact same per-entry byte layout the node already uses when computing UTXO commitments, so a
  chunk's bytes can be fed straight into a fresh multiset without any extra parsing.

A bespoke chunked binary format was chosen because the artifact is fundamentally
write-once/read-a-few-times: no compaction or extra file-count overhead, trivial to hash
chunk-by-chunk and distribute as plain files, and sequential streaming reads/writes are all that
`exodus create` and `exodus verify`/`diff` need.

`exodus create` is resumable: since the underlying UTXO iteration always restarts from the
beginning of the requested historical block's UTXO set (there is no cursor-resume for an
arbitrary past block), a resumed run re-derives every entry, but recognizes chunks from a
previous, interrupted attempt whose SHA-256 digest still matches the manifest and skips
re-writing them, only paying the disk write/hash cost for chunk data not already durably
persisted.

See the package doc comment in `domain/exodus/bundle.go` for the full authoritative
description of the format.

## Usage

The node must be stopped before running `htnexodus create` or `htnexodus diff --live` against its
database directory - both would otherwise contend with a running node for the same database files.
`htnexodus verify` and a `htnexodus diff` between two already-generated bundles do not touch a
node's database at all and can be run at any time.

## Generate a candidate

By block hash:

```sh
./htnexodus create \
  --db-path ~/.htnd/hoosat-mainnet/datadir2 \
  --network mainnet \
  --block <hex-block-hash> \
  --out ./candidate-2025-09 \
  --note "operator: alice, rationale: last known-good DAA score before the August stall"
```

By DAA score (resolved by walking the selected parent chain from the tip; the requested score
must fall between the node's local pruning point and its current tip):

```sh
./htnexodus create \
  --db-path ~/.htnd/hoosat-mainnet/datadir2 \
  --network mainnet \
  --daa-score 123456789 \
  --out ./candidate-2025-09
```

If the requested DAA score is older than the node's local pruning point, `create` fails fast with
a clear error naming the pruning point's own DAA score and hash, rather than walking all the way
back and failing with a confusing low-level "block header does not exist" error once it reaches
the pruning boundary.

### Which UTXO set a candidate is built from

`--source` selects the derivation, and it defaults to `acceptance-data`:

- **`acceptance-data`** rebuilds the set as the pruning point's UTXO set plus every accepted
  transaction between the pruning point and the target block, taken from the acceptance data the
  node recorded when it resolved each of those blocks. This is what the per-block multiset chain -
  the thing block headers commit to - is computed from, so it is the derivation that reproduces
  header UTXO commitments.
- **`materialised`** reads virtual's materialised UTXO table through the stored UTXO-diff chain.

The two are meant to be the same set. In practice they are not: the materialised table is
maintained by applying UTXO diffs and is never recomputed, so a diff that was mis-applied stays
mis-applied indefinitely. Use `materialised` only to compare the two against each other.

### The header commitment check

`create` compares the bundle's commitment against the target block's own header UTXO commitment
and **fails if they differ**. That commitment sits in a block that was mined, propagated and
accepted, so it is the one value here that the network agreed on; a bundle hashing to anything
else is not the UTXO set this chain committed to at that block.

If the check fails, the bundle is still written (so it can be diffed) but `create` exits non-zero
and says not to publish it. Retry with `--source acceptance-data` if it was built from the
materialised table; if it already was, this node's own pruning point set is offset from what the
chain committed to and no bundle derived from it can be trusted. `--allow-commitment-mismatch`
downgrades the failure to a warning, and exists only for producing an artifact for comparison.

## Verify a candidate bundle

```sh
./htnexodus verify --bundle ./candidate-2025-09
```

Re-reads every chunk, checks its SHA-256 digest and entry count against the manifest, recomputes
the multiset commitment from the entries, and compares it against the manifest's claimed
commitment.

That is self-consistency only: it proves the chunks match the bundle's own manifest and says
nothing about whether the bundle is the set the chain committed to. Pass `--db-path` (pointing at
a stopped node that has the target block's header) to additionally check the recomputed commitment
against that block's own header UTXO commitment:

```sh
./htnexodus verify --bundle ./candidate-2025-09 \
  --db-path ~/.htnd/hoosat-mainnet/datadir2 --network mainnet
```

A bundle that is internally consistent but does not match the header commitment fails verification.

## Diff two candidates

```sh
./htnexodus diff --bundle-a ./candidate-2025-09 --bundle-b ./candidate-from-bob
```

## Diff a candidate against a live node recomputation

```sh
./htnexodus diff --bundle-a ./candidate-2025-09 --live \
  --db-path ~/.htnd/hoosat-mainnet/datadir2 --network mainnet
```

`--source` applies here too. Diffing one node's two derivations against each other -
`create --source materialised` then `diff --live --source acceptance-data` - localises that node's
own drift and names the outpoints involved.

`diff` reports counts for both sides, outpoints present in only one side (with aggregate sompi
value), and outpoints present in both but with a differing entry (amount, script, DAA score, or
coinbase flag), which is intended to be the primary tool for reconciling disagreements between
independently-run trusted nodes.

## Not yet ported on `GhostDAG`

The state-mutating `htnexodus import` command is intentionally out of scope for this branch port.
That later work will need its own `--force`-gated implementation and review before GhostDAG can
rebaseline a node's consensus state from one of these bundles.
