# Remediation status — verified against the tree

Branch: `remediation/sept-2026`. Baseline commit: `2fa0f265a` (identical to `origin/master`).
Verification date: 2026-09-20.

## Read this first

**Nothing recorded in this document changes the validity of any already-mined block.** Every item
marked "verified fixed" below was already in the tree at the baseline commit and is already live.
The consensus work this branch adds (Workstream C) is gated behind a block version that **does not
exist in any network's `POWScores` table**, so it evaluates to "off" for every block that can be
produced today. If that ever stops being true, this section is where it must be said, in bold, at
the top.

## Source-document gap

`docs/HTND_Remediation_and_HardFork_Plan.pdf` **does not exist** — not in `docs/`, not anywhere on
this host, and not in any commit on any branch (`git log --all --diff-filter=A`). The plan was
therefore reconstructed from the task prompt (which enumerates every workstream, hard rule and
ticket ID) plus `ISSUES.md` and `AGENT_STATE.md`.

Consequences, all flagged rather than guessed at:

- The "claimed status" column below is the status recorded in `ISSUES.md`, not the PDF's own claim.
- Section 11's runbook list is taken from the prompt's four names (HTN-002 disqualification, stratum
  0 H/s, nearly-synced lag, HTN-197 nil `blockHash`). Any fifth runbook the PDF named is missing.
- "Step 0.1", "Step A1" (the seven predicate questions) and "Step A2" (checkpoint authority) are
  referenced by the prompt but their contents are not recoverable. They are listed under
  **needs a human** and were not invented.

## Method

Each row was verified by reading the named code path, not by trusting a status line. Where the
status line and the code disagree, the code wins and the discrepancy is called out — `AGENT_STATE.md`
already warned that `ISSUES.md` status fields go stale, and this pass found **eight** more.

## Status table

