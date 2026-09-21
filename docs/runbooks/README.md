# htnd runbooks

Operational runbooks for symptoms seen on live Hoosat nodes. Each one starts from what an operator
actually observes — a log line, a pool dashboard, a stalled sync — and works back to the mechanism.

| Runbook | The symptom that sends you here |
|---|---|
| [htn-002-disqualification.md](htn-002-disqualification.md) | "UTXO commitment" / disqualification warnings; the node stops advancing |
| [stratum-zero-hashrate.md](stratum-zero-hashrate.md) | Pool shows 0 H/s, "Mining difficulty 0.000000", miners connect and drop |
| [nearly-synced-lag.md](nearly-synced-lag.md) | Node reports nearly synced but block timestamps run minutes behind wall clock |
| [htn-197-nil-blockhash.md](htn-197-nil-blockhash.md) | Every header rejected with "blockHash is nil"; IBD never progresses |

## Rules that apply to all of them

**Measure before changing anything.** Every one of these symptoms has at least one plausible cause
that is not the obvious one, and two of them were originally misdiagnosed. The runbooks say which
measurement discriminates.

**Repair flags are one-shot.** `--repair-block-statuses` and `--repair-missing-multisets` are
recovery steps, run once, watched, and then removed from the launch command. Leaving
`--repair-block-statuses` in a compose file is what caused the outage described in
[stratum-zero-hashrate.md](stratum-zero-hashrate.md). Since this branch, htnd warns loudly at every
start when either flag is set. See [`deploy/`](../../deploy) for compose and systemd examples that
keep the one-shot runs separate from the steady-state node.

**Never run an offline tool against a live datadir.** `utxoforensics`, `ldbtool` and
`htnexodus import` open datadirs directly, and pebble replays its WAL on open. Work on a copy.
Copying a live pebble datadir with a plain `cp` produces a torn copy — compaction deletes SSTs
mid-copy and the manifest then references a missing file. Snapshot with `cp -al` into a directory on
the same filesystem first, then copy that, then remove the link directory. Never open the
hardlinked directory itself: its inodes belong to the live node.

**A node that is behind is not necessarily stuck.** Check whether a counter is moving before
concluding anything has hung. "Resolving virtual. Estimated progress: N%" climbing, or accepted
block counts rising, means slow, not stalled — and the remedy for slow is different.
