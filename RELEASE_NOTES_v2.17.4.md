# HTND v2.17.4 Release Notes

Release date:  1.10.2026

## TL;DR
- **Update your node.** This build enforces the block version 10 coinbase and value checks that earlier builds only tolerated. Mainnet is already on version 10, so these checks apply as soon as you upgrade.
- **Mempool now requires inputs to be at least 1000 DAA old.** Transactions that spend fresh outputs, or outputs of unconfirmed transactions, are refused. Wallets, exchanges and pools that chain unconfirmed transactions need to adjust.
- **htnwallet waits longer before spending coinbase outputs**, so a short reorg can no longer leave a send spending coins that don't exist.
- **Much faster virtual resolution on long chains.** DAGKnight tip ordering went from over a minute to under a second.
- **Better diagnostics** for UTXO commitment mismatches and disqualified blocks.

Covers everything since the v2.17.3 release.

## Highlights

- **Value checks enforced at block version 10.** A block's coinbase must pay the expected amounts to the expected scripts. A transaction accepted despite missing inputs must now pass validation and can't spend more than the inputs this node can find. Before, nodes in "offset mode" accepted both on trust.
- **Miner's-view toleration kept.** Mainnet mining nodes don't all commit the same UTXO multiset. A block whose only problem is a UTXO commitment or accepted-ID merkle root this node can't reproduce is still accepted, as long as its acceptance data agrees with its UTXO diff. This is what lets nodes follow the network's chain.
- **Disqualified blocks are logged, not fatal.** A disqualified block now produces a full report as a warning: every check with its verdict, the miner, the toleration inputs, the selected parent, the pruning point and the merge set. A single bad block no longer stops the node. The node still shuts down after 15 consecutive disqualified blocks.
- **New mempool input-age policy** (`--input-min-age-daa`, default 1000). See [Mempool](#mempool).
- **DAGKnight performance fix.** Finding common chain ancestors now costs time proportional to fork depth, not chain length.

## Consensus

### Validation rules
- From block version 10:
  - **Missing-input acceptances are validated.** Found inputs get script, signature, maturity, sequence lock and sigop checks. Outputs can't exceed the found inputs (`ErrSpendTooHigh`), and the real fee is recorded. A transaction with no found inputs is rejected (`ErrMissingTxOut`).
  - **Coinbase toleration is bounded.** An imperfect coinbase is tolerated only if its fields, output count and payee scripts match. Each output, and the total, may exceed the expected value by at most 0.1 HTN for each merge set transaction this node couldn't price. Underpaying is allowed. Anything else disqualifies the block.
  - The UTXO commitment check is strict, except for the miner's-view toleration described above.
- The miner's-view toleration now has its own gate, `StrictMinersViewFieldsVersion`, which isn't scheduled. It can only be activated once every mining node commits the same multiset.
- The miner's-view toleration now applies on every node. Before, it depended on which node happened to mine the current pruning point. A node could switch enforcement on as soon as its pruning point moved, and then disqualify an ordinary block.
- All hard-fork gates moved from the `hardforks` package into `domain/dagconfig/params.go`, next to the `POWScores` entries that define block versions. `dagconfig.HardForkActive` replaces `hardforks.Active`. `RefuseMismatchedImportVersion`, `ValidateHeaderBitsVersion` and `ValidateIBDPruningListVersion` are unscheduled.

### UTXO set
- Restoring the past UTXO set of a block whose selected parent is genesis now walks genesis's diff chain to virtual. Before, the restored set could hold almost all of virtual's coins when virtual was on another chain. This made nodes compute a wrong commitment and refuse to reorg to a heavier chain. Mainnet isn't affected, because no mainnet chain block has genesis as its selected parent.

### Performance
- DAGKnight `OrderDAG` finds the latest common chain ancestor by walking two selected-parent chains alternately. It no longer builds each chain all the way to genesis. Selected parents and pairwise ancestors are memoized for the duration of one DAGKnight call.
- Results are unchanged. Equivalence tests compare the new code with a verbatim copy of the old code over hundreds of random DAGs. On a copy of the live testnet database, both versions returned the same 581-block ordering, in 66.1 s (old) and 0.54 s (new).
- Before this fix, slow ordering under the consensus lock stalled block processing, dropped IBD peers for low rate and timed out RPCs such as `GetInfo` on long chains.

## Pruning
- The acceptance-data pruning point diff now accounts for coins the previous pruning point's UTXO set already holds. Before, a byte-identical coinbase accepted again after the previous pruning point could leave an old entry in the commitment, or keep a spent coin in the served set.
- Nodes serve the pruning point UTXO set without first checking it against their own baseline. The receiving node still verifies the set against the header commitment on import.

## Mempool
- **Input minimum age.** A transaction is refused if any input is younger than `--input-min-age-daa` DAA units (default 1000, roughly 100 seconds on mainnet). For coinbase inputs, the age is counted after coinbase maturity.
- **Unconfirmed parents.** Inputs that spend outputs of transactions still in the mempool count as too young. Such transactions are refused instead of being held as orphans.
- The refusal is `RejectImmatureSpend`. The sending peer is not banned or disconnected, and the transaction isn't relayed.
- The policy applies to RPC submissions, relayed transactions, replacements and promoted orphans. Block validation and block template building are unchanged.
- `--input-min-age-daa=0` turns the policy off. `--coinbase-reorg-safety-margin` remains as a hidden, deprecated alias that overrides it when given.

## Wallet (htnwallet)
- **Coin selection.** Coins are only selected if they pass the same input-age rule. Coinbase outputs wait for maturity plus a 1000 DAA margin. On 2026-09-29, a compound transaction spent a coinbase five DAA units past maturity. A reorg then removed that coinbase, leaving the transaction spending coins that don't exist.
- **Unconfirmed outputs.** The wallet never spends unconfirmed outputs.
- **Balance.** `GetBalance` reports coins that aren't old enough yet as pending instead of available.
- **Large sends.** A send or compound that doesn't fit in one standard-mass transaction now returns an error asking you to consolidate first. Before, the wallet chained a split and a merge.
- `GetExternalSpendableUTXOs` uses the wallet's network maturity (1000 on testnet) instead of the raw parameter value.

## Mining
- Block templates are reused for 100 ms if no new block is found.

## Diagnostics
- **Disqualification report.** A block that fails `verifyUTXO` now names the failing check, the failing transaction and the selected parent. For a block disqualified by inheritance, the report names the root disqualified block (one warning per resolution step).
- **`[TX-VERDICT]` (debug level).** Logs the per-input view behind every merge set transaction rejection: resolved, SPENT or ABSENT.
- **`[ACCEPT-STATE]` (warning).** Logs the local state behind every missing-input verdict, so the logs of two nodes can be compared.
- **`[PP-COMMITMENT]`.** When a pruning point's UTXO set fails its header commitment, this report narrows down where the mismatch came from. It walks the chain between the two pruning points and compares the pruning point diff derived both ways. It also runs at startup with `--enable-utxo-debug-diagnostics`.
- **`--muhash-journal=<path>` (hidden flag).** Records every element added to or removed from each block's UTXO multiset, including block templates, as JSONL. Recording stops at 2,000,000 records.
- **`cmd/muhashjournal` (new tool).** Analyzes the journal: `verify`, `summary` (finds the block where a mismatch started), `whatif` and `diff` (miner vs. validator).
- **`utxoforensics -ppfingerprint`.** Hashes the pruning point UTXO set into partitions by outpoint prefix. With `-ppfingerprint-compare` and `-ppfingerprint-dump`, two node operators can find and exchange only the partitions that differ.


## Build and tooling
- The app build string is empty again, so the version string uses the commit hash.
- `build_and_test.sh` rejects production writes to the `dagconfig` hard-fork gates.
- The reorg stability test now checks that the honest node's multiset matches the attacker's header commitment for every side-chain block. Before, a reorg alone was taken as proof that the two nodes agreed.
- Mempool tests for chained transactions now stage parent outputs into virtual.