| ID | Claimed status (ISSUES.md) | Actual status (verified in code) | Evidence (file:symbol) | Action |
|---|---|---|---|---|
| HTN-001 | fixed | **Fixed** — depth/rule params read the DAG-anchored version; global used only before either block has a DAA score | `domain/consensus/utils/blockversion/current.go:Current` | none |
| HTN-003 | fixed | **Fixed** — managers derive version from their own DAA score | `.../blockversion/current.go:OfSelectedParent`; `ghostdagmanager/ghostdag.go:416`, `mergedepthmanager/merge_depth_manager.go:25`, `difficultymanager/difficultymanager.go:346` | none |
| HTN-002 | needs_human (deferred by user) | **Open by design** — `tolerate` swallows RuleError on 4 checks | `consensusstatemanager/verify_and_build_utxo.go:blockInheritsKnownUTXOCommitmentOffset`, `:stop` | Workstream C-1 (gated) |
| HTN-004 | open | **Open** — per-tx rule errors degrade to a rejection record, not a block failure | `consensusstatemanager/calculate_past_utxo.go:394` | Workstream C-1 (gated) |
| HTN-005 | needs_human | **Open** — `refuseMismatchedImportedPruningPointUTXOSet` exists but is wired only to the operator flag `--enable-sanity-check-pruning-utxo`, default off; no version gate | `consensusstatemanager/import_pruning_utxo_set.go:290`; `infrastructure/config/config.go:162` | Workstream C-2 (gated). **Must not** be enabled for existing versions (hard rule 1) |
| HTN-006 | open | **Open** — `IsValidPruningPoint` / `ArePruningPointsInValidChain` commented out | `blockprocessor/validate_and_insert_imported_pruning_point.go:13-29` | Workstream C-4 (gated) |
| HTN-007 | open | **Open, confirmed** — `StageDAAData` stages but never compares to `header.Bits()`; the comment above it claims a check that does not exist; `ErrUnexpectedDifficulty` is declared and unused in production code | `blockvalidator/pruning_violation_proof_of_work_and_difficulty.go:78-83`; `ruleerrors/rule_error.go:45` | Workstream C-3 (gated) |
| HTN-115 | needs_human | **Fixed** — capped at `4*495`, enforced both directions with tests | `app/appmessage/p2p_msgrequestibdblocks.go:16`; `protowire/p2p_request_ibd_blocks.go:19,31` | correct stale status |
| HTN-146 | needs_human | **Fixed** — priority propagated into the promoted tx | `mempool/orphan_pool.go:242`; `unorphan_priority_test.go` | correct stale status |
| HTN-153 | needs_human | **Fixed** — temp file + fsync + rename + dir fsync, with the Windows split from HTN-223 | `cmd/htnwallet/keys/keys.go:293-346`; `syncdir_unix.go`, `syncdir_windows.go` | verify *every* save path; add crash test if a path is uncovered |
| HTN-159 | needs_human | **Fixed** — appends to the default list and validates with `DecodeAddress` at startup | `app/component_manager.go:mergedAndValidatedFrozenAddresses` (called :176) | correct stale status |
| HTN-162 | needs_human | **OPEN — confirmed security defect.** `VerifyChecksum` and `VerifyFileSize` exist but have **zero callers**. The download→install path verifies nothing, and there is no signature verification of any kind | `infrastructure/autoupdate/downloader.go:159,195` (dead); `updater.go:233 downloadUpdate` → `:306 installUpdate` | Workstream A — implement |
| HTN-164 | needs_human | **OPEN** — `os.Exit(0)` in `RestartNode`, bypassing app shutdown | `infrastructure/autoupdate/updater.go:721` | Workstream A — implement |
| HTN-166 | needs_human (partial) | **Partly addressed** — limits are now settable (`--p2p-max-message-size`, `--rpc-max-message-size`), defaults deliberately unchanged. The public/authenticated endpoint split is still open | `grpcserver/rpcserver.go:SetMaxMessageSizes`; `netadapter.go:NewNetAdapter` | see "judgment calls" |
| HTN-168 | needs_human | **Fixed** — neither path bans for this node's own errors | `flowcontext/errors.go:39-63`; `blockrelay/handle_request_anticone.go:52-62` | correct stale status |
| HTN-177 | needs_human | **OPEN** — 27,607,704-byte ELF still committed, no source | `tools/pebble-tool/pebble-tool` | Workstream A — delete |
| HTN-204 | REOPENED, do not re-apply | **Open, correctly reopened** — fix is not in the tree | — | Workstream D — design only |
| HTN-213 | open, not fixed | **Fixed** — `sendMutex` held around `Post`, the send loop and `CloseSend`; `-race` test present | `grpcclient/grpcclient.go:39,92,145`; `post.go:55`; `send_disconnect_race_test.go` | correct stale status |
| HTN-214 | fixed | **Fixed** — sizing pass reads the maintained per-script count; one fill scan | `domain/utxoindex/store.go:UTXOs` (:646-665) | none |
| HTN-215 | fixed | **Fixed** — `knownPastOfSelectedVirtualParents` memoizes within one call | `consensusstatemanager/pick_virtual_parents.go:71-83,316` | none |
| HTN-217 | fixed | **Fixed** — `*ForCurrentVersion` helpers clamp; clamp tests exist. Production code has **no** raw `GetBlockVersion()-1` indexing (remaining hits are `_test.go` only) | `dagconfig/params.go:blockVersionIndexForSlice` (:205); `params_test.go:TestPerVersionTablesAreClampedPastTheirEnd` | add the CI grep ban (not present) |
| HTN-219 | fixed | **Fixed** — listener removed on transport disconnect; the no-op overwrite is gone | `app/rpc/rpc.go:91-97`; `netadapter/netconnection.go:99-104` | none |
| HTN-220 | fixed | **Fixed** — identical `(lowHash, highHash)` served from cache | `blockrelay/handle_request_headers.go:29-33,102` | none |
| HTN-221 | fixed | **Fixed** — `actualTimeSpan` clamped to 4× expected; ungated by explicit user decision | `difficultymanager/timespan_clamp_test.go` | none |
| HTN-226 | fixed | **Fixed** — the four remaining tables go through clamped helpers | `dagconfig/params.go:231-249` | none |
| HTN-227 | fixed | **Fixed** — repair passes disabled on staging consensus | `domain/domain.go:82-83` | none |
| HTN-229 | (stratum bridge) | **Not in this repo** — `htn-stratum-bridge` is a separate repository, absent here | — | Workstream F — write `docs/bridge-HTN-229.md`, do not guess at its code |

### Workstream B / E / F items (not ticket-numbered)

| Item | Actual status | Evidence | Action |
|---|---|---|---|
| `RepairMissingMultisets` one-shot | **Exists**, flag-gated, with two tests | `app/component_manager.go:152`; `domain/consensus/repair_missing_multisets_test.go` | confirm the log line wording |
| `--repair-block-statuses` loud warning on normal run | **Missing** | `infrastructure/config/config.go` | Workstream B — implement |
| Repair flags in deploy artefacts | **Clean** — no compose file exists in-repo; `Dockerfile` has no repair flag | `Dockerfile` | Workstream B — add clean compose + one-shot migration examples |
| systemd unit | **Absent** | — | Workstream B — add sample |
| `docs/runbooks/` | **Absent** | — | Workstream B — write 4 runbooks |
| `utxoforensics` canonical artefact tool | **Done** — `-canonical` / `-canonical-out` | `cmd/utxoforensics/canonical/`, `canonical_artifact.go` | see Workstream E below |
| README docs URL | **Broken** — `https://github.com//Hoosat-Oy/docs` (double slash) | `README.md:99` | Workstream F — fix |
| README Discord link | **Empty** | `README.md:88-90` | Workstream F — TODO, do not invent a link |
| `letfhook.yml` typo file | **Present**, 75 bytes, alongside the real `lefthook.yml` | `letfhook.yml` | Workstream F — remove |
| CI: two-node construction test + `-race` grpcclient | **Not in CI** | `.github/workflows/tests.yaml` | Workstream F — add |

