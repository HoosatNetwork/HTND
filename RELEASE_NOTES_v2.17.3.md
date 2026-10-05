# HTND v2.17.3 Release Notes

Release date: 2026-09-25

## TL;DR
- **Update your node.** If you don't, it stops following the real network at DAA score 227,679,830.
- **Miners get paid for every valid block.** No more blocks that earn nothing.
- **New nodes sync without getting stuck.**
- **Fewer crashes, fewer stuck nodes, and fewer bans of honest peers.**
- **Developers:** change imports to `github.com/HoosatNetwork/HTND/v2`.

Covers everything since v2.17.0 (including the v2.17.1 and v2.17.2 patch releases).

**Upgrading is required.** Block version 10 activates on mainnet at DAA score **227,679,830**. After that point, upgraded nodes reject the version 9 blocks that older builds keep producing, so nodes that don't upgrade split off from the network.

## Highlights

- **Block version 10 hard fork on mainnet** at DAA score 227,679,830.
- **Fairer coinbase rewards from version 10.** Every accepted merge set block is now paid its subsidy and fees. Before, a valid block could get nothing because it fell outside the difficulty window's sample. On a live node, almost every coinbase over a 12-hour window dropped at least one block this way. Version 10 also removes rounding errors from coinbase reward calculation.
- **Headers-proof IBD now works reliably (HTN-196).** Nodes syncing from a pruning point proof now color blocks exactly as their syncer does. They no longer get stuck on a pruning point that meets the peer's chain only at virtual genesis.
- **Difficulty no longer collapses after an outage.** A long gap in the difficulty window used to push difficulty straight to the easiest possible value. Nodes that had just finished a headers-proof IBD also mined at genesis difficulty. Both are fixed.
- **Fewer false disqualifications.** Several UTXO commitment and multiset defects that wrongly disqualified valid chains are fixed. If the node does end up disqualifying the network's chain, it now shuts down after 15 consecutive disqualified blocks. Before, it kept running on a stale virtual while looking healthy.
- **Go module path is now `github.com/HoosatNetwork/HTND/v2`.** Projects that import HTND packages need to update their imports.

## Consensus

### Block versions
- Each block's version-specific rules now come from that block's own version, not from the process-wide block version global. This covers:
  - GHOSTDAG coloring and pruning proof header validation
  - the DAA and difficulty window size
  - the block mass limit and accepted-ID merkle order
  - coinbase construction, including the header and coinbase of newly built blocks
  - virtual parent selection and tip ordering
- Finality and pruning depth now follow the chain's current block version.
- Per-version parameter tables are no longer indexed past their end or by the global block version. CI now rejects raw per-version indexing.
- `SetBlockVersion` is now an atomic compare-and-swap, which removes a check-then-act race.

### UTXO set, multiset and fees
- A duplicate coinbase that more than one chain block has already accepted is now skipped or restamped in the MuHash multiset. Before, it produced `ErrBadUTXOCommitment` on correct blocks and disqualified the chain down to the tip.
- A transaction accepted despite missing inputs is accepted only when at least one input resolves. Absent inputs are skipped in both its multiset contribution and its pruning point diff.
- Outputs are kept when a transaction spends a coin this UTXO set does not hold.
- A UTXO offset one block above an imported pruning point is now tolerated and reported accurately.
- A database error while filling inputs no longer leaves a block marked UTXO-valid.
- A staged nil UTXO diff child no longer reads back the stored one.
- Fixed a fee disagreement between block building and block validation. When acceptance data lacks input UTXO entries, the recorded fee is used.
- Coinbase fee mismatches are now logged with the source of each fee, including which merge set block was paid nothing and why.

### Virtual resolution
- Virtual keeps a UTXO-valid selected parent instead of moving to a lighter, DAGKnight-ordered pending chain. This now applies on every resolve, not only chunked ones. This was very likely the main source of frequent reorgs that gained no work.
- Virtual's UTXO set stays correct while a disqualified chain is resolved in chunks.
- The reachability reindex root is reconsidered on every block and keeps advancing past blocks that have no GHOSTDAG data.

