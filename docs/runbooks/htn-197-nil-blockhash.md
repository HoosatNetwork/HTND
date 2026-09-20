# Runbook: every header rejected with "blockHash is nil"

**Symptom.** During header IBD, every incoming header is rejected with `blockHash is nil`. The sync
never progresses. Restarting does not help, and the node retries the identical failure on every
insert with no backoff.

**Status: this is a known, unfixed stall (HTN-197).** This runbook tells you how to confirm you have
it, how to get out of it, and what evidence to capture — because the fix is still blocked on exactly
the information a live occurrence would provide.

---

## Confirm it is this

The decisive evidence is the line **immediately before** the first `blockHash is nil`:

```sh
docker logs htnd 2>&1 | grep -B5 "blockHash is nil" | head -40
```

You are looking for:

```
Calculating pruning points diff failed <reason>. Falling back ...
```

naming an **acceptance-data** failure. That line, followed by `blockHash is nil` on every subsequent
header, is the signature.

Two properties distinguish this from a transient error, and both are confirmed from the code rather
than guessed:

- **It repeats on every header, by construction.** `UpdatePruningPointIfRequired` runs after every
  insert, header-only included, while `HadStartedUpdatingPruningPointUTXOSet` is set. That flag is
  cleared only inside `updatePruningPoint()` on success. An error leaves it set.
- **IBD never proceeds once triggered.** Same reasoning: the flag never clears, so every header hits
  the same walk and is rejected. This is permanent for that sync attempt, not a retry that
  eventually succeeds.

If your log shows the error intermittently, or sync continues afterwards, you have something else.

---

## Mechanism

`validateAndInsertBlock` calls `UpdatePruningPointIfRequired` after every insert. With
`HadStartedUpdatingPruningPointUTXOSet` set, it runs `updatePruningPoint`:

1. The acceptance-data diff fails first — a selected-chain block between the previous and current
   pruning point has no acceptance data.
2. The fallback, `calculateDiffBetweenPreviousAndCurrentPruningPoints`, walks `UTXODiffChild`.
3. In HTND, `UTXODiffChild` returns `(nil, nil)` for "no child recorded" — both for the staged-nil
   case and for `database.ErrNotFound`. This is deliberate HTND behaviour and documented in that
   function; kaspad returns not-found instead.
4. That `nil` is passed to `ghostdagDataStore.Get`, which returns a clean
   `errors.New("blockHash is nil")`.
5. The error propagates, the header is rejected, and the next insert retries the same walk.

So the walk hits a legitimate "no child yet" and mistakes it for "the walk cannot continue". It
fails cleanly but wrongly — there is no crash and no corruption risk here.

**Why a nil check is not the fix.** Adding one only changes the message. The real question is why
the flag is set while the chain data between pruning points is missing, and the answer determines
whether the update should be deferred until the data exists or should surface a different error.

Current theory, **not confirmed**: the preceding acceptance-data failure is itself the signature of a
header-only block in that range, since acceptance data only exists once a block is UTXO-resolved.
That is the same shape HTN-196 fixed — header-only blocks whose bodies never arrive because the
syncer's chain and this node's pruning point do not reconcile. If so, HTN-196's fix may reduce how
often this state is reached, because it stops IBD from silently "succeeding" while stuck on such a
gap. Nobody has verified this against a fresh occurrence.

---

## Getting the node running again

There is no in-place repair. The stuck state is a pruning-point UTXO set update that was started and
cannot finish, and no repair flag addresses it — `--repair-missing-multisets` and
`--repair-block-statuses` both operate on block statuses and multisets, not on this.

**Before you destroy the evidence, read the next section.** A resync is currently the only way out,
and it takes the reproduction with it.

Resync from scratch:

```sh
docker compose down
docker volume rm <project>_htnd-data     # or: rm -rf <appdir>/hoosat-mainnet/datadir
docker compose up -d
```

Faster alternative if you have one: restore a known-good datadir snapshot from before the stall, or
import an Exodus pruning-point bundle (`docs/exodus-pruning-point.md`).

---

## Capture this first — it is what the fix is blocked on

HTN-197 has sat at `needs_info` because it has only ever been seen once, on a node that was not
available for inspection, on a build (`993c95892`) that predates several pruning-manager changes. If
you are looking at it now, you have something nobody has had since.

Please capture, **before** wiping:

1. **A copy of the datadir**, taken safely. Stop the node first if you can. If you cannot:
   `cp -al` the live datadir into a directory on the same filesystem, copy *that* elsewhere, then
   remove the link directory. A plain `cp` of a live pebble datadir is torn — compaction deletes
   SSTs mid-copy and the manifest then references a missing file. **Never open the hardlinked
   directory itself**; its inodes are the live node's.
2. **The node's exact version and commit** — `htnctl get-info`, or the `Version` line at startup.
   The one existing report's stack line numbers did not match HEAD, which is why its mechanism could
   only be reconstructed by reading code.
3. **The full log from startup to the first `blockHash is nil`**, not just the repeating tail. The
   `Calculating pruning points diff failed` line and everything before it is the part that matters.
4. **Whether this node had previously completed a headers-proof IBD**, and whether it had been
   restarted mid-sync.
5. **The peer it was syncing from**, if the log names one.

File that against HTN-197. With a datadir copy the walk can be replayed offline against a
`TestConsensus` instance, which is the step that has never been possible.

---

## Related

- **HTN-196** — fresh headers-proof sync getting stuck because the imported pruning point is not on
  the headers tip's selected chain. Fixed; possibly reduces how often HTN-197's state is reached.
- **HTN-204** — a freshly synced node mining at genesis difficulty because a block whose selected
  parent was pruned never gets its trusted DAA window. Different symptom, same neighbourhood; the
  recorded fix is **not** safe to re-apply.