## Discrepancies found between ISSUES.md and the code

Eight tickets are recorded as `needs_human` / `open` but are **fixed in the tree**, all by commits
that landed before this branch: HTN-115, HTN-146, HTN-153, HTN-159, HTN-168, HTN-213 (recorded
"open, not fixed"), plus HTN-214/215 whose status lines were already correct but whose evidence
blocks still describe the pre-fix code.

Direction of error matters: every discrepancy found was a *pessimistic* status line (work done,
record not updated). None claimed a fix that was absent. The one inverse case is HTN-162, whose
`needs_human` status is accurate but understates the finding — the verification functions exist,
which reads like partial progress, but they are dead code, so the defect is total, not partial.

## Workstream C: the four gated rules

All four are implemented in `domain/consensus/utils/hardforks` and wired at their call sites. **Every
one is unscheduled**, keyed at `math.MaxUint16`, which no network can reach — mainnet's highest
producible version is `len(POWScores)+1 = 10`.

| Predicate | Ticket | Where it bites | Anchored on |
|---|---|---|---|
| `StrictUTXOCommitmentVersion` | HTN-002 / HTN-004 | `verifyUTXO` stops tolerating RuleErrors from the four UTXO checks | selected parent's DAA score (`versionOfChildOf`) |
| `RefuseMismatchedImportVersion` | HTN-005 | import fails closed; `GetPruningPointUTXOs` refuses to serve a mismatched set | pruning point header's DAA score |
| `ValidateHeaderBitsVersion` | HTN-007 | `header.Bits()` must equal the computed required difficulty | selected parent's DAA score (`blockversion.OfSelectedParent`) |
| `ValidateIBDPruningListVersion` | HTN-006 | restores `IsValidPruningPoint` + `ArePruningPointsInValidChain` | pruning point header's DAA score |

None keys on the header's own `Version()` field. That field is peer-supplied, and every rule here
adds strictness, so keying on it would let any miner opt out by claiming an older version.

### Tests

- `hardforks` package: six tests asserting inertness at every reachable version on every network,
  at absurd versions, that the placeholder is unreachable, and that a *scheduled* gate must be
  defined by `POWScores` (fires at activation time, not before).
- `TestHeaderBitsRuleIsInertUntilItsGateIsScheduled` — a block with wrong bits still lands.
- `TestHeaderBitsRuleRejectsWrongBitsOnceScheduled` — the same block is refused with
  `ErrUnexpectedDifficulty` once scheduled, and a block this node built still passes.
- `TestTwoConsensusesBuiltAtDifferentVersionsAgreeOnEverythingGated` — two consensuses fed identical
  blocks, built at global version 1 and 9, agree on pruning point, pruning point *list*, finality
  point, tip status and template bits.
- `TestValidateAndInsertImportedPruningPoint` — two long-dead commented-out assertions restored under
  the gate, including the plan's named case: a UTXO set **with one sompi removed** is accepted with
  the gate off and rejected with `ErrBadPruningPointUTXOSet` with it on.
- `TestEveryGateIsUnscheduledInAShippedBuild` — belt and braces from outside the package.

### CI

`build_and_test.sh` gained a second check: production code may not assign to a gate or call
`hardforks.SetForTest`. The gates are `var` rather than `const` only so tests can exercise a rule
that is otherwise unreachable.

### Corrected assumption

The plan's HTN-006 test ("pruning point list validated after activation") was drafted as
"accepted before, rejected after". That is **wrong**, and the first draft of the test failed because
of it. Importing at the wrong pruning point already fails with the gate off — with a bare database
`not found` raised much later, once something looks for data the syncee does not have. The gate does
not turn an accepted import into a rejected one; it turns a late, untyped, incidental failure into an
early typed `ErrUnexpectedPruningPoint`. That is still worth having, but it needed saying accurately.
The assertion is deliberately **not** made inside `TestValidateAndInsertImportedPruningPoint`,
because the gate-off path leaves the consensus half-imported and the rest of that long test then runs
against it. It needs its own test; that is not yet written and is listed below.