### Difficulty
- The retarget's actual time span is capped at 4x the expected span. A long gap in the window can no longer push difficulty to the floor.
- After a pruning point import, the difficulty window uses the pruned block's trusted DAA window. That trusted window is also served to peers, so the fix survives more than one hop.

### Concurrency
- These cached fields are now safe for concurrent readers: transaction ID, populated fee, populated mass and header block level.
- Concurrent subnetwork ID formatting no longer returns another transaction's ID.

## IBD and pruning
- Headers-proof sync fixes (HTN-196):
  - A block is treated as pruned only when this node has no GHOSTDAG data for it.
  - The pruning point anticone is served sorted by the syncer's own blue work and reordered by parent links on receipt.
  - Proof headers are colored the same way the syncer colors them.
- A pruning point that is not the root of the synced chain is refused. The node then retries with another peer instead of committing an unusable state. Serving nodes apply the same check.
- Body sync is skipped when the two chains meet only at virtual genesis. IBD no longer loops "successfully" in that state.
- A node whose tips are all disqualified now recovers instead of stopping.
- IBD no longer re-requests block bodies from a peer that never answers, and only requested blocks count toward a body batch.
- IBD now switches to the committed staging consensus before cleaning up the old one.
- A malformed pruning point proof or trusted data now gets a clean rejection instead of a panic. This covers an empty first proof level and out-of-range trusted-data indices.
- `--deletion-depth` keeps the blocks of recent pruning points.
- An interrupted pruning point UTXO set update resumes with the diff it had chosen.

## P2P
- Honest peers are no longer banned for this node's own local errors. Flow errors that are not the peer's fault now disconnect the peer without a ban.
- Resource use from any single peer is now bounded:
  - block reads per `IBDBlockLocator`
  - transaction invs queued while waiting for requested transactions
  - transactions held during IBD, limited by bytes as well as count
  - the hash count in `RequestIBDBlocks`
- One stalled syncee no longer blocks pruning point anticone serving for every other peer.
- The last served header chunk is cached, so a stuck peer can't repeatedly tie up the consensus lock.
- Silent outbound peers are disconnected, and broadcasts continue past a peer whose outgoing route is full.
- Block request flows now fail cleanly when a block can't be served or a locator can't be built. The peer is no longer left waiting.
- `RequestNextHeaders` sent outside a header exchange is rejected instead of causing a panic.
- New blocks now send transaction IDs that were held back for batching.
- Parents this node already has are no longer reported as orphan roots.
- gRPC peer seeding now has a deadline.

## Mempool and mining
- Transactions are removed from the mempool only after a UTXO-valid block accepts them.
- Orphan handling fixes:
  - orphan chains are promoted transitively
  - orphan children are kept when a block mines their parent
  - an orphan's priority carries through promotion
  - relayed compound orphans stay evictable and expirable
- Diamond-shaped transaction chains can no longer hang the mempool.
- Replacement and promotion now reject double spends of outputs in the pool.
- These inputs no longer crash the node:
  - an out-of-range index in fee-rate eviction
  - a transaction spending a missing output of a pool transaction
  - a negative extra-output count when building a block template
- High-priority transactions that consensus now rejects are removed during revalidation.
- Block templates are filled only up to the next block's mass limit.
- The dev-fee output stays in place when a template's pay address changes.
- Pending-transaction lookups by address now return every match.

## RPC and rpcclient
- One block that can't be described no longer takes down the whole node.
- Notification listeners of disconnected RPC clients are no longer leaked.
- A slow address-index request no longer holds up the same client's mining requests.
- `UTXOsChanged` notification fixes:
  - notifications reach the addresses they were registered for
  - a subscription ends when its last address is removed
  - a script without an address no longer crashes the broadcast
