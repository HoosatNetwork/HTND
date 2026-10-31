# Pruning journal and history worker

The c4 worker for [issue #42](https://github.com/HoosatNetwork/HTND/issues/42) runs automatically on every network, including mainnet. There is no activation option. An existing 2.18.x datadir first finishes any legacy pending UTXO update using its recorded derivation method, then initializes the journal from the complete set.

Pruning UTXO updates use private snapshots and a durable redo journal. Once the complete set passes its commitment check, a separate joined owner deletes history in batches behind a logical read mask. Pending-set refusal remains in effect. Archival nodes retain block data. The node requires Pebble snapshots and an enabled WAL; synchronous tools and unsupported backends without durable work retain the synchronous implementation. Durable work on an unsupported backend is refused.

## Diagnostics and testing

Use `htnd --pruning-diagnostics` with the usual configuration arguments to log owner consensus-lock waits and holds and journal build/cleanup and history planning/batch durations. Diagnostics default off; the disabled path reads no diagnostic clocks, allocates no diagnostic locker, and collects no observations. Existing operational completion logs remain. Reports should include the build and configuration, pruning progress, phase durations, acceptance/RPC latency, memory, snapshot lifetime, history backlog, disk/compaction growth, and restart/shutdown behavior. Report commitment mismatches, missing required data, repeated recovery errors or stalled work.

The `pruningtest` build tag adds deterministic crash barriers, lock accounting, masked-read counters and iterator statistics for tests. No environment variable activates these in a production binary. Small test batches exercise multiple crash boundaries; they do not measure production batch latency. Historical lab observation/engine-log schemas are not production telemetry.

## Disk and service limits

The default reserve policy uses a 1 GiB floor plus four times estimated unsealed job bytes. Sealed pruned jobs require the 1 GiB floor; archival jobs retain the full requirement. These values are provisional and do not reserve space against other writers. An adjacent pending move on a pruned node may use synchronous low-space fallback so its finish can free history. That fallback can hold consensus ownership for a long time. Backlogged and archival jobs wait resumably.

Batch limits do not bound derivation/planning memory, snapshot retention, history backlog or compaction space. Cancellation cannot preempt a commit/fsync, all memory-only work or the synchronous fallback. Process-kill tests do not establish power-loss safety. Production behavior has not been measured at mainnet size.

## Downgrade

Every upgraded datadir needs preparation before an older release opens it:

1. Stop the node and service restarts.
2. Run this binary with the same data directory, network, archival and retention settings, adding `--prepare-downgrade`. For example, `htnd --prepare-downgrade` on mainnet or `htnd --testnet --prepare-downgrade` on testnet. The command starts no network, RPC or profiling service.
3. Require successful exit and database close. Preparation joins owners, drains and verifies pending journal work, finishes history jobs, removes journal metadata and reconciles exact legacy block counts.
4. Start the older release only after preparation succeeds.

If interrupted, disk-refused or otherwise unsuccessful, resume preparation with this binary. Restarting this version after preparation upgrades the datadir again. There is no dependency or Pebble format upgrade.
