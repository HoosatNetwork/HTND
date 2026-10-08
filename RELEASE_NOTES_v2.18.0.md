# HTND v2.18.0 Release Notes

Release date:  TBD

## TL;DR
- **Update your node. This release is a mainnet hard fork that takes effect as soon as you upgrade.** 
  - IT Hard forks to block version 11 and protocol version 11 at DAASccore 236878000
  - Gated controls:
```
var mainnetHardForkGates = HardForkGates{
        StrictUTXOCommitmentVersion:    10, // Activate immediatley, current block vesrion
        StrictMinersViewFieldsVersion:  11, // Foztor - No evidence of this on mainnet, but activate with HG
        StrictCoinbaseVersion:          11, // Not seen in practice, activate and enforce at block version 11
        RefuseMismatchedImportVersion:  10, // Similar, stop the rot at v10
        ValidateHeaderBitsVersion:      10, // Stop the rot at v10
        ValidateIBDPruningPointVersion: 10, // We start this today to do the checkpoints during IBD
        ValidateIBDPruningListVersion:  10, // We start this today to do the checkpoints during IBD
        OffsetModeValueChecksVersion:   10, // Already implemented
        MLDSA44SignaturesBlockVersion:  15, // This is postponed until some later point.  Foztor. 5/Oct/27
        ParentsIncestVersion:           10, // Historical onwards below
        MergeSetSizeLimitVersion:       10,
        HeaderDAAScoreVersion:          10,
        HeaderBlueWorkVersion:          10,
        HeaderBlueScoreVersion:         10,
        HeaderPruningPointVersion:      10,
        IndirectParentsVersion:         10,
} ```
  - Nodes that do not upgrade will leave mainnet as block version 11 activates.
  - Block headers are validated in full. A header's DAA score, blue work, blue score, pruning point, indirect parents and difficulty bits must match what this node computes. Parent and merge set structure is checked as well.
  - A block's coinbase must match exactly the amounts this node computes. v2.17.4 tolerated small differences.
  - A pruning point imported during IBD, and its UTXO set, are checked against the headers.
- **Chain checkpoint** a community validated checkpoint has been added, which IBD is validated to be descendent from:
   ```
   // domain/dagconfig/params.go
    var mainnetPruningPointCheckpoint = &Checkpoint{
	Hash:           mustHash("27c1163f701f881ed90560e63031156c29d99100acc40ad019e0fadc61fb43b5"),
	BlueScore:      221022005,
	DAAScore:       233742961,
	UTXOCommitment: mustHash("f5072e6ddf17067bb05a5a99ee095fac922d285c0d0318f42275989b55b9ffff"),
    }
    ```
- **The contents of this PP are not validated, however they are correctly emebbed.  The lineage of the current PP to this checkpoint is not currently validated.
- **Mining pools must run this build before mining on it.** Blocks from an older build that break the new header or coinbase rules are rejected.
- **Block Version 11** activates at DAAScore 236878000  alongside protocol version 11 this will help segement chain splits.
- **ML-DSA-44 post-quantum signatures** are in consensus, the mempool and htnwallet. They are are dormant until block version 15 (placeholder) gets an activation DAA score, which is not set in this release.
- **The node no longer stops after a streak of disqualified blocks.** It repairs the disqualified tip chains and resolves virtual again, with peers still connected.
- **Payments carried by a disqualified block are no longer lost.** Their transactions stay in, or return to, the mempool.
- **Much faster under load.** Block and header reads and RPC transaction submissions no longer wait on the consensus lock. IBD body download is pipelined. Virtual resolution over full blocks and ML-DSA traffic is many times faster. Pebble files are capped at 256 MB so bloom filters stay cached.
- **htnwallet** gains ML-DSA-44 addresses and multisig, can import a genkeypair key or an HTN web wallet, lists UTXOs and resubmits a broadcast the node lost.
- **New RPC `GetWalletUTXOs`**, and `GetUTXOsByAddresses` no longer builds responses too large to send.

Covers everything since the v2.17.4 release. v2.17.5 was never released; its changes are included here.

## Consensus

