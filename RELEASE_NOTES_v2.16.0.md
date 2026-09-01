# HTND Release Notes v2.16.0

This release includes significant improvements to consensus stability, IBD (Initial Block Download) reliability, UTXO diff handling, and performance optimizations. Over 360 commits contribute to this release, with major focus areas including DAGKnight consensus integration, virtual block handling, pruning point management, and numerous bug fixes.

## Highlights

### Consensus & DAGKnight Integration
- **DAGKnight Consensus**: Full integration of DAGKnight consensus algorithm with HTND, including selected-parent fix activation at DAA score 214,600,000 (Block version 8)
- Block version 8 introduces DAAScore-gated selected-parent fix for improved consensus security
- Fixed DAGKnight selected-parent attribution and error propagation
- Added filtering of disqualified blocks in GHOSTDAG/DAGKnight merge sets
- Enabled native subnetwork transaction payloads
- Added OP_CHECKTEMPLATEVERIFY (CTV) v2 support and L2 script v1 splice operations

### IBD (Initial Block Download) Improvements
- **Header-only block handling**: Comprehensive fixes for IBD with header-only blocks, including proper handling in zoom windows and pruning point scenarios
- **Zoom stall fixes**: Multiple fixes preventing bans for legitimate IBD stalls caused by all-header-only windows or local disqualifications
- **Pruning point tolerance**: Added tolerance for inherited pruning-point offsets in UTXO commitment validation, preventing IBD failures on diverged local chains
- **BFS iterator**: Replaced selected child iterator with BFS (Breadth-First Search) in antiPastHashesBetween for improved parent-closure completeness
- **Performance instrumentation**: Added per-block stage timing and separated network-wait from local-processing time to identify and address ~400ms/block IBD processing cost
- Fixed integer overflow in IBD zooming
- Added configurable past median time tolerance (2 seconds) to accommodate clock drift between nodes
- Improved nearly-synced detection and handling

### UTXO Diff Algebra
- **Pruning point offset handling**: Comprehensive fixes for inherited pruning-point offsets, including:
  - Toleration of UTXO commitment mismatches below inconsistent pruning points
  - Robust marker-free UTXO commitment toleration
  - Repair of imported pruning-point UTXO multisets at import time
  - Proper offset baseline detection from pruning points
- **Coinbase validation**: Fixed coinbase transaction validation to use correct GHOSTDAG data, with improved error reporting
- **Diff corruption fixes**: Fixed UTXO diff corruption in reorg merges and coinbase ID collisions
- **Conflict tolerance**: Enhanced isTolerableConflict to handle same-valued coinbase duplicates and fully identical conflicting entries
- Added tolerance for missing-input block transactions on inherited pruning-point offsets

### Virtual Block & Genesis Handling
- Comprehensive fixes for virtual genesis and virtual block handling across the codebase
- Fixed virtual block staging and retrieval
- Added guards and checks for virtual genesis blocks in blue anticone calculations
- Improved handling of virtual blocks in IBD, including proper window heap processing
- Fixed nil pointer dereferences related to virtual genesis blocks
- Ensured virtual genesis/blocks are not added as last valid blocks in tip calculations
- Added filtering of virtual genesis hashes from merge sets

### Blue Anticone & Performance
- **Blue Anticone Size**: Added max walk limits and optimizations to prevent excessive traversal
- Increased max traversal limits where needed (5x increase in some scenarios)
- Added PebbleDB settings optimizations based on community feedback
- Increased UTXODiff LRU cache size to improve restorePastUTXO performance
- Better Pebble value separation (1KB was too small)
- Improved BFS child iterator with proper high/low hash boundaries

### Security & Stability
- **Nil pointer dereference fixes**: Over 20 fixes for nil pointer dereferences across ghostdag manager, dagTraversalManager, blueAnticoneSize, and other components
- **Race condition fixes**: Fixed races to pass CI tests
- **Memory safety**: Reverted unsafe byteslice operations in serialization code
- Removed hardcoded GitHub personal access token from source code
- Added proper mutex locking for thread-safe operations
- Fixed double-close panic in RPC stats
- Added validation for node fee outputs in consensus

