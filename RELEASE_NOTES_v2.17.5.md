# HTND v2.17.5 Release Notes

Release date:  TBD

## TL;DR
- **Update your node. This release tightens consensus at block version 10, which mainnet already runs.** A block's coinbase must now match exactly the amounts this node computes, and a block whose UTXO commitment or accepted-ID merkle root this node can't reproduce is disqualified. v2.17.4 tolerated both. Nodes on older builds will keep following blocks this build disqualifies.
- **Mining pools must run this build before mining on it.** A miner whose node commits a different UTXO multiset from the rest of the network will have its blocks disqualified.
- **The node no longer stops after a streak of disqualified blocks.** It repairs the disqualified tip chains and resolves virtual again, with peers still connected. `--shutdown-on-disqualified-streak` restores the old behavior.
- **Payments carried by a disqualified block are no longer lost.** Their transactions stay in, or return to, the mempool.
- **Faster block relay, validation and wallet queries.** Pebble files are capped at 256 MB so bloom filters stay cached, `HasBlock` no longer reads the block body, and coin lookups make one database read instead of two.
- **htnwallet** can import a genkeypair key or an HTN web wallet, lists UTXOs with their derivation paths, and resubmits a broadcast the node lost.
- **New RPC `GetWalletUTXOs`** lists an HD wallet's coins from its extended public keys.

Covers everything since the v2.17.4 release.

## Consensus

### Validation rules
- **Strict coinbase from block version 10** (`StrictCoinbaseVersion = 10`). The coinbase must pay exactly the value this node computes. The v2.17.4 allowance of up to 0.1 HTN for each merge set transaction this node couldn't price is gone, and so is the allowance to underpay. Any other coinbase disqualifies the block.
- **Strict miner's-view fields from block version 10** (`StrictMinersViewFieldsVersion = 10`). A block whose only failures are its UTXO commitment or its accepted-ID merkle root is now disqualified. In v2.17.4 such a block was accepted as long as its acceptance data agreed with its UTXO diff.
- **Double spends are judged the same way on every node.** Before, a coin that virtual still held but a block's past did not was labeled a double spend. Whether that was true depended on the order in which the node had resolved blocks. So two nodes with the same DAG could store different acceptance data and different UTXO commitments for the block that merged the spend. Now an outpoint is a double spend only if an earlier transaction in the same merge set pass already spent it. Any other coin that is absent from the block's past counts as missing.
- **Pruning point advancement is checked when `RefuseMismatchedImportVersion` activates.** From that version, a node refuses to store a pruning point advancement whose locally computed UTXO commitment doesn't match the pruning point's header. This gate is still unscheduled, so nothing changes yet.

### Disqualified-block streaks
- After 15 consecutive disqualified blocks, the node used to shut down. It now resets the disqualified prefix of each tip chain, resolves virtual again and keeps its peer connections. Only one repair runs at a time.
- `--shutdown-on-disqualified-streak` keeps the old fail-stop behavior.

### Performance
- **`HasBlock` reads only the key.** Before, every call fetched and deserialized the whole block, and no caller used it. A separate hash-only cache answers the repeated asks relay makes for each peer about one announced block. On a 105 KB block, a `HasBlock` that misses the cache went from 224 µs and 468 KB of allocations to 2.1 µs and 153 B. A cache hit takes 59 ns. Database faults are reported as errors, not as "block not present".
- **One lookup per transaction input.** Block validation, the multiset base lookup, the pruning point diff and the UTXO survey each called `HasUTXOByOutpoint` and then `UTXOByOutpoint`, and the `Has` call ignored the UTXO cache. A single lookup now checks staged changes, then the cache, then the database. A cached coin went from 5.9 µs to 0.12 µs per input, and an uncached one from 12.9 µs to 10.5 µs (pebble, 200k coins).

## Database
- **Pebble target file size is capped at 256 MB.** Per-level targets used to grow to 200 times the base size, which is 12.8 GB at the last level. Each sstable has one bloom filter block, and filter blocks of large tables no longer fit in the block cache. Every point read then loaded megabytes from disk under the consensus lock. On an affected mainnet node, block relay spent 10.2 s of 11.5 s reading filter blocks and processed about 2 blocks per second instead of 5 or more.
- The target size doesn't change the on-disk format. Existing tables stay readable and are split as compaction rewrites them. Expect about 400 files per 100 GB. `HTND_MAX_FILE_SIZE_MB` overrides the cap, but not below the base file size. `HTND_PEBBLE_CACHE_MB` is still the quickest relief on an affected node.