### Header and structural checks
Seven checks inherited from upstream had been commented out with no gate. Until now, a header's DAA score, blue work, blue score, pruning point and indirect parents were accepted as the peer claimed them. From the version 10 gate:
- **`checkParentsIncest`**: no direct parent may be an ancestor of another. It now runs just after GHOSTDAG, because the gate needs the selected parent.
- **`checkMergeSizeLimit`**: the merge set may not exceed `MergeSetSizeLimit`.
- **`checkDAAScore`, `checkBlueWork`, `checkHeaderBlueScore`**: the header must match the computed value exactly. The old DAA check, when it ran, allowed a header up to 10 below the computed score and any amount above it.
- **`validateHeaderPruningPoint`**: restored. The header's pruning point must equal the one the block builder would write into a template.
- **`checkIndirectParents`**: the expected parents are built from the DAA score this node computed, not the one the header claims.
- **`checkHeaderBits`**: the difficulty bits must match the computed difficulty.

### Coinbase and UTXO commitment
- **Strict coinbase from block version 10.** The coinbase must pay exactly the value this node computes. The v2.17.4 allowance of up to 0.1 HTN for each merge set transaction this node couldn't price is gone, and so is the allowance to underpay.
- **The miner's-view toleration stays on mainnet.** A block whose only failures are its UTXO commitment or accepted-ID merkle root is still accepted on mainnet, as long as its acceptance data agrees with its UTXO diff. Mainnet ends the toleration at block version 11, which isn't scheduled. Testnet ends it at version 12.
- **Double spends are judged the same way on every node.** Before, a coin that virtual still held but a block's past did not was labeled a double spend, depending on the order in which the node had resolved blocks. Two nodes with the same DAG could then store different acceptance data and UTXO commitments. Now an outpoint is a double spend only if an earlier transaction in the same merge set pass already spent it. Any other coin absent from the block's past counts as missing.

### Pruning point import
- **A pruning point advancement is refused if its UTXO commitment doesn't match.** From the version 10 gate, a node won't store a pruning point whose locally computed UTXO commitment differs from its header.
- **The imported pruning point must be valid.** It must be on the headers selected tip's selected chain, at least pruning depth below it. A pruning point scored above the tip used to wrap around in an unsigned subtraction and pass. It now fails.
- **The imported pruning point list is checked against the headers, newest end only.** The old check failed on every DAG: it took genesis's zero-hash commitment as a list entry, and on a syncee it asked for virtual genesis's header. The new check requires the headers above the current pruning point to commit to it. The pruning point its header commits to must then be stored within the commitment window below it (6 entries on mainnet) with a lower blue score. Older entries aren't checked, because 786 of the 2,902 pruning points stored on mainnet commit to a pruning point that was never stored.
- `IsValidPruningPoint` and the list check have separate gates, `ValidateIBDPruningPointVersion` and `ValidateIBDPruningListVersion`.

### ML-DSA-44 signatures - CODE PRESENT BUT NOT YET ACTIVE
- **New opcode `OP_CHECKSIGMLDSA44` (0xa6)** verifies ML-DSA-44 (FIPS 204) signatures, using Cloudflare CIRCL. It is NOT YET SCHEDULED FOR ACTIVATION
- **P2PKH form**: `OP_DUP OP_BLAKE2B <32-byte key hash> OP_EQUALVERIFY OP_CHECKSIGMLDSA44`, spent with a 2421-byte signature plus sighash byte and a 1312-byte public key. Addresses use version byte 0x04. There is no pay-to-pubkey form, since it would make every output 1.3 KB.
- **P2SH forms**: single-sig (the P2PKH script as redeem script) and m-of-n multisig built from existing opcodes. Existing script size limits cap multisig at 2-of-11.
- The signed message is the Schnorr sighash under a separate domain, so a signature can't be replayed under another scheme.
- Below version 11, 0xa6 behaves exactly as before: an unknown opcode with 0 sigops. From version 11, only pushes of exactly 1312 or 2421 bytes are exempt from the 520-byte element limit.
- Verified signatures and parsed public keys are cached, and merge set scripts are verified in parallel before the acceptance pass. In a benchmark of 80 ML-DSA spends, one block went from 18-19 ms to 11-12.5 ms.
- Devnets can set `mldsa44SignaturesBlockVersion` in `--override-dag-params-file`. The node and the wallet daemon must use the same file.

