# HTND v2.16.0 Release Notes

Release date: 2026-09-01

This release includes 365 commits with ~7856 lines added and ~1606 lines deleted across 173 files. Major focus areas include IBD reliability improvements, UTXO diff corruption fixes, coinbase handling with block versions 8 and 9 activation, virtual block management, and performance optimizations.

---

## Consensus & Block Processing

### Block Version 8 & 9 Activation
- **Block version 8 activated** at DAA score 217,137,983 (POWScores[6])
- **Block version 9 activated** at DAA score 218,735,007 (POWScores[7])
- Added per-block coinbase entropy (version 8+) based on merge set + DAA score to prevent transaction ID collisions between sibling blocks
- Added timestamp-based coinbase entropy (version 9+) that additionally folds the block's own header timestamp into entropy for stronger collision resistance
- All version-indexed parameter arrays extended to 9 entries to support versions 8 and 9

### Coinbase & UTXO
- Fixed coinbase manager to use each block's own version (not ambient `GetBlockVersion()`) when building expected coinbase transactions
- Fixed UTXO diff corruption in reorg merges caused by coinbase ID collisions
- Fixed `AddTransaction` in mutable UTXO diff to properly handle outpoint collisions (only treat same-valued collisions as no-op)
- Restored previously-disabled pruning-point-import UTXO commitment validation
- Fixed `updateSelectedTipUTXODiff` to reconcile full absolute virtual diff with selected tip's relative diff instead of silently staging wrong diff

### Reorg & Diff Handling
- Added `reconcile_reorg_utxo.go` with reconciliation logic for reorg UTXO sets
- Fixed `resolve_block_status.go` to properly handle disqualified blocks and their UTXO diffs
- Added tolerance for inherited pruning-point offsets in UTXO commitment validation
- Made `verifyUTXO` uniformly permissive on inherited pruning-point offsets
- Added repair of imported pruning-point UTXO multisets at import time
- Fixed the fourth unprotected conflict shape in diffFrom
- Fixed missing-input block transaction handling on inherited pruning-point offsets
- Added robust, marker-free UTXO commitment toleration

### Virtual Blocks
- Comprehensive fixes for virtual genesis and virtual block handling
- Fixed virtual block staging and retrieval in consensus state manager
- Added guards for virtual genesis blocks in blue anticone calculations
- Fixed `pick_virtual_parents.go` to not select disqualified blocks
- Ensured virtual genesis/blocks are not added as last valid blocks in tip calculations
- Added filtering of virtual genesis hashes from merge sets
- Fixed child iterators to properly handle nil high/low values against virtual blocks
- Added mutex to block retrieval to prevent race conditions

### Pruning
- Added pruning store with 2-entry cache (new file)
- Added pruning point validation: ensure pruning point is in selected parent chain of locator hash
- Fixed pruning point diff calculation and messages
- Added `CheckMergeSetBluesAndIfBlockExistsInThem` to validate pruning points
- Don't create pruning point with any other than UTXO valid blocks
- Increased UTXODiff LRU cache size from 1000 to 250,000

---

## IBD (Initial Block Download)

### Core IBD Fixes
- Replaced selected child iterator with BFS (Breadth-First Search) in `antiPastHashesBetween` for improved parent-closure completeness
- Fixed antiPastHashesBetween parent-closure completeness bug causing IBD missing parents
- Fixed missing parent errors in antiPastHashesBetween by ensuring actualHighHash progress
- Fixed integer overflow on IBD zooming
- Added configurable past median time tolerance (2 seconds) to accommodate clock drift between nodes

