# 2.17.3-15

Changes from 2.17.3-14.

SubmitBlock rejects a block when the node has no peers or is not nearly synced. Lack of IBD is not enough. The exception remains --allow-submit-block-when-not-synced.

GetBlockTemplate reports isSynced only when the node is nearly synced, IBD is not running, and at least one peer is connected. A miner does not get a synced template on an isolated node.

The /utxobase:ok|bad|unknown/ token is no longer added to the user agent and is not a permission to supply the pruning-point UTXO set. Import still has to reproduce the header commitment. A set that does not returns ErrBadPruningPointUTXOSet.

POWScores, protocol 11, and the pruning-point list check are unchanged from 2.17.3-14.