### Other disabled consensus checks, now labelled

Six further checks were commented out with no ticket and no activation plan. None was enabled; each
now carries an explicit label:

| Check | File | Label |
|---|---|---|
| `checkParentsIncest` | `pruning_violation_proof_of_work_and_difficulty.go` | disabled, not gated, no ticket |
| `checkMergeSizeLimit` | `block_header_in_context.go` | disabled, not gated, no ticket |
| `checkIndirectParents` | `block_header_in_context.go` | disabled, not gated (cost concern, unmeasured) |
| `checkDAAScore` | `block_header_in_context.go` | disabled, not gated — relates to HTN-006 |
| `checkBlueWork` | `block_header_in_context.go` | disabled, not gated — HTN-006 |
| `checkHeaderBlueScore` | `block_header_in_context.go` | disabled, not gated — HTN-006, 62.5% mismatch |
| `validateHeaderPruningPoint` | `block_header_in_context.go` | disabled, not gated — HTN-001 cites this line |

The "enable these on block v6" note on three of them is **stale**: version 6 activated long ago and
they are still off, so reaching v6 resolved nothing.

### Not done in Workstream C

- A dedicated test for `ValidateIBDPruningListVersion`'s activated behaviour (see above).
- The "offset-inherited commitments are rejected only when the gate says so" test for
  `StrictUTXOCommitmentVersion`. The gate is wired and inert-tested, but its *activated* path is not
  yet exercised end to end: reaching it needs a consensus actually running on an offset baseline,
  which is the scenario HTN-002 reproduced in a scratchpad test that was never committed.

## Workstream E: canonical UTXO artefact tool

`utxoforensics -canonical [-canonical-out FILE]`, read-only, against a **copy** of a cleanly shut
down datadir.

It enumerates the pruning-point UTXO bucket and reports three things: entry count, MuHash, and the
SHA-256 of a canonical encoding. It also says whether the set matches the pruning point's own header
commitment — and explicitly **does not** decide which of the two is authoritative, or which pruning
point to checkpoint. Those are the Step A2 maintainer decisions.

The logic lives in `cmd/utxoforensics/canonical`, separate from the 3350-line `main.go`, so it is
unit-testable without a datadir.

**What makes the output canonical**

- Ordered by outpoint — transaction ID bytes (via `DomainTransactionID.Less`, not hex strings, per
  HTN-230), then index. Input order cannot affect output.
- Each entry serialized with `utxo.SerializeUTXO` — the exact call consensus feeds to
  `multiset.Add`. So the reported MuHash *is* the value a header's `UTXOCommitment` is compared
  against, not a second opinion computed a different way. That equivalence is asserted by a test.
- Length-prefixed entries, so no two different sets can collide by concatenation.
- A magic + format version header and a trailing entry count, so a future format cannot silently
  compare equal and a truncated file cannot hash as a shorter valid one.

**Why two hashes.** MuHash is what the chain commits to, but it is order-independent, so it cannot
distinguish a set from a reordered copy. The encoding SHA-256 is reproducible by anyone with
`sha256sum` against the published file, without running this tool.

**Streaming.** A mainnet set is tens of millions of entries, so the builder is O(1) in memory and
requires input already in canonical order — which the pebble bucket iterator provides, since the key
*is* the serialized outpoint. It verifies that rather than assuming it, and fails loudly on a
regression instead of publishing an unreproducible hash. `BuildFromUnordered` sorts first, for
callers holding a slice.

**Tests (11, all passing)**

- Identical output across 8 repeated runs, and across 16 shuffles of the same input.
- The MuHash equals an independently built consensus multiset.
- One sompi removed from one entry changes both hashes, with entry count unchanged.
- A dropped entry changes both hashes.
- A duplicate outpoint is rejected, not silently deduplicated — a double-count is a finding.
- Out-of-order input to the streaming builder is rejected.
- The streaming and sorting entry points produce byte-identical encodings.
- The encoding is self-describing, and truncation changes the hash.
- The empty set is well-defined rather than a crash.
- `BuildFromUnordered` does not reorder the caller's slice.

## Workstream D: HTN-204 — design only

`docs/design/HTN-204.md`. **No consensus code was written**, per the plan.

The refutation was re-verified against this branch rather than trusted. The attempt patch is not in
the repository, so it was reconstructed from the ticket and run A/B:

| Tree | Runs | Outcome |
|---|---|---|
| With the attempt patch | 3 | **3/3 FAIL** (~34s, IBD timeout) |
| Unpatched control | 2 | **2/2 pass** (~6.3s) |