### Zoom & Stall Handling
- Root-cause fix: conclude IBD negotiation when the zoom window is all header-only
- Don't ban peers for IBD zoom stalls caused by all-header-only windows
- Abandon (don't ban) peers on any prolonged IBD zoom stall
- Stop banning peers for IBD zoom-in stalls caused by local disqualification
- Log IBD zoom steps at debug level on healthy path, warn only when stuck
- Dump full locator window when IBD zoom stalls for non-disqualification reasons
- Fixed premature NO-OP exit from ResolveVirtual during IBD

### Header-Only Blocks
- Send blocks in IBD even if header-only, but mark their PoW hash as header-only and don't process them
- Don't send header-only blocks in normal operation
- Don't use header-only blocks as lowest unknown syncer chain hash
- Fixed resolve virtual failure with header-only blocks in IBD
- Fixed pruning point error during IBD with headers proof
- Fixed IBD with headers proof when pruning point is unchanged
- Fall back to shared ancestor when pruning point isn't on syncer tip's chain
- Never fail IBD in missingBlockBodyHashes on a diverged local chain

### Performance
- Added per-block stage timing to identify ~400ms/block IBD processing cost
- Separated IBD network-wait time from local-processing time per batch
- Instrumented restorePastUTXO and DiffFrom to find IBD slowness sources

---

## GHOSTDAG & DAGKnight

### DAGKnight
- DAGKnight consensus algorithm is present in codebase (dagknight.go)
- Fixed DAGKnight selected-parent attribution and error propagation
- Added filtering of disqualified blocks in GHOSTDAG/DAGKnight merge sets
- Added check in `findSelectedParent` to skip disqualified blocks
- Added MaxDAGKnightTips constant (24)
- Added DAGKnight faithfulness analysis document

### GHOSTDAG Manager
- Fixed nil pointer dereference in ghostdag manager that propagated to get anticone
- Fixed `getChainPath` to handle nil blocks and nil selected parents
- Removed excessive logging from partitionByLCAFuture
- Fixed `agrees` function to handle nil B or C parameters
- Added sorting of antipast by blue score
- Fixed child iterator to respect not going beyond high hash and include low hash support

---

## Network & Peer Management

- Fixed nil pointer dereferences in sendLoop, receiveLoop, and fromAppMessage
- Added mutex to get block operations
- Ignore inv for virtual hashes
- Added proper error handling for missing blocks

---

## Performance & Database

### PebbleDB
- Better PebbleDB settings based on AI feedback (Claude, Gemini, Grok)

### Caching
- Increased UTXODiff LRU cache size from 1000 to 250,000
- Removed redundant calls from restorePastUTXO

### Blue Anticone
- Added max walk limits to blueAnticoneSize to prevent excessive traversal
- Increased max traversal by 5x where needed
- Fixed nil pointer dereference in blueAnticoneSize
- Fixed blueAnticoneSize to not fetch ghostdag data for virtual block hashes
- Small optimization: cut one iteration of loop when candidateBluesAnticoneSizes exceeds max

---

## Security & Stability

- 20+ nil pointer dereference fixes across: ghostdag manager, dagTraversalManager, blueAnticoneSize, sendLoop, receiveLoop, fromAppMessage, ghostdag data store, block validator, etc.
- Fixed races to pass CI tests
- Fixed double-close panic in RPC stats Stop()
- Fixed panic in ghostdag data store when block hash is nil
- Added validation that block hash cannot be nil from ghostdag datastore
- Reverted unsafe byteslice operations in serialization code for memory safety
- Removed hardcoded GitHub personal access token from source code
- Added proper mutex locking for thread-safe operations
- Don't propagate ErrMissingTxOut error
- If transaction is missing outpoints, don't error - just don't accept the transaction

---

## Auto-Updater && Auto-Reporter

- Changed auto-updater and auto-reporter to **opt-in** (was opt-out) per NonKYC request
- Fixed race condition in updater with updateInProgress flag

---

## Build & Infrastructure

- Upgraded to Go 1.27
- Cross-platform rlimit support with Windows-safe implementation
- Dockerfile updated to Go 1.27
- Better build system for Docker images
- Updated module dependencies
- Added lefthook for pre-commit git hooks
- Added sample lefthook.yml and letfhook.yml files

---

## Code Quality & Testing

- Added skip to long-running tests
- Fixed numerous test golden data comparisons
- Improved debug logging across consensus, UTXO, and network components
- Moved excessive logging from INFO/WRN to DBG level
- Fixed format string errors (%d to %s) throughout
- Removed code coverage requirements where not needed
- Fixed test failures and build errors
- Added detailed script comparison logging for coinbase validation
- Added targeted IBD/virtual-resolution diagnostic logging
- Fixed TestReverseUTXODiffs

---

## RPC & API

- Fixed nil pointer dereference in RPC routerInitializer
- Fixed format string errors in various RPC handlers
- Removed ASCII art from output
- Added info of block status to received blocks
- Return error if header DAA score does not exist, use daaBlockStore
- Use headers DAA score, not daaBlockStore DAA score for consistency

---

## Wallet

- Added fee estimation to wallet (cherrypick from Kaspa #2291)
- Wallet compounder improvements to prevent mempool orphans and selfish mining

---

## Mining

- Fixed block template builder to use correct blue scores
- Reconciled reorg UTXOs for proper mining
- Don't stage disqualified block UTXOs
- Added backup validation for coinbase red block outputs
- Added detailed debug logging to coinbase manager
- Stopped bucketing red block merge rewards

---

## Configuration

- Added `PastMedianTimeValidationTolerance` config option (default: 2000ms = 2 seconds)


---


## Known Issues

- Pruning point validation is temporarily disabled in some scenarios because of utxo commitment mismatches with pruning points.

---

## Breaking Changes

None. This is a backward-compatible release.

---

## Files Changed

- 173 files changed
- 7856 insertions(+)
- 1606 deletions(-)
- New files: `pruningstore/pruning_store.go`, `child_iterator.go`, `reconcile_reorg_utxo.go`, `antipast_test.go`, `antipast_order_test.go`, rlimit files, lefthook configs, etc.

---

*Generated from commits between v2.15.0 (2426ce95d) and HEAD (9b37508d5)*