### Disqualified-block streaks
- After 15 consecutive disqualified blocks, the node used to shut down. It now resets the disqualified prefix of each tip chain, resolves virtual again and keeps its peer connections. Only one repair runs at a time.
- `--shutdown-on-disqualified-streak` keeps the old behavior.

### Performance
- **Reads without the consensus lock.** `GetBlock`, `GetBlockHeader(s)`, `HasBlock`, block info and the nearly-synced check no longer take the consensus lock, so P2P and RPC reads don't stall block processing. These paths read the caches but never fill them, so a block being pruned can't be cached again.
- **Transaction submission verifies scripts outside the lock.** Under the lock it reads consensus state and populates UTXO entries. Script and signature checks run afterwards. A burst of ML-DSA submissions used to starve block processing.
- **Selected parent diffs cost only the outpoints a block changed.** Before, each block in a resolve chunk was diffed over the changes of every block before it in the chunk, so full blocks resolved in quadratic time. The new diff is checked against the full diff by default. Any disagreement is logged, and the full result is used. `HTND_VERIFY_SELECTED_PARENT_DIFF=0` turns the check off.
- **Block and acceptance data caches live off the Go heap.** The block cache was bounded only by count (10,000 blocks), and ML-DSA inputs carry about 3.7 KB each. On testnet it reached 3.1 GB and the node spent about 60% of its CPU in garbage collection. The caches now hold serialized values outside the Go heap, under byte budgets: `HTND_BLOCK_CACHE_MB` (default 256) for blocks, 128 MB for acceptance data.
- **Script parsing allocates by opcode count.** It used to reserve one opcode per byte, about 120 KB for every ML-DSA signature script. This was half of all allocations during a slow testnet resolve.
- **DAA window minimum timestamp** compares blue work in place instead of copying a big integer per block. This was 28% of CPU during header sync.
- **`HasBlock` reads only the key.** A cache miss went from 224 µs and 468 KB of allocations to 2.1 µs. Database faults are reported as errors, not as "block not present".
- **One lookup per transaction input**, consulting the UTXO cache. A cached coin went from 5.9 µs to 0.12 µs per input.
- **`HTND_LARGE_CACHE_DIVISOR` divides by its own value.** A leftover shift made `HTND_LARGE_CACHE_DIVISOR=2` divide the large caches by 2,097,152, which could make post-IBD virtual resolution take hours. Nodes that don't set it are unaffected.
- **Progress while draining pending virtual.** After a restart mid-resolve, the first block or template request drained the whole backlog silently. It now logs progress at most every 10 seconds.

## IBD
- The next block body batch is requested as soon as the current one has arrived, before it is processed. Before, the two nodes took turns, with gaps of 2-6 seconds between batches. At most two batches are in flight.

## Database
- **Pebble target file size is capped at 256 MB.** Per-level targets used to grow to 12.8 GB at the last level, and their bloom filter blocks no longer fit in the block cache. On an affected mainnet node, block relay spent 10.2 s of 11.5 s reading filter blocks. Existing tables stay readable and are split as compaction rewrites them. `HTND_MAX_FILE_SIZE_MB` overrides the cap.

## Mempool
- **Transactions of a disqualified block are kept.** A block that is already disqualified on arrival leaves the mempool untouched. A block that is disqualified later has its transactions validated back into the mempool on the next block, as normal priority.
- **ML-DSA-44 spends are standard.** The standard signature script limit rises from 1650 to 7977 bytes, enough for a 2-of-11 ML-DSA multisig. Before activation, consensus still refuses these spends.

