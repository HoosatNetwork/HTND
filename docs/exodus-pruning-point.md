# Exodus pruning point candidate tooling (`htnexodus`)

> Status: experimental, `exodus-pruning-point` test branch only. This tool does **not** change
> any consensus rules and does **not** ship, embed, or require any checkpoint. It is local
> tooling for generating and comparing candidate "exodus pruning points" so the community can
> converge on one before any later ratification/import work (tracked separately in
> HoosatNetwork/HTND#20).

## Background

Mainnet has had repeated chain stalls that have left some outpoints unrecoverable, making the
locally-calculated pruning-point UTXO set unreliable on some nodes. An "exodus pruning point" is
a proposed remedy: a manually authored, community-vetted UTXO set checkpoint at a specific
DAA score/block, that a later, separate piece of tooling can import as a trusted floor.

Before that can happen, the community needs a way to:

1. generate a candidate bundle from any node's own synced state,
2. verify a candidate bundle is internally self-consistent, and
3. diff two candidates (or a candidate against a live node) to find and reconcile
   disagreements between independently-run nodes.

That is what `htnexodus` (`cmd/htnexodus`) does.

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

A bespoke chunked binary format was chosen over a dedicated PebbleDB instance because the
artifact is fundamentally write-once/read-a-few-times: no compaction or extra file-count
overhead, trivial to hash chunk-by-chunk and distribute as plain files, and sequential
streaming reads/writes are all that `exodus create` (write) and a future `exodus import` (read,
see HoosatNetwork/HTND#20) need - there is no requirement for random access by outpoint during
either operation.

`exodus create` is resumable: since the underlying UTXO iteration always restarts from the
beginning of the requested historical block's UTXO set (there is no cursor-resume for an
arbitrary past block), a resumed run re-derives every entry, but recognizes chunks from a
previous, interrupted attempt whose SHA-256 digest still matches the manifest and skips
re-writing them, only paying the disk write/hash cost for chunk data not already durably
persisted.

See the package doc comment in `domain/exodus/bundle.go` for the full authoritative
description of the format.

## Usage

The node must be stopped before running `htnexodus create` or `htnexodus diff --live` against
its database directory - both processes would otherwise contend for the same database files.
`htnexodus verify` and a `htnexodus diff` between two already-generated bundles do not touch a
node's database at all and can be run at any time.

### Generate a candidate

By block hash:

```sh
./htnexodus create \
  --db-path ~/.htnd/hoosat-mainnet/datadir2 \
  --network mainnet \
  --block <hex-block-hash> \
  --out ./candidate-2025-09 \
  --note "operator: alice, rationale: last known-good DAA score before the August stall"
```

By DAA score (resolved by walking the selected parent chain from the tip; the node must have
already synced past this DAA score):

```sh
./htnexodus create \
  --db-path ~/.htnd/hoosat-mainnet/datadir2 \
  --network mainnet \
  --daa-score 123456789 \
  --out ./candidate-2025-09
```

This prints the computed UTXO set commitment and compares it against the target block's own
header commitment as a sanity check (a mismatch is expected precisely in the cases this tooling
exists to help investigate - the node's own pruning-time UTXO calculation being unreliable - and
does not by itself mean the bundle is wrong).

### Verify a candidate bundle's internal self-consistency

```sh
./htnexodus verify --bundle ./candidate-2025-09
```

Re-reads every chunk, checks its SHA-256 digest and entry count against the manifest, recomputes
the multiset commitment from the entries, and compares it against the manifest's claimed
commitment.

### Diff two candidates

```sh
./htnexodus diff --bundle-a ./candidate-2025-09 --bundle-b ./candidate-from-bob
```

### Diff a candidate against a live node recomputation

```sh
./htnexodus diff --bundle-a ./candidate-2025-09 --live \
  --db-path ~/.htnd/hoosat-mainnet/datadir2 --network mainnet
```

`diff` reports counts for both sides, outpoints present in only one side (with aggregate sompi
value), and outpoints present in both but with a differing entry (amount, script, DAA score, or
coinbase flag), which is intended to be the primary tool for reconciling disagreements between
independently-run trusted nodes.

## Non-goals (see HoosatNetwork/HTND#21)

- No hard fork, no consensus rule changes.
- No signature scheme or key management - `--note` is free text, unauthenticated.
- No shipping/embedding a checkpoint into the binary or `dagconfig`.
- No changes to fork-choice or pruning-point selection logic.
- No import/rebaseline mechanism (tracked separately in HoosatNetwork/HTND#20).
