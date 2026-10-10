# HTND 2.19-cryptonoob-rc

First release of this line. Not a continuation of the Foztor tip.

There is no v2.18.1 tag. The last tagged Foztor release is v2.18.0, commit 15126b9. The latest commit on foztor-next-rc is 9370ac289 (8 October 2026). Master is two documentation commits past the 2.18.0 merge, at c097146c6.

That tip accepts a pruning-point UTXO set after a header check. It does not require the set to hash to the UTXO commitment in the header. This line rejects that set. The histories do not join. The split is the ledger rule, not the block number.

## Restored validation

Kaspa, and Hoosat before the check was relaxed, treat the UTXO set as the ledger state and the header UTXO commitment as its hash. The Foztor line treats the header as sufficient. This release restores the set check.

1. Coinbase must match. The subsidy and fees must equal the value computed from the UTXO set the node holds. A set that does not reproduce is not used for that computation.
2. Pruning-point UTXO commitment is mandatory. A set whose multiset does not hash to the header commitment returns ErrBadPruningPointUTXOSet. Import stops.
3. blockInheritsKnownUTXOCommitmentOffset returns false. A known offset no longer excuses the next block.
4. The pruning-point list must form a chain back to genesis. ArePruningPointsInValidChain rejects a list that stops short. This is not a flag.
5. A missing input at pruning-point import is an error. The IBD anchor is not exempt from the header check.
6. Protocol version is locked at 11. A peer on version 8 or 10 is banned for 120 minutes. No flow is registered for any other version.
7. Startup refuses a datadir whose pruning point is not genesis and whose stored multiset does not match the header commitment. A substituted datadir2 does not start.
8. A mismatched pruning-point UTXO set is banned for 120 minutes without --enablebanning. The address does not return immediately.
9. A finality violation during pruning-proof IBD bans the peer for 120 minutes. The node selects the next peer at once.
10. A closed route during pruning-proof IBD is treated as a bad peer when the proof was already inconsistent, not as a clean disconnect.
11. Live-chain gate. A synced node does not start a destructive IBD against a mature tip. The gate uses tip time and process age.
12. The pruning proof is checked before any staging write. An empty or inconsistent proof is rejected. Staging left by a failed IBD is deleted before the next attempt.
13. Permanent --addpeer entries cannot be banned. After 60 seconds with no block the node reconnects the addpeer list.
14. Status heartbeat. The log reports whether the node is requesting blocks, has received one, or is at the tip. Ban lines are rate-limited.
15. The relay offense counter bans from handle_relay_invs.go. An invalid inv, a block without proof of work from BanMinVersion, or a wrong block version bans without --enablebanning.
16. Proof size is capped at 1 GiB. An oversized proof is refused before it is applied.
17. DisallowDirectBlocksOnTopOfGenesis remains true on mainnet. A node that holds only genesis does not treat orphans as a normal IBD.
18. SubmitBlock requires the node to be out of IBD and nearly synced. A node with no peers does not accept locally mined blocks.
19. GetBlockTemplate reports isSynced from peer count and the nearly-synced check, not only from the IBD flag.
20. Archival mode walks every selected parent to genesis. Without the flag the node follows pruning-point links. Both paths still require genesis and a matching stored list. The jump does not relax the commitment check.
21. Coinbase maturity is a reorg margin for a fresh coinbase output. It is not a ledger repair and it does not replace the UTXO commitment.
22. The utxobase user-agent tag is not a permission. Import still has to reproduce the header commitment.
23. The DNS seed list is the operator's. A reduced seed list does not change the ledger rule.
24. The node speaks only protocol version 11. A later upgrade opens the window before the version moves. Older 2.17.3-x peers on version 11 sync. Peers on version 8 or 10 do not.

## Why a separate branch

A commit on the Foztor tip would remain on a base this rule rejects. The UTXO set that tip holds does not hash to its header commitment. This code will not adopt it. The two lines meet only where every node can still reproduce the same UTXO set: before the split, or a new baseline that every node imports together.

## Build

Go 1.27.1 or later. CI builds linux/amd64, linux/arm64, windows/amd64 and darwin when a release is published.

Linux:

    go install -ldflags="-s -w" --tags="pebblegozstd" . ./cmd/...

Windows needs TDM-GCC and the deadlock tag:

    go install -ldflags="-s -w" --tags="deadlock pebblegozstd" . ./cmd/...

The binary reports 2.19-cryptonoob-rc.