## RPC
- **New `GetWalletUTXOs`.** Takes a wallet's extended public keys and returns every coin on its addresses, with its derivation path. It scans both chains until `gapLimit` (default 100, max 1000) unused addresses in a row, covers single-sig P2PK, P2PKH and P2SH and multisig up to 20 cosigners, and reports `truncated` at the 20,000-index cap. Available in `htnctl`.
- **`GetUTXOsByAddresses` and `GetPaginatedUTXOsByAddresses` cap their responses to one RPC message.** A wallet with millions of coins made the node build a 2.37 GB response that gRPC then refused, every poll. The node now returns the first coins that fit, in index order, and logs the cut. The response has no truncated flag. `GetBalanceByAddress` still reports the full balance.
- **Faster UTXO checks against virtual.** Coins are read in key order through one cursor with prebuilt keys. 100,000 lookups in a 4M-coin set went from 3.41 s to 0.69 s.
- **`GetUsableAddresses` stops at the first spendable coin.** It reads 1 coin, then 32, then all, only while every coin so far is withheld. On a busy node this handler had been about 60% of non-GC CPU.
- **`GetTransactionStatus` checks the last 2000 chain blocks first**, and only then falls back to the full scan.
- **rpcclient decodes every `GetTransactionStatus` answer.** Pending, not-found and orphan answers used to fail with "hash string length is 0" and break the RPC stream. The wire format is unchanged.

## P2P
- A peer with an unsupported protocol version is disconnected with a warning instead of panicking the node. The protocol version stays at 8.

## Wallet (htnwallet)
- **ML-DSA-44 addresses.** `new-address --address-type mldsa44` (also `mldsa44-p2pkh`) or `mldsa44-p2sh`. Multisig up to 2-of-11. The command refuses until the node is past the activation version.
  - Keys are derived from the BIP39 seed with a keyed BLAKE2b, never through secp256k1, so breaking secp256k1 doesn't reveal them. The mnemonic still restores everything.
  - The daemon is watch-only and ML-DSA keys have no public derivation, so `create` precomputes 500 key hashes per chain into `keys.json`. `generate-mldsa44-keys` fills or extends the pool of an older wallet. Multisig cosigners exchange pools with `export-mldsa44-keys` and `import-mldsa44-keys`.
  - Change goes to an ML-DSA-44 address when every input is ML-DSA-44.
  - Older htnwallet binaries refuse a keys file with an ML-DSA-44 section.
- **Import existing keys.** `import-private-key` takes a key as `genkeypair` prints it. `import-web-wallet` takes an HTN web wallet mnemonic or encrypted export, 256 addresses per chain by default (`--num-addresses`).
- **`htnwallet utxos`** lists every coin with outpoint, amount, address, derivation path and DAA score.
- **Lost broadcasts are recovered.** From 60 s after a broadcast, the daemon checks every 30 s whether the node still holds the transaction. It resubmits a lost one, and releases the inputs of an accepted, invalid or refused one. Tracking ends after 2 hours.
- **Faster compounds and syncs.** Compounds reuse the UTXO set for up to 10 minutes. Syncs no longer re-derive every recent address every two seconds; a batch of 1000 indexes went from about 590 ms to a one-time 205 ms.
- **Auto-compound always reuses the change address**, so it can't walk past the ML-DSA key pool. `-u` is hidden and accepted for existing command lines.
- A change address is tracked before the change arrives on it. `balance -v` shows a Pending column.

## Mining (htnminer)
- Each template is solved once. Before, every thread kept mining a template after a solution, submitting sibling blocks that could not all join the chain.
- Threads copy the PoW state once per job instead of once per hash.
- Template notifications are no longer dropped, and a successful submit refreshes the template at once.
- `--target-blocks-per-second` is one limit for the miner, not per thread. A rejected block is logged instead of stopping the miner.

## Tools and configuration
- **`--profile` takes `host:port`**, so two nodes on one machine can each serve pprof. A bare port keeps listening on every interface.
- **`utxoforensics -pplistcheck`** runs the imported pruning point checks offline against a datadir copy and prints every stored pruning point with the index its header commits to.

## Build and docs
- Many of the test scripts have been temporarily disabled whilst they are repaired to a level that satisifies CI
- `build_and_test.sh` rejects production writes to any hard-fork gate, except `MLDSA44SignaturesBlockVersion`, which a custom network's config may set.
- `docs/script-engine.md` describes ML-DSA-44 signatures, its P2SH forms and multisig limits.
- `ISSUES.md` and `docs/REMEDIATION_STATUS.md` record the HTN-006 pruning list defect and the mainnet measurements.
- Protobuf code is regenerated with protoc 36.0. Descriptors carry the `/v2` `go_package` path.
- The README asks for Go 1.27.1 or later and installs with `-tags pebblegozstd`.