### Network & Peer Management
- Don't ban peers for legitimate IBD zoom stalls
- Abandon (don't ban) peers on prolonged IBD zoom stalls
- Added force-same-version peer filtering to prevent protocol mismatches
- Prevent getting stuck talking to very slow senders (tar pit mitigation)
- Handle ErrUnexpectedParents gracefully in block relay
- Added checks to prevent relaying blocks with disqualified parents
- Don't ban for sending disqualified blocks when node already has them
- Disabled indirect parent check temporarily

### Auto-Updater & Reporting
- Made auto-updater and auto-reporting **opt-in** (changed from opt-out)
- Auto-updater now self-updates HTND and its binaries from GitHub releases
- Added automatic GitHub issue reporting on panics (opt-in)
- Added random delay before installing updates to prevent mass simultaneous updates
- Fixed race condition in updater with updateInProgress flag
- Fixed GitHub release API issues
- Updated Dockerfile to disable auto-updating by default

### Build & Infrastructure
- **Go version support**: Upgraded to Go 1.26.0 and added Go 1.27 support
- Cross-platform rlimit support (including Windows-safe implementation)
- Dockerfile updated to Go 1.27
- Better build system for Docker images
- Removed genalphabet reference
- Updated module dependencies
- Added lefthook for pre-commit git hook usage

### Code Quality & Testing
- Added skip to long-running tests
- Fixed numerous test golden data comparisons
- Improved debug logging across consensus, UTXO, and network components
- Moved excessive logging from INFO/WRN to DBG level for better signal-to-noise ratio
- Fixed format string errors throughout the codebase
- Removed code coverage requirements where not needed
- Fixed test failures and build errors

### Known Issues & Workarounds
- Some finality tests are currently disabled as they are not yet compatible with DAGKnight consensus
- Pruning point validation is temporarily disabled in some scenarios
- Header-only block guards have been adjusted to accommodate IBD scenarios

## Detailed Changes

### Consensus Layer
- Fixed block template and validation to have different blue scores
- Reconciled reorg UTXOs
- Added validation that pruning point is in the selected parent chain of locator hash
- Staged consensus UTXO diff and multiset in consensus state manager
- Fixed validation and insertion of imported pruning point blocks
- Added detailed script comparison logging for coinbase validation
- Added backup validation for coinbase red block outputs
- Fixed coinbase validation to use each block's own version
- Fixed block relay to use correct block versions

### Database & Storage
- Applied PebbleDB settings from v2.10.1
- Better PebbleDB value separation
- Fixed LRU cache issues
- Disabled LRU cache mutexes (they were slowing down the node)
- Increased UTXODiff LRU cache size
- Removed redundant calls from restorePastUTXO

### Network Protocol
- Fixed missing parent errors in antiPastHashesBetween
- Fixed antiPastHashesBetween parent-closure completeness bug causing IBD missing parents
- Sort antiPast by blue score for consistent ordering
- Ignore inv for virtual hashes
- Added mutex to get block operations
- Fixed send/receive loop nil pointer dereferences
- Added proper error handling for missing blocks

### Block Processing
- Don't send header-only blocks in normal operation
- Mark header-only blocks in IBD and don't process them
- Don't use header-only blocks as lowest unknown syncer chain hash
- Fixed premature NO-OP exit from ResolveVirtual during IBD
- Fixed resolve virtual failure with header-only blocks in IBD
- Fixed ResolveVirtual not finding pending blocks and not updating tips

### Wallet
- Added fee estimation to wallet (cherrypick from Kaspa #2291)
- Wallet compounder improvements to prevent mempool orphans and selfish mining

### API & RPC
- Fixed nil pointer dereference in RPC routerInitializer
- Fixed format string errors in various RPC handlers
- Removed ASCII art from output

### Block Version 8 Features
- DAAScore-gated selected-parent fix activation at 214,600,000
- Proper selected parent attribution in DAGKnight
- Error propagation improvements
- Compatibility fixes for DAGKnight consensus

## Breaking Changes

None. This is a backward-compatible release.

## Upgrade Notes

- Auto-updater is now **opt-in**. Users who want automatic updates must explicitly enable them.
- Block version 8 activates at DAA score 214,600,000. Nodes should upgrade before this point to avoid consensus issues.
- Go 1.26.0 or later is recommended for building from source.
- The unsafe byteslice operations have been reverted for safety. Performance impact is minimal.

## Contributors

Major contributions from Toni Lukkaroinen with assistance from AI tools (Claude, Gemini, Grok) for code review and optimization suggestions.

---

*Generated from commits between v2.15.0 and HEAD (2026-09-01)*
