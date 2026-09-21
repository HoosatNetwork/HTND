# Runbook: the node says it is nearly synced but runs minutes behind

**Symptom.** The node is past the `IsNearlySynced` threshold, so it serves templates and answers as
though it were caught up, but the block timestamps in its own log run minutes behind wall clock at
the moment each line is printed. Throughput sits at a few blocks per second against a network doing
considerably more.

The observed case: ~2–3 blocks/s processed per 1.1–1.5s slice, with logged block timestamps ~7.5
minutes behind wall clock.

---

## First: is it slow, or is it stuck?

These need completely different responses, and the difference is one grep.

```sh
docker logs htnd --since 5m 2>&1 | grep "Estimated progress"
```

If "Resolving virtual. Estimated progress: N%" is **climbing**, the node is working through a
backlog. It is slow, not stuck, and it will finish. This exact case was misdiagnosed once as an
infinite loop in `ResolveVirtual`; the log showed steady progress the whole time.

If nothing is moving at all, this is the wrong runbook — see
[htn-197-nil-blockhash.md](htn-197-nil-blockhash.md) for a genuinely permanent IBD stall.

---

## Why nearly-synced is slower than bulk IBD

This is architectural and worth understanding before chasing it as a bug.

`AddBlock` takes an `updateVirtual` parameter. On the nearly-synced live path it is **true**, so
every block pays for a full virtual update, including `pickVirtualParents`. Bulk, far-behind IBD
sets it false and defers that work to a single `ResolveVirtual` at the end.

So "bulk IBD is fast, nearly-synced is slow" is expected, not a regression. The node is doing
strictly more work per block once it is close to the tip.

---

## Measure before changing anything

Take a CPU profile from the live node. This is read-only and does not disturb it:

```sh
# Requires HTND_PROFILER=1 (see deploy/htnd.service)
curl -o /tmp/htnd.prof http://127.0.0.1:6060/debug/pprof/profile?seconds=20
go tool pprof -top /tmp/htnd.prof | head -30
```

In the investigated incident, **GC background marking alone was 52.88% of all sampled CPU**, at 341%
average core utilisation. The node was busy, but burning most of it on allocation pressure rather
than on block processing. That is the signature to look for: if GC dominates, the fix is whatever is
allocating, not scheduling or I/O.

Two independent allocation-heavy paths accounted for it, both since fixed:

| Path | Share of total CPU | Fixed by |
|---|---|---|
| `utxoindex.UTXOs` scanning its cursor twice per unlimited query | 15.25% | HTN-214 |
| `pickVirtualParents` → `mergeSetIncrease` re-walking reachability per candidate | ~18–19% | HTN-215 |

`utxoindex.UTXOs` scanned once to count and once to fill, although a per-script count was already
maintained in the same commit as the entries. It is reached from
`HandleGetBalancesByAddresses`/`HandleGetUsableAddresses` — so ordinary address-index RPC traffic was
competing directly with block processing for CPU and GC budget.

`pickVirtualParents` called `IsAncestorOfAny` once per BFS-visited ancestor, per candidate. It now
memoises positive answers for the duration of one call.

**If you are running a build that has both, and GC still dominates, you have found something new.**
Profile first and record what the top entries actually are rather than assuming it is one of these.

---

## Things that look like the cause and are not

**Address-index RPC load.** A large `GetBalancesByAddresses` query scans virtual's UTXO set and, on
older builds, evicted everything block processing had warmed from the shared LRU (HTN-207). If
85–98% of your RPC traffic is address-index queries — which it was on the node in question — this
contributes, but it is a serving-path cost, not a consensus one. Reducing that traffic helps
throughput without changing any validated value.

**"Pending tip does not overcome previous selected parent" warnings.** A block losing the tip race
to an already-valid, heavier chain is routine GHOSTDAG/DAGKnight behaviour. These were downgraded to
debug in HTN-218 precisely because they were being read as a fault. If your build still logs them at
warn, they are noise, not a symptom.

**The reachability reindex root.** If reachability appears anywhere near the top of a profile, check
whether the reindex root is still at virtual genesis after a pruning-proof IBD (HTN-205). That made
one node process 0.8 blocks/s where it should have done 26.8, and it is a state collapse rather than
a code path being slow — the discriminator is whether the chain has exhausted the interval space it
inherited, **not** the size of the reachability tree. Getting that backwards produced a confident
but wrong refutation during the original investigation.

---

## What to do

1. Confirm progress is moving. If it is, the backlog will drain — let it.
2. Profile. If GC dominates, find the allocating path; do not tune the scheduler.
3. Check you are on a build with HTN-214 and HTN-215.
4. If address-index RPC is a large fraction of traffic, consider whether that load belongs on the
   same node that is trying to keep up with the tip.
5. Only then look for something new — and record the profile, because the two most confident
   explanations offered during the original investigation were both wrong.