- Paginated UTXO queries no longer skip a UTXO at each offset and return the correct message type.
- `GetTransactionStatus` no longer reports about 2^64 confirmations for blocks that haven't been merged.
- `GetMempoolEntry` reports the fee of the transaction that was looked up.
- Inputs with verbose data no longer loop forever.
- `StopNotifyingPruningPointUTXOSetOverride` now actually stops the notifications.
- Shutdown over RPC now goes through the interrupt listener.
- rpcclient:
  - reconnect and timeout changes no longer race each other
  - the replaced connection is closed on reconnect
  - `Send`/`CloseSend` are serialized
  - every request, including `GetUsableAddresses` and the by-address queries, now times out
  - a client closed while reconnecting no longer exits the process

## UTXO index
- The index no longer keeps coins that consensus has already spent.
- Coin counts come from what is stored, not from what a change claims.
- Drift warnings appear only when the index and virtual agree on state.
- Bulk balance queries no longer evict block validation's UTXO cache.

## Database and startup
- A corrupted pebble datadir is now detected and recovered at startup. Before, the node failed to start.
- Offline tools (`utxoforensics`, `htnexodus`, the pruning proof harness) never delete a datadir.
- Opening a pebble datadir with the LevelDB engine is refused instead of destroying it.
- Values written with `BatchPut` are visible inside the same pebble transaction.
- Every tracked cursor is closed when the database closes.
- Each block's staged changes are committed through a write-only transaction.
- Shutdown waits for queued consensus events before closing the database.
- Datadir repair passes no longer run on a staging consensus, and the block status repair no longer runs on every boot.
- An empty database version file or an empty stored prefix now produces an error instead of a panic.
- A `GOMEMLIMIT` set with a unit suffix (for example `4GiB`) is kept instead of being replaced with 8 GB.

## Auto-update
- Archives that nobody signed are refused.
- Extracted entries stay inside the extraction directory.
- The new binary is installed by rename, so the old one is never deleted first.
- Restart now requests a graceful shutdown instead of `exec` followed by `os.Exit`.

## Wallet (htnwallet)
- The keys file is saved atomically, and a failed save can't damage the original.
- A send is never rebuilt and rebroadcast after a broadcast error. When a broadcast fails partway, the error names the transactions already submitted.
- Sweeps include every coin, and the merge step no longer spends coins the splits already spend.
- Sends whose fee arithmetic would overflow are rejected.
- These no longer cause a panic:
  - malformed partially signed transactions
  - outputs without an address
  - repeated daemon `Shutdown` requests
- Vote flags are validated up front, and the vote retry loop no longer spins.

## Other fixes
- **Address manager:**
  - addresses with invalid IP lengths are rejected
  - addresses with pre-epoch timestamps are ignored
  - `RandomAddresses` never returns the same address twice
- **Connection manager:** requested-peer renames are now applied after iteration.
- **Config:**
  - `--freeze-address` adds to the default frozen list and validates its input
  - a negative `--minrelaytxfee` is rejected
- **Addresses:** multisig-PKH addresses whose payload isn't a 32-byte hash are rejected.
- **ldbtool:** a `-fresh` copy whose destination is or contains the source is refused.

## Performance
- The virtual rebuild is skipped when the parent set hasn't changed.
- Block templates are reused for 100 ms, so mining polls skip the consensus lock.
- Known-ancestor results are memoized across candidates in `pickVirtualParents`.
- DAG tips already known to violate finality are not rechecked.
- Hashes are compared as bytes, not as hex strings.
- UTXO index result buffers are sized from the maintained count instead of scanning twice.

## Build and tooling

- Go 1.27.1.
- The module moved to the `/v2` import path.
- `utxoforensics` gains a canonical pruning point UTXO artifact tool. It also has new checks for header bits against required difficulty and for blue score or blue work that disagrees with stored GHOSTDAG data.
- The unsourced `pebble-tool` binary was removed.