## Mempool
- **Transactions of a disqualified block are kept.** Before, every inserted block's transactions were removed from the mempool, even when the block was disqualified and its transactions would never be accepted. The sender's coins stayed unspent, but the payment never arrived and nothing resent it.
  - A block that is already disqualified on arrival leaves the mempool untouched, and high-priority transactions are still rebroadcast.
  - A block that is disqualified later has its transactions validated back into the mempool on the next block. They return as normal priority, so they are relayed once but not rebroadcast. Transactions that were accepted some other way are skipped.

## RPC
- **New `GetWalletUTXOs`.** Takes a wallet's extended public keys and returns every coin on its addresses, with address, outpoint, UTXO entry and derivation path.
  - It scans the external (`m/0/i`) and internal (`m/1/i`) chains until `gapLimit` addresses in a row hold no coin. The default gap limit is 100 and the maximum is 1000.
  - Single-signature wallets are checked in P2PK, P2PKH and P2SH forms. Multisig wallets are checked for every cosigner, up to 20.
  - Each chain is capped at 20,000 indexes. A scan that stops at a cap reports `truncated`.
  - Results are filtered against virtual's UTXO set, as in `GetUTXOsByAddresses`. Available in `htnctl`.
- **`GetUTXOsByAddresses` is faster for large wallets.** The coins checked against virtual are sorted and read through one cursor instead of one `Get` each. 20,000 lookups in a 1M-coin set dropped from 565 ms to 137 ms, and 100,000 lookups in a 4M-coin set from 3.41 s to 0.69 s.
- **`GetTransactionStatus` checks the last 2000 chain blocks first.** Before, it scanned every block for any transaction that had left the mempool. It now looks only for an acceptance in that window, because later chain blocks reject duplicate copies of an accepted transaction. The full scan remains the fallback.
- **rpcclient decodes every `GetTransactionStatus` answer.** Pending, not-found, orphan and unknown answers carry an empty accepting-block hash. The client used to fail with "hash string length is 0" on them and break its RPC stream. The wire format is unchanged, so old and new nodes and clients stay compatible.

## P2P
- A peer with an unsupported protocol version is disconnected with a warning instead of panicking the node. The protocol version stays at 8.

## Wallet (htnwallet)
- **Import existing keys.**
  - `import-private-key` creates a wallet from a Schnorr key as `genkeypair` prints it.
  - `import-web-wallet` creates a wallet from an HTN web wallet mnemonic or from its encrypted export. Web wallet keys use the web wallet's derivation path, `m/44'/972/0'/<chain>'/<index>'`. 256 addresses per chain are imported by default, and `--num-addresses` changes that. Importing again with a larger number extends the range.
  - Imported wallets are single-signer Schnorr wallets. They can't be multisig. Offline signing works.
- **`htnwallet utxos`** lists every coin the daemon tracks, with outpoint, amount, address, derivation path and DAA score, followed by the total. It reads the same addresses as `balance`, so the two always agree.
- **Lost broadcasts are recovered.** The daemon tracks each transaction it broadcasts. From 60 s after a broadcast, it checks every 30 s whether the node still holds the transaction.
  - If the node has accepted the transaction, or reports it invalid, the inputs are released.
  - If the node has lost it, the wallet submits it again. If the resubmission is refused, the inputs are released immediately instead of after an hour.
  - Tracking ends after 2 hours.
- **Compounds reuse the UTXO set for up to 10 minutes.** Before, each compound fetched up to 10,000 coins per address, which took minutes on large wallets and set the real compound pace. Coins the daemon already spent are skipped. A settled transaction's inputs are dropped from the reused set. A refusal over inputs forces a fresh fetch. New coins can take up to 10 minutes to become available for compounding. Sends still fetch fresh coins every time.
- A change address is tracked before the change arrives on it.
- `balance -v` shows a Pending column next to Available.

## Build and docs
- `build_and_test.sh` also rejects production writes to `StrictCoinbaseVersion`.
- The README asks for Go 1.27.1 or later, installs with `-tags pebblegozstd`, describes DAGKnight and restores the Discord link.
- Regenerated protobuf descriptors now carry the `/v2` `go_package` path.
