# The IBD UTXO failure survey

A node whose UTXO set disagrees with the network reports that disagreement badly. It names the
first block that failed and then goes quiet, for three separate reasons:

- `logToleratedIssue` emits one warn line per check label for the entire life of the process, and
  debug-logs every subsequent occurrence.
- Once a chain block is disqualified, `ResolveBlockStatus` resolves every descendant through its
  cascade branch, which never calls `verifyUTXO` at all — so the node never asks what those blocks
  would have failed on.
- `validateBlockTransactionsAgainstPastUTXO` abandons the remaining transactions as soon as one of
  them cannot resolve an input, so a block missing six coins reports one.

One failure cannot tell you whether coins are being lost or whether two nodes merely disagree about
how a coin is spelled. A whole sync's worth of failures, each carrying the outpoints involved and
every other place this node holds those outpoints, can.

The survey writes one JSON object per failing block to a file. It is off unless you turn it on, it
never changes a validation verdict, and a failure to write is logged and swallowed.

## Running it

```sh
HTND_UTXO_SURVEY=/var/log/htnd/utxo-survey.jsonl htnd --...
```

| Variable | Default | Meaning |
| --- | --- | --- |
| `HTND_UTXO_SURVEY` | unset (off) | File to append JSONL records to. |
| `HTND_UTXO_SURVEY_MAX` | `5000` | Stop after this many records. `0` means unlimited. |
| `HTND_UTXO_SURVEY_MAX_TXIDS` | `128` | Cap on each per-record transaction-ID and diff-element list. `0` means unlimited. |
| `HTND_UTXO_SURVEY_DEEP` | `0` | How many records may pay for an O(UTXO-set) recomputation of the selected parent's multiset. Each one is a multi-minute full scan on a mature chain; `1` or `2` is usually enough. |

Records are flushed on every write, so a killed node keeps everything it surveyed.

To survey a database that has already synced, reset the statuses of the chain segment you care
about to `UTXOPendingVerification` and re-resolve — a block that already has a status is never
re-verified.

## What a record says

Every record identifies the block (`blockHash`, `selectedParent`, `daaScore`, `blueScore`,
`isChainBlock`), the stage it failed at (`ibdStage`: `pruning-utxo-import` or `chain-replay`), and
every check that failed, joined — `"ErrBadUTXOCommitment+missing-input"`, not just the first.

Four fields locate the offset rather than merely reporting it:

- `headerUTXOCommitment` vs `calculatedUTXOCommitment` — what the network committed to versus what
  this node computed.
- `parentStoredMultiset` vs `parentHeaderUTXOCommitment` — whether the selected parent was *already*
  off. MuHash is homomorphic, so a parent that is off passes its offset to every descendant
  unchanged. A parent that agrees with its own header means the offset appears at this block, which
  is the one worth chasing.

`missingOutpoints` is where the coin-loss question is actually settled. For each outpoint a block
could not resolve:

- `alreadySpentInThisPast` — the outpoint is absent because this block's own past already spent it.
  Correct behaviour for a double spend, not a loss. Records with nothing but these are not findings.
- `foundInMergesetAdds` — this block's own acceptance data creates it, so a coin that should exist
  because of *this block* does not.
- `foundInParentSet` — the selected parent's UTXO view has it.
- `alternateMatches` — every place this node holds the outpoint (`virtual-utxo-set`,
  `past-utxo-diff-toAdd`, `selected-parent-diff-toAdd`, `mergeset-acceptance-output`, …) with each
  one's `serializedUTXO`: **the exact bytes that copy would contribute to a MuHash**. Two matches
  for one outpoint whose `serializedUTXO` differ are the byte-level proof that the coin exists and
  the disagreement is about its identity, not its existence.

`extraAddsNotInHeaderView` / `extraRemovesNotInHeaderView` compare the block's own UTXO delta
against the delta its acceptance data describes — two separate implementations of one set, so every
disagreement is an element the commitment will be wrong by. Read the `reason` on each:

| `reason` | Meaning |
| --- | --- |
| `add-not-in-acceptance-data` | The diff creates a coin nothing accepted creates. |
| `add-differs-from-acceptance-data` | Both create it, with different bytes. This is a handling mismatch. |
| `acceptance-output-absent-from-diff` | An accepted output never reached the diff. This is a newly-missing coin. |
| `remove-not-in-acceptance-data` | The diff spends a coin nothing accepted spends. |
| `acceptance-input-absent-from-diff` | An accepted spend never reached the diff. |

## Classification