The central finding: **the bug and the thing preventing a worse bug are the same line.**
`calculateBlockWindowHeap` serves both difficulty (wants the true trusted window) and trusted-data
serving (must only enumerate what it can serve). The empty window was accidentally what kept the
serving path consistent.

The document's main conclusion is that the plan's two options are **not alternatives** to separating
those two uses — both require it, because both make the difficulty window non-empty and the serving
path shares it.

**Awaiting your choice before any implementation.**

## Final state of this branch

| Workstream | State |
|---|---|
| A — non-consensus fixes | Done. HTN-166 partly (limits settable, defaults unchanged; endpoint split still open) |
| B — repair tooling and deploy artefacts | Done |
| C — gated hard-fork code | Implemented and green; two of four required tests not written (below) |
| D — HTN-204 | Design document only, as instructed. Awaiting a decision |
| E — canonical UTXO artefact tool | Done |
| F — hygiene and CI | Done |

### Still outstanding

1. **C: a dedicated test for `ValidateIBDPruningListVersion`'s activated path.** The gate is wired
   and inert-tested; the activated assertion cannot live in
   `TestValidateAndInsertImportedPruningPoint` because the gate-off path leaves the consensus
   half-imported and the rest of that long test runs against it.
2. **C: `StrictUTXOCommitmentVersion`'s activated path end to end.** Reaching it needs a consensus
   actually running on an offset baseline — HTN-002 reproduced exactly that in a scratchpad test
   that was never committed.
3. **HTN-166: the public/authenticated RPC endpoint split.** Architectural, not a limit.
4. **D: implementation**, pending the option choice.

## Hard-rule compliance ledger

| Rule | How this branch complies |
|---|---|
| 1. No new validation for existing versions | Every C predicate keys on a version above every network's `POWScores` length |
| 2. HTN-216 gate untouched | `mergeSetRewardIgnoresDAAWindowVersion = 10` and mainnet DAA `227679830` not modified |
| 3. No HTN-204 re-apply | Workstream D is a design document only |
| 4. No real activation score | Placeholder version constant only; no `POWScores` entry added |
| 5. Lockstep tables | No new `POWScores` entry, so no table extension is due; documented for the release captain |
| 6. No repair flags in deploy artefacts | Compose examples ship without them; one-shot migration is a separate service |
| 7. Never run the committed ELF / production datadirs | The ELF is deleted, never executed; all testing on `TestConsensus` |
| 8. Code beats ISSUES.md | Eight discrepancies recorded above |
| 9. No marketing claims touched | README edits are a URL fix and a TODO |

## Needs a human

Carried forward verbatim from the prompt's out-of-scope list, plus what this pass added:

- Production inventory (Step 0.1) — step contents unrecoverable, PDF missing.
- The seven predicate questions (Step A1) — **contents unrecoverable, PDF missing.**
- Checkpoint pruning-point selection and commitment authority (Step A2) — **unrecoverable.**
- Choosing the activation DAA score, and extending all eight per-version tables in lockstep with it.
- Release signing keys — blocks HTN-162 from being *completed*; see the judgment call below.
- Deploying binaries, datadir copies, publishing UTXOSetHealth pages, DNS seed changes.
- Exchange/pool coordination, rollback posture, GitHub issues, naming the release captain.
- HTN-166's actual limit values — needs production traffic data (see below).
- HTN-177: whether to recover pebble-tool's real source or rewrite it deliberately.

## Judgment calls recorded here before they are made

- **HTN-162 without signing infrastructure.** A pinned public key cannot be invented. The defensible
  implementation is: verify a checksum from the release metadata *and* require a signature, with
  auto-install **disabled by default** behind an explicit flag until a key is configured. That
  satisfies "no unsigned archive installs" without this branch fabricating a key.
- **HTN-166 limits.** Lowering a P2P message ceiling can reject legitimate large IBD messages and
  partition this node from the network — the same class of risk hard rule 1 exists to prevent, and
  nobody has measured the largest legitimate message on this chain. So the defaults are **not**
  changed. What shipped instead is the mechanism: both ceilings are settable at runtime
  (`--p2p-max-message-size`, `--rpc-max-message-size`, `0` = built-in default), the effective values
  are logged at startup, a value below 16 MiB warns, and a negative one fails startup. That turns
  "a human must pick a number" into "a human who has measured their traffic can pick one without a
  rebuild", without this branch guessing on their behalf. Four tests pin that the zero default is a
  true no-op. **Still open:** separating public from authenticated RPC endpoints, which is an
  architectural change rather than a limit.
