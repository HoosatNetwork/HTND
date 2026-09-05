# Exodus pruning point candidate tooling (`htnexodus`)

> Status: experimental, `exodus-pruning-point` test branch only. This tool does **not** change
> any consensus rules and does **not** ship, embed, or require any checkpoint in the node
> binary or `dagconfig`. `create`/`verify`/`diff` are local, read-only tooling for generating and
> comparing candidate "exodus pruning points" so the community can converge on one
> (HoosatNetwork/HTND#21). `import` (HoosatNetwork/HTND#20) is a separate, operator-invoked,
> state-mutating command for rebaselining a node's own consensus state onto an already-agreed
> candidate; it is not automatic and does not distribute candidates between nodes.

## Background

Mainnet has had repeated chain stalls that have left some outpoints unrecoverable, making the
locally-calculated pruning-point UTXO set unreliable on some nodes. An "exodus pruning point" is
a proposed remedy: a manually authored, community-vetted UTXO set checkpoint at a specific
DAA score/block, that nodes can import as a trusted floor.

Before that can happen, the community needs a way to:

1. generate a candidate bundle from any node's own synced state,
2. verify a candidate bundle is internally self-consistent, and
3. diff two candidates (or a candidate against a live node) to find and reconcile
   disagreements between independently-run nodes.

Only once the community has converged on a candidate does `import` come in: it lets an operator
rebaseline their own node onto that candidate's UTXO set, forcing it to be treated as a trusted
floor rather than something the node needs to (unreliably) recompute itself.

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
streaming reads/writes are all that `exodus create` (write) and `exodus import` (read) need -
there is no requirement for random access by outpoint during either operation.

`exodus create` is resumable: since the underlying UTXO iteration always restarts from the
beginning of the requested historical block's UTXO set (there is no cursor-resume for an
arbitrary past block), a resumed run re-derives every entry, but recognizes chunks from a
previous, interrupted attempt whose SHA-256 digest still matches the manifest and skips
re-writing them, only paying the disk write/hash cost for chunk data not already durably
persisted.

See the package doc comment in `domain/exodus/bundle.go` for the full authoritative
description of the format.

## Usage

The node must be stopped before running `htnexodus create`, `htnexodus diff --live`, or
`htnexodus import` against its database directory - all three would otherwise contend with a
running node for the same database files. `htnexodus verify` and a `htnexodus diff` between two
already-generated bundles do not touch a node's database at all and can be run at any time.

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
the pruning boundary (that history has been discarded locally and is simply not retrievable from
this node - pick a more recent DAA score, or sync a node with a deeper retention window).

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

### Import a candidate bundle (rebaseline the node's own consensus state)

> **This is the one command in this tool that mutates the node's own consensus database.**
> `create`, `verify`, and `diff` never write anything to the node's own database - `import`
> does, deliberately. Stop the node first, and back up the database directory before the first
> time you run this against a database you care about.

```sh
./htnexodus import \
  --bundle ./candidate-2025-09 \
  --db-path ~/.htnd/hoosat-mainnet/datadir2 \
  --network mainnet \
  --force
```

`--force` is mandatory and has no default: it exists so this can never run by accident, only by
explicit operator decision, consistent with HoosatNetwork/HTND#20 requiring this to be
operator-invoked, never automatic.

What it does, in order:

1. Re-verifies the bundle's internal self-consistency (same check as `exodus verify`) and
   refuses to proceed if it fails.
2. Confirms the bundle's target block is already fully known to this node locally (its own
   header, body and GHOSTDAG data must already exist - `import` does not fetch or otherwise
   learn about unknown blocks; there is still no node-to-node distribution of candidates).
3. Streams every UTXO entry from the bundle into the node via the same
   `ClearImportedPruningPointData` / `AppendImportedPruningPointUTXOs` /
   `ValidateAndInsertImportedPruningPoint` consensus API sequence real headers-proof IBD already
   uses in production to bootstrap a fresh node's initial pruning point UTXO set - no new,
   previously-unexercised consensus logic was written for this command.
4. On success, forces the virtual block's sole parent to the target block and replaces the
   node's virtual UTXO set with the bundle's contents, so the target block becomes the new
   trusted UTXO baseline.

**Known limitations of this first version:**

- It does not update `PruningPoint()`'s own "current pruning point" record - that value is only
  ever advanced by the node's ordinary block-processing pruning-depth logic
  (`UpdatePruningPointByVirtual`), which is unmodified by this tool. It will lag behind the new
  virtual parent until normal operation catches up; this is intentional (see
  HoosatNetwork/HTND#21's explicit non-goal of not touching pruning-point selection logic).
- It reuses the same UTXO-commitment-mismatch tolerance logic real IBD already has (see
  `pruningmanager.go`'s `verifyAndRepairImportedPruningPointUTXOSet`): if the bundle's computed
  commitment doesn't match the target block's own header commitment (a known, tolerated
  condition on chains with historical stalls), the import still proceeds, using the bundle's own
  recomputed multiset as the new trusted anchor, exactly as the existing tolerated-import code
  path already does for real peer-served pruning points.
- It only supports rebaselining onto a block already on the node's own known chain; it does not
  attempt to resolve or discard a locally-diverging fork.

## Non-goals (see HoosatNetwork/HTND#21)

- No hard fork, no consensus rule changes.
- No signature scheme or key management - `--note` is free text, unauthenticated.
- No shipping/embedding a checkpoint into the binary or `dagconfig`.
- No changes to fork-choice or pruning-point selection logic.
- No node-to-node distribution protocol for candidates (manual file sharing only).