| `classification` | What it means | Where to look |
| --- | --- | --- |
| `ORIGINAL_MISSING` | The coin is in neither the selected parent's UTXO view nor anything this block accepts. It should have arrived with the pruning point. | Pruning-point snapshot, IBD chunk transfer, deserialization, the imported multiset. |
| `NEW_MISSING` | This block's own acceptance data creates the coin, and it is not there. | Acceptance apply, coinbase collisions, `AddTransaction`, the selected-tip diff. |
| `HANDLING_MISMATCH` | The coin is present with a different `SerializeUTXO` preimage. **Nothing was destroyed.** | DAA stamping, script version, `isCoinbase`, serialization on both producer and validator. |
| `COMMITMENT_ONLY` | No spend failed; only the commitments disagree. The notes say whether the parent was already off. | The parent's multiset, or this block's own arithmetic if the parent was clean. |
| `UNKNOWN` | Not enough in the record to place it — including the benign "every absent outpoint was already spent in this block's own past". | The `notes` field. |

`HANDLING_MISMATCH` deliberately outranks both missing verdicts: a coin that is present under
different bytes is not lost, however many other symptoms accompany it.

## Clustering a run

`utxoforensics -survey` prints the whole table in one command, and needs no database:

```sh
go run ./cmd/utxoforensics -survey /var/log/htnd/utxo-survey.jsonl
```

It reports failures by error and by classification, their spread over DAA scores, whether the
pruning-point import was already offset, which block the offset *enters* the chain at (the one whose
selected parent still agrees with its own header — on a run with an offset import there is normally
none, and a block appearing here is the one worth chasing), which outpoints block more than one
block, and which of those are held under disagreeing `SerializeUTXO` preimages.

It refuses to read a malformed line rather than skipping it: every conclusion below is a count, and
a survey that quietly undercounts is worse than one that will not open.

The same questions by hand, when you want to slice them differently:

```sh
SURVEY=/var/log/htnd/utxo-survey.jsonl

# How many failures, by error type and by classification.
jq -r '.error'          "$SURVEY" | sort | uniq -c | sort -rn
jq -r '.classification' "$SURVEY" | sort | uniq -c | sort -rn
jq -r '[.ibdStage, .error, .classification] | @tsv' "$SURVEY" | sort | uniq -c | sort -rn

# Q1: one block or a dense band? (failures per 10k DAA scores)
jq -r '.daaScore / 10000 | floor * 10000' "$SURVEY" | sort -n | uniq -c

# Q2: do later failures keep spending the same coins - one loss poisoning the rest?
jq -r '.missingOutpoints[]? | "\(.txid):\(.index)"' "$SURVEY" | sort | uniq -c | sort -rn | head -20

# Q3: did it start at the import? A pruning-utxo-import record is the baseline every
#     chain-replay record has to be read against.
jq -c 'select(.ibdStage == "pruning-utxo-import")' "$SURVEY"

# Q4: is the offset created or inherited? A record whose parent agrees with its own
#     header is where the drift enters the chain.
jq -c 'select(.parentStoredMultiset != "" and .parentStoredMultiset == .parentHeaderUTXOCommitment)
       | {blockHash, daaScore, error, classification}' "$SURVEY"

# Q5: is the coin present under different bytes? This is the loss-vs-handling question.
jq -c '.missingOutpoints[]? | select(.foundUnderDifferentDAAScore or .foundUnderDifferentAmountOrScript)
       | {txid, index, alternateMatches}' "$SURVEY"

# The same question at the byte level: an outpoint whose copies do not agree on their
# MuHash preimage is a spelling disagreement, not a destroyed coin.
jq -c '.missingOutpoints[]?
       | select([.alternateMatches[].serializedUTXO] | unique | length > 1)
       | {txid, index, preimages: [.alternateMatches[] | {source, serializedUTXO}]}' "$SURVEY"

# Coins genuinely absent everywhere, excluding the benign already-spent case.
jq -c '.missingOutpoints[]? | select(.foundInParentSet == false and .foundInMergesetAdds == false
       and .alreadySpentInThisPast == false and (.alternateMatches | length) == 0)
       | {txid, index, spentByTx}' "$SURVEY" | sort -u

# Elements the block's delta and its acceptance data disagree on.
jq -r '(.extraAddsNotInHeaderView + .extraRemovesNotInHeaderView)[]?.reason' "$SURVEY" |
  sort | uniq -c | sort -rn
```

Q6 — whether two nodes that disagree on balance still hold the same outpoints with different entry
metadata — is a node-to-node question the survey cannot answer alone. `cmd/utxoforensics` compares
two databases' sets directly; the survey tells you which outpoints to compare.

## What not to conclude

- A `missing-input` error does **not** mean the coin was spent or deleted. Check
  `alreadySpentInThisPast` and `alternateMatches` first.
- A run of `COMMITMENT_ONLY` records whose parents are all already off is one offset counted many
  times, not many bugs.
- Do not change a golden hash to make a commitment match until the survey shows which preimage is
  the correct one.
