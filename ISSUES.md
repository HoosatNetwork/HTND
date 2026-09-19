# ISSUES

Source: consensus split-brain audit, 2026-09-13, static reading of master @ ddfd01da6.
No code was changed and no tests were run for any issue below. "repro: static" means the defect
was found by reading the code; the fix_plan names the test that should reproduce it.

Severity mapping from the audit: P0 = critical, P1 = high, P2 = medium, P3 = low.

---

## HTN-001
- title: Pruning point, candidate and finality depth depend on when the consensus object was constructed
- status: fixed
- severity: critical
- area: pruning
- evidence:
  - domain/consensus/factory.go:311 and domain/consensus/factory.go:394-395 — `config.FinalityDepth()` / `config.PruningDepth()` are evaluated once at construction; both read the process-global `constants.GetBlockVersion()` (domain/dagconfig/params.go:249, domain/dagconfig/params.go:267), which is 1 in a freshly started process.
  - app/protocol/flows/v8/blockrelay/ibd_with_headers_proof.go:19-20 — the global is raised to the tip version *before* `InitStagingConsensusWithoutGenesis`, so a staging consensus (promoted by `CommitStagingConsensus`) freezes version-9 values instead.
  - Mainnet result: restarted node finalityInterval=86400, pruningDepth=185798; node that finished headers-proof IBD without restarting finalityInterval=54000, pruningDepth=136882.
  - domain/consensus/processes/pruningmanager/pruningmanager.go:411 (depth test) and :421 / :1095 (`finalityScore` move rule) — two nodes with identical DAGs pick different pruning points.
  - domain/consensus/processes/finalitymanager/finality_manager.go:101, :118, :146 — virtual finality point uses the frozen depth (24h vs 3h window), so `isViolatingFinality` disagrees on reorgs between 54000 and 86400 blue score deep.
  - domain/consensus/processes/pruningmanager/pruningmanager.go:238-251 — the code logs this defect but does not fix it.
  - Nothing cross-checks the result: header pruning-point validation is commented out (domain/consensus/processes/blockvalidator/block_header_in_context.go:106); import-time `IsValidPruningPoint` / `ArePruningPointsInValidChain` are commented out (domain/consensus/processes/blockprocessor/validate_and_insert_imported_pruning_point.go:13-29).
  - Does not reconverge: on restart the IBD node switches to 86400 but keeps its 54000-aligned pruning points in the index list.
- measured 2026-09-15 (MainnetParams under ForceSetBlockVersion): versions 1-4 finalityDepth=86400 pruningDepth=185798 (K=18); versions 5-9 finalityDepth=54000 pruningDepth=136882 (K=40). Mainnet POWScores=[17500000 21821800 29335426 43334184 192792190 213340776 217137983 218735007], so the chain is at version 9. Every node builds its consensus at startup with the global at 1, so restarted nodes (the usual case) run the version-1 values; only nodes that finished headers-proof IBD without restarting run 54000/136882. Intent per 1be7e25b0 ("Add finality duration to change with every block version") and df4c31bda was per-version values.
- decision (user, 2026-09-15): "the pruning points should depend on the current block version" - finality and pruning depth follow the chain's CURRENT block version (not each evaluated block's own version, not a construction-time value). Implemented as blockversion.Current: the higher of the DAA-derived versions of the virtual selected parent and the headers selected tip, falling back to the process-global only while neither has a DAA score. Options that were offered: A) per-block DAG-anchored version (intended values; changes pruning points on restarted nodes, needs coordinated release), B) pin version-1 values everywhere (matches the de facto network, removes the split for IBD nodes), C) activation-gated switch to per-version values at a future DAA score, D) report-only logging first
- repro: static
- implementation: dagconfig FinalityDepthForBlockVersion/PruningDepthForBlockVersion; new domain/consensus/utils/blockversion.Current; finalitymanager and pruningmanager read depths through it on every use (including the <5 one-interval guard in UpdatePruningPointByVirtual); factory passes POWScores and the versioned depth functions. Reproduction test passes after the change.
- suite (domain+app, -tags=ci): 68 packages ok, 1 failure - TestValidateAndInsertImportedPruningPoint/hoosat-testnet "Unexpected pruning point" (passes on HEAD). Cause is the new semantics, not a code bug: the test shrinks only the version-1 parameters (FinalityDuration 5 blocks, K[0]=0) while testnet POWScores [1 50 100 150 200 ...] take its chain to version 5 at DAA score 200, where K=40 and a 200ms target time give a pruning depth of ~28900; previously every test consensus froze version-1 depths. Fix: pin that test's chain to version 1 with POWScores=[MaxUint64], as pruningmanager/pruning_test.go already does. Added TestPruningFollowsTheChainsCurrentBlockVersion (chain at version 5 on a node built at global 1 must select with version-5 depths).
- current-version test notes: TestPruningFollowsTheChainsCurrentBlockVersion uses POWScores [1,1,1,1] (header validation treats DAA score 0 as version 1, so an activation at 0 fails ErrWrongBlockVersion) and full-length per-version slices (checkBlockTimestampInIsolation indexes TargetTimePerBlock by version). Both HTN-001 tests pass; TestValidateAndInsertImportedPruningPoint passes with its chain pinned to version 1.
- fix_plan: Compute finality/pruning depth per call from a DAG-anchored version (`constants.BlockVersionForDAAScore` of the virtual selected parent's or pruning point's stored DAA score), as mergedepthmanager already does; decide explicitly which version governs a pruning point that straddles a fork. Add report-only logging of header `PruningPoint()` vs `ExpectedHeaderPruningPoint` before any enforcement. Test: two TestConsensus instances built after `ForceSetBlockVersion(1)` and `ForceSetBlockVersion(9)`, fed identical blocks past two finality intervals; assert equal `PruningPoint()`, `PruningPointByIndex` lists and `VirtualFinalityPoint()`.
- tests: domain+app -tags=ci suite green (after pinning TestValidateAndInsertImportedPruningPoint to version 1); domain/consensus/depth_construction_time_test.go reproduces before the fix: identical 200-block chain, consensus built at global version 1 picks pruning point blue score 150, one built via NewConsensus at global version 9 picks 120, and virtual finality points differ (NewTestConsensus forces the global to 1, so the second node must come from NewConsensus)
- commit: ee22f57e9

## HTN-002
- title: UTXO validity depends on the node's local pruning-point baseline ("inherited offset" toleration)
- status: needs_human
- severity: critical
- area: utxo
- evidence:
  - domain/consensus/processes/consensusstatemanager/verify_and_build_utxo.go:41 and :79-95 — when `blockInheritsKnownUTXOCommitmentOffset` is true, any RuleError from commitment, accepted-ID merkle root, coinbase, or body-transaction checks (including script failures) is downgraded to a log line.
  - domain/consensus/processes/consensusstatemanager/inherited_offset.go:35 — the only guard compares merge-set acceptance data with the block's diff; it says nothing about the block's own coinbase, signatures or commitment.
  - domain/consensus/processes/consensusstatemanager/verify_and_build_utxo.go:455-500 — tolerance is enabled whenever the pruning point's stored multiset != its header commitment; nodes still on genesis, synced from genesis, or on a clean import are strict. Strict and tolerant nodes coexist.
  - domain/consensus/processes/consensusstatemanager/import_pruning_utxo_set.go:290-310 — a peer set that does not match its header is accepted unless the refuse flag is set; the flag is the hidden `EnableSanityCheckPruningUTXOSet` (domain/consensus/factory.go:358), default false.
  - Split: an honest miner on a tolerant node commits its offset multiset and offset fees; strict nodes disqualify that block and all descendants (ErrBadUTXOCommitment / ErrBadCoinbaseTransaction). Tolerant nodes with different offsets accept each other's blocks but accept different transactions (missing inputs), so UTXO sets keep drifting (documented cascade at domain/consensus/processes/consensusstatemanager/calculate_past_utxo.go:347).
- decision (user, 2026-09-15): measure first, no code change. GetInfo already reports UTXOSetHealth (app/rpc/rpchandlers/get_info.go:31); collect it from mainnet nodes to size the strict/tolerant split before any rule change. Confirmed while triaging: tolerated steps are utxo-commitment, accepted-id-merkle-root, coinbase-transaction and block-transactions-vs-past-utxo (ValidateTransactionInContextAndPopulateFee: input/output amounts, coinbase maturity), gated by blockOnlyCarriesTheInheritedOffset; refusing mismatched imported sets stays behind EnableSanityCheckPruningUTXOSet (default off).
- repro: static
- fix_plan: Needs a maintainer decision because every current mainnet node may be tolerant. Direction: make validity independent of local baseline health; refuse to import or serve a set that does not match its header; replace per-node toleration with a coordinated rebaseline (checkpointed pruning point + UTXO commitment shipped in a release). Interim: expose `UTXOSetHealth` and tolerated-failure counts over RPC to measure the split. Test: consensus A imports a pruning-point set with one entry removed, consensus B clean; mine a block on A, insert into both; today A=UTXOValid, B=DisqualifiedFromChain.
- decision (user, 2026-09-15, second): use the fix_plan if it is the best option.
- measured 2026-09-15 (utxoforensics (built at 7c45a617c) on a copy (/mnt/data/.htnd5-forensics-copy) of the htnd5 mainnet datadir, taken after a clean SIGINT shutdown on 2026-09-15 07:33): -pphistory - the served pruning point bucket (21,679,714 entries) hashes to the current pruning point [2809]'s STORED multiset 1437ecba..., which does not match that point's header commitment dc0a1e17... - this node runs on an offset baseline, i.e. the tolerant mode. Recommendation: the fix_plan's removal of toleration is not safe to apply now (it would disqualify this node's chain); its rebaseline needs a maintainer-chosen checkpoint (pruning point + UTXO commitment), which cannot be invented here.
- REPRODUCED 2026-09-15 (fix_plan test, scratchpad/htn002/offset_baseline_split_test.go, not committed - it documents today's split): a syncee imports the syncer's pruning point with one UTXO entry changed by one sompi. ValidateAndInsertImportedPruningPoint ACCEPTS it (commitment check disabled); the syncee reports an offset baseline (checked=true, verified=false) and accepts the syncer's later blocks as Valid. A block mined on the tolerant syncee is Valid on the syncee and DisqualifiedFromChain on the strict genesis-synced syncer, with no insert error on either - deterministic on mainnet and testnet params. So an honest miner on a tolerant node produces blocks strict nodes reject, and any peer can put a node into the tolerant mode by serving it a changed pruning point set.
- decision (user, 2026-09-15, third): the import commitment check cannot be turned back on - no node serves a correct (commitment-matching) pruning point set. Stays needs_human.
- decision (user, 2026-09-19): keep the tolerance as-is; do not act on this yet. Asked in a survey of all
  open needs_human issues (see AGENT_STATE.md 2026-09-19) - explicitly deferred, not closed. Stays
  needs_human; no code change.
- tests: not run
- commit: uncommitted

## HTN-003
- title: Consensus rules read the process-global block version, so rule selection depends on message order, IBD and uptime
- status: fixed
- severity: high
- area: consensus
- evidence:
  - Rules reading `constants.GetBlockVersion()`:
    - domain/consensus/processes/ghostdagmanager/ghostdag.go:64-76 (dynamic K from v6) and :327 (maxAnticoneSize k+1 from v7) — GHOSTDAG data is persisted and never recomputed for real blocks.
    - domain/consensus/processes/consensusstatemanager/verify_and_build_utxo.go:517 (accepted-ID merkle root sorting below v5).
    - domain/consensus/processes/blockvalidator/block_body_in_isolation.go:231 (max block mass 500k vs 1M).
    - domain/consensus/processes/difficultymanager/difficultymanager.go:85, :107, :123, :145, :162 (DAA window and target time; stored DAA score and DAA-added blocks).
    - domain/consensus/processes/consensusstatemanager/resolve.go:70 (tip ordering).
    - domain/consensus/processes/pruningmanager/pruningmanager.go:223 (one-interval pruning guard).
  - Where the global is raised:
    - app/protocol/flows/v8/blockrelay/handle_relay_invs.go:309-310 raises it from an unvalidated header DAA score before validation (`checkDAAScore` is disabled at domain/consensus/processes/blockvalidator/block_header_in_context.go:88).
    - app/protocol/flows/v8/blockrelay/ibd.go:119 raises it to the tip version before replaying blocks from the pruning point.
    - app/rpc/rpchandlers/submit_block.go:33 / :66 checks the version but never raises the global.
  - Divergence cases:
    - (a) Around a fork at DAA score D, a late anticone block with DAA < D is colored under post-fork rules on nodes that already saw a block >= D, and under pre-fork rules elsewhere.
    - (b) IBD from a pruning point before D replays pre-fork blocks under post-fork rules.
    - (c) After a restart, a SubmitBlock of a v9 block with mass in (500k, 1M] is rejected as too heavy.
  - `checkBlockVersion` (domain/consensus/processes/blockvalidator/block_header_in_isolation.go:71) validates the header version, but none of the rules above use the header version.
- decision (user, 2026-09-15): each block's own version - derive the version from the block's own DAA score (the selected parent's for virtual), as mergedepthmanager and coinbasemanager already do, for GHOSTDAG K / anticone size, block mass, DAA window and target time, tip ordering and accepted-ID merkle sorting. (HTN-001's finality/pruning depth deliberately follows the chain's current version instead.)
- progress (rule by rule, one commit each):
  - GHOSTDAG dynamic K / anticone bound: committed e21594e04 (domain+app suite 69 ok). Version from the selected parent's computed DAA score (block's own DAA score does not exist yet when coloring). Repro TestGHOSTDAGColoringFollowsTheBlocksOwnVersion: global 9 recolored a stored v1 merge block 2 blues/0 reds/blue score 4 vs 1/1/3. Pruning proof GHOSTDAG managers keep the global (HTN-009).
  - block mass + accepted-ID merkle sorting: committed 12f307e87 (suite green; merkle test mutation-checked against the global read). TestBlockMassLimitFollowsTheBlocksOwnVersion failed before the fix (700k version-5 block rejected with the global at 1; the old error also printed the whole limits table). TestAcceptedIDMerkleRootOrderFollowsTheBlocksOwnVersion targets the new calculateAcceptedIDMerkleRoot(data, blockVersion) signature, so before the fix it only failed to compile.
  - difficulty window/target time: committed e16ca9659 (suite 69 ok). TestRequiredDifficultyFollowsTheBlocksOwnVersion failed before the fix (same v1 block: 173b69c3 at global 1 vs 1e7fffff at 9 on mainnet; 1801855e vs 1f0346dc on testnet).
  - difficulty window: committed (see above). Virtual parents limit + tip ordering: committed e99d965c7 (suite 69 ok). TestVirtualParentsFollowTheNextBlocksVersion failed before the fix: global 9 on a version-1 chain built a 12-parent template (limit 10) that the node rejected with ErrTooManyParents.
  - new site found 2026-09-15: building a block while the process-global version is ahead of the chain produces a coinbase the node itself rejects (ErrBadCoinbasePayloadLen): coinbasemanager.blockVersion falls back to constants.GetBlockVersion() for a block with no stored header (the block being built), so the payload prefix follows the global, while validation uses the header version. The global can be raised from an unvalidated relayed header DAA score (handle_relay_invs.go:309), so a peer can make a node build invalid templates. Committed 04737e5cc (suite 69 ok): coinbasemanager.blockVersion derives the version of a header-less block from its staged DAA score); TestBuiltCoinbaseFollowsTheBuiltBlocksVersion failed before the fix with ErrBadCoinbasePayloadLen on AddBlock and on the node's own template.
  - non-consensus reads triaged 2026-09-15:
    - blocktemplatebuilder/txselection.go:113 caps a template's total mass at policy.BlockMaxMass[global]; validation (12f307e87) caps by the header version, so a node whose global is ahead of the chain (raised by a relayed header) fills a version-1 template past 500k and rejects it - same class as the virtual parents bug. Fix: derive the next block's version from the virtual DAA score and the activation table (template builder gets POWScores). Committed 4bd7553d3 (suite 70 ok): nextBlockMaxMass; TestTemplateMassFollowsTheNextBlocksVersion mutation-checked (global read fills 900k into a v1 template).
    - blocktemplatebuilder.go:146 and txselection.go:162 sort by subnetwork only below version 5, while checkBlockTransactionOrder always requires the order; harmless today because the mempool only accepts native-subnetwork transactions (others panic), note only.
    - calcTxValue weighting, mempool Config.MaximumMassPerBlock (never read), submit_block validatePoW (conversion already rejects an empty PoW hash) and validateDAAScore window (RPC staleness policy), factory cache sizing, hashrate log: note only.
  - done (committed, see progress above): GHOSTDAG K/anticone (e21594e04); block mass and accepted-ID merkle sorting (12f307e87); difficulty window/target time (e16ca9659); virtual parents + tip ordering (e99d965c7); built coinbase version (04737e5cc); template mass cap (4bd7553d3).
  - leftover scan (done 2026-09-15, repo-wide grep for GetBlockVersion() and the dagconfig current-version helpers, non-test code; no indirect references): no consensus rule still reads the process-global version.
    - fallbacks only (global used only without an activation table, or before the needed GHOSTDAG/DAA/header data exists - genesis, trusted-data bootstrap): ghostdag.go blockVersion, difficultymanager.go blockVersion, consensus_state_manager.go versionOfChildOf, resolve.go findNextPendingTip (only when virtual has no GHOSTDAG data), coinbasemanager.go blockVersion, mergedepthmanager.go, blockversion/current.go, blocktemplatebuilder.go nextBlockMaxMass.
    - non-consensus, direct reads (note only): factory.go:164/170 PruningDepth/FinalityDepth and :794 DAA window - cache sizes only; validate_and_insert_block.go:205 hashrate debug log; consensus.go expectedDAAWindowDurationInMilliseconds - IsNearlySynced threshold (tx relay/mining gate; v1 window ~44 min until the global is raised after a restart); mempool/config.go blocksPerSecond - mempool expiry conversion (same restart window); mempool/config.go:95 MaximumMassPerBlock - set, never read; blocktemplatebuilder.go:149 and txselection.go:160 subnetwork sort below v5 - harmless (native-only mempool); blocktemplatebuilder.go:234 calcTxValue mass weighting - selection weighting only.
    - app layer, policy (note only): ibd_with_headers_proof.go:159 headers-proof IBD decision uses PruningDepth() of the global just raised from the relay block's unvalidated DAA score; submit_block.go:98 validatePoW gate and :155 DAA window staleness check; estimate_network_hashes_per_second.go:47 window cap; handle_pruning_point_and_its_anticone_requests.go:74/:121 slice capacity hints.
    - tools/tests: cmd/utxoforensics (reproduces the construction-time depths on purpose), testutils/for_all_nets.go.
  - not this issue: HTN-010 trusted-data window/K (fixed 0765d2186); HTN-009 proof validation coloring (still open).
- repro: static
- fix_plan: Pass each block's own version (`BlockVersionForDAAScore(powScores, header.DAAScore())`, or the selected parent's for virtual) into GHOSTDAG, merkle-root, mass, DAA-window and ordering code; remove ambient reads from domain/consensus/processes (mergedepthmanager and coinbasemanager already follow this pattern); add a staticcheck/grep gate that forbids `GetBlockVersion()` there. Test: simnet-style POWScores with a fork at D; insert X (DAA < D) before vs after a block with DAA >= D on two instances; compare X's GHOSTDAG data and descendants' selected parents.
- tests: per-rule repro tests named in the progress block (each failed before its fix or was mutation-checked); domain+app -tags=ci suite green before each sub-commit
- closed (user, 2026-09-15): marked fixed after the leftover scan found no consensus rule reading the process-global version.
- restart window note (2026-09-15, not fixed): the nearly-synced threshold and mempool expiry conversion use version-1 values only until the global is raised - by the first relayed block (handle_relay_invs.go:310), the start of IBD (ibd.go:96) or the first template build (block_builder.go:117/254). On a connected node that is seconds; the global cannot be pushed beyond the chain's version on mainnet/testnet, which are already at their highest version. Not worth a change.
- commit: e21594e04, 12f307e87, e16ca9659, e99d965c7, 04737e5cc, 4bd7553d3 (sub-commits)

## HTN-004
- title: UTXO diff conflict tolerance makes restorePastUTXO depend on each node's diff-child path
- status: open
- severity: high
- area: utxo
- evidence:
  - domain/consensus/utils/utxo/diff_algebra.go:82 — `isTolerableConflict` accepts conflicting entries whenever both are coinbases or amount+script match, ignoring BlockDAAScore (which is part of the multiset preimage and of maturity/lock checks).
  - domain/consensus/utils/utxo/diff_algebra.go:268-293 — in `diffFrom`, an outpoint in both toAdd sets but only one toRemove set is dropped from the result entirely; this is algebraically wrong when the removal was a restamp (remove X@d1, add X@d2).
  - domain/consensus/utils/utxo/mutable_utxo_diff.go:275-283 — `addEntry` lets the incoming DAA score win, so the result depends on the seed diff.
  - The diff-child tree itself depends on reorg history and arrival order (domain/consensus/processes/consensusstatemanager/resolve_block_status.go:429-463, ReverseUTXODiffs) and on IBD.
  - Two nodes with the same DAG can therefore reconstruct a coin as present vs absent, or with a different DAA stamp, which changes acceptance and multisets.
  - The code's own diagnostics say restorePastUTXO "has independently drifted" (domain/consensus/processes/pruningmanager/pruningmanager.go:1629-1637).
  - Confidence: plausible, not demonstrated.
- triage 2026-09-15: parked. The tolerances (isTolerableConflict, the inBothToAdd single-removal skip, addEntry's incoming-score-wins restamp) are deliberate maintainer decisions, documented in place with mainnet evidence (coinbase ID collisions, a node whose IBD stalled 61 times, 23 wrong-stamp coins); restamp_algebra_test.go, duplicate_add_test.go and intersection_remainder_test.go pin them. Any change alters how every node reconstructs past UTXO sets and multisets, so a fix is a consensus change; a demonstration test would need a reorg DAG touching a collided or restamped outpoint from both branches. needs_human.
- repro: static
- fix_plan: Build a DAG where a coinbase-ID collision or a restamped outpoint is touched by both branches of a reorg; compare the `RestorePastUTXOSetIterator` multiset for the same block on a node that saw the reorg and on one fed only the final chain. If they differ, replace the tolerance with exact diff algebra keyed on (outpoint, entry) and an explicit restamp representation.
- decision (user, 2026-09-15): attempt a repro (test only, no code change) - reorg DAG touching a colliding or restamped outpoint from both branches; compare reconstructions.
- decision (user, 2026-09-15, second): test coinbase-ID collisions.
- tests: repro attempts 2026-09-15, not reproduced (test only): (1) TestRelayedBlockVerdictsAgree seeds 100-139 (40 seeds) - no verdict disagreement; (2) scratchpad/htn004/reorg_reconstruction_order_test.go - two branches merge the same transaction at different heights (restamp) and each spends its output with a different transaction, grown in alternating stages; observers receive the same blocks flip-flopping the tip 5 times, P-branch-first and R-branch-first; 20 attempts, 51 past-UTXO reconstructions compared per attempt: identical multisets, all matching header commitments, same final tip, no conflicting verdicts (only expected UTXOPendingVerification on the node that never had the losing branch selected). Coinbase-ID collisions not exercised.
- decision (user, 2026-09-15): commit the reorg test as a regression guard and keep the issue open (coinbase-ID collisions untested).
- commit: f883de582 (test only: domain/consensus/reorg_reconstruction_order_test.go; no fix)

## HTN-005
- title: Served pruning-point UTXO set is a node-local construction and is never enforced against its commitment
- status: needs_human
- severity: high
- area: pruning
- evidence:
  - domain/consensus/processes/pruningmanager/pruningmanager.go:2079-2091 — the diff method (acceptance-data vs diff-chain walk) depends on which local data still exists.
  - domain/consensus/processes/pruningmanager/pruningmanager.go:2005-2061 — `pickConsistentPruningPointDiff` chooses by comparing against the node's own per-block multiset, and applies the primary diff anyway when neither matches (:2057-2061).
  - domain/consensus/processes/pruningmanager/pruningmanager.go:2136-2144 — `validateUTXOSetFitsCommitment` only warns unless the hidden sanity flag is set.
  - domain/consensus/consensus.go:864-888 — `GetPruningPointUTXOs` serves the bucket after checking only the pruning point hash.
  - Different peers can hand an IBD node different sets under the same pruning point hash; the importer then tolerates the mismatch (HTN-002).
- triage 2026-09-15: the fix_plan's first step (refuse to serve a bucket that does not hash to its commitment) would stop every node serving today: updatePruningPoint's own comment says no node currently holds a set matching its header, which is why the commitment check only warns. Depends on the HTN-002 measurement; needs_human.
- repro: static
- fix_plan: Refuse to serve a bucket that does not hash to the header commitment (return an error so the IBD peer tries another node); make the diff method deterministic (one method, fail if its data is missing, recompute from restorePastUTXO otherwise). Test: two nodes that reach the same pruning point via different histories (one deleted acceptance data, one did not) must produce identical bucket multisets.
- decision (user, 2026-09-15): not yet - the commitment is failing and the cause must be known first.
- measured 2026-09-15 (utxoforensics (built at 7c45a617c) on a copy (/mnt/data/.htnd5-forensics-copy) of the htnd5 mainnet datadir, taken after a clean SIGINT shutdown on 2026-09-15 07:33): -virtualcheck - virtual's materialised UTXO table (21,631,594 entries, hash 0ebe89cd...) DISAGREES with virtual's stored multiset (12e1ad59...): a local drift between the table (RPC balances, served sets) and the multiset (block templates). -commitmentscan 400000 - 83,074 readable chain blocks: 6,533 stored multisets match their header, 76,541 differ; the tip 02e7be07... agrees; oldest divergence in window at DAA 226452612. -pphistory - bucket equals the stored multiset of pruning point [2809] (not stale), stored != header. Cause not yet known; next: -stampcheck on the copy.
- measured 2026-09-15 (same copy): -stampcheck (400,000 chain blocks back; 83,074 with acceptance data) - 415,037 unspent created coins checked, 415,025 stamps agree with this node's own acceptance data, 12 CONTRADICT it: all outputs of two transactions (9d089687...:0-5 accepted by ed02e919... at DAA 226476488, virtual holds 226476490; cbefb035...:0-3 accepted by f17ced69... at DAA 226476458, virtual holds 226476459) - stamps 1-2 too high, the same signature the intersectionWithRemainderHavingDAAScoreInPlace comment reports from earlier mainnet measurement. Each such coin is a SerializeUTXO preimage no correct node reproduces, so the commitment cannot match while they exist. Candidate cause for the virtual table vs multiset disagreement; not yet traced to code.
- trace 2026-09-15 (copy + node log): both wrong-stamp coins are COINBASE outputs of the accepting chain block's selected parent (9d089687 = coinbase of 216864f0, accepted by chain child ed02e919 at DAA 226476488, virtual table holds 226476490; cbefb035 = coinbase of aefe3353, accepted by f17ced69 at 226476458, table holds 226476459). A selected parent's coinbase is accepted only by its chain child, so the higher stamp is virtual's: while the parent was virtual's selected parent, virtual also merged sibling tips, its DAA score ran ahead, and it accepted that coinbase at its own score; the later restamp to the chain child's score never reached the materialised table. Not crash-split: commitVirtualUTXODiff writes inside the same DB transaction as multiset/acceptance data, and f17ced69 was accepted at 20:26:15 on 2026-09-14, 13 s before the next process start, on 9d54ad3b1 - which already contains the restamp fixes 6df1cdaa4/d41041a50/e8d81ddb5. (The node's restarts at 20:26 and 20:39 were hard stops: no shutdown lines; only the 07:33 SIGINT shut down cleanly.) Block ed02e919's own header commitment matches neither stamping rule on this node. Next: consensus test of a selected parent + sibling tip + chain child, asserting the table stamp and table-vs-multiset (earlier HTN-004 tests compared diff-chain reconstructions, never the materialised table).
- repro attempt 2026-09-15: TestVirtualTableRestampsTheSelectedParentsCoinbase (committed 59a6764e0, test only) - selected parent P plus sibling tip X, then chain child B of P: virtual accepts P's coinbase at virtual's DAA (7), and once B (DAA 6) is the selected tip the table is restamped to 6 and hashes to virtual's multiset on mainnet and testnet params. Does NOT reproduce the mainnet stamps (which had more merged siblings and relay-order resolution). Mutation check: applying the table diff's additions before its removals makes it fail (coin gone), so it guards the table restamp path. Running: TestRelayedBlockVerdictsAgree extended locally (work copy, not committed) with a per-node virtual table vs multiset check, seeds 200-219.
- trace 2026-09-15 (cont.): the two wrong-stamp cases differ. 216864f0 (selected parent whose coinbase 9d089687 is +2) and its chain child ed02e919 both match NEITHER stamping rule against their headers - an offset stretch where the inherited-offset tolerance may be involved. aefe3353 (selected parent whose coinbase cbefb035 is +1) and its chain child f17ced69 BOTH match their headers under the merging-block rule - per-block multisets were correct and neither block was tolerated, yet virtual's table kept the coinbase at virtual's old DAA score. So the table drift happened without the tolerance path. f17ced69 was inserted 13 s before a hard stop of the node (20:26:15 vs next start 20:26:28). Relay simulation with a per-node table vs multiset check (seeds 200-219, ~70 blocks each): 0 disagreements.
- hypothesis 2026-09-15 (unconfirmed, being tested): virtual resolution is not one atomic commit. ResolveVirtual commits (1) the resolve staging area (statuses; chain blocks' UTXO diff child temporarily their selected parent), then (2) ReverseUTXODiffs rewrites each chain block's diff in its OWN commit (commitUTXODiffInSeparateStagingArea, one per block), then (3) the virtual update (table, multiset, selected tip diff). A hard kill after (1) or during (2) - this node was hard-killed repeatedly (e.g. 2026-09-14 20:26 and 20:39) - may leave blocks Valid with a half-reversed diff chain; if the retry finds nothing to resolve and gets no reversal data, the reversal is never finished and later virtual updates compose diffs from different bases, the one condition under which the HTN-004 tolerances engage. Longer relay simulation (5 seeds x ~320 blocks, table check) is clean, consistent with a crash-only cause. Same design exists upstream in kaspad.
- confirmed in code 2026-09-15: resolve_block_status.go ResolveBlockStatus returns (stored status, nil reversal data) when a chain has no unverified blocks left, and ResolveVirtual only calls ReverseUTXODiffs when reversal data is non-nil - so a node killed after the resolve commit and before the reversal/virtual commits never finishes that reversal on retry. Crash-injection test running (work copy).
- crash repro 2026-09-15 (work copy, not committed): kill simulated right after the ResolveVirtual status commit on a plain 6+5 block chain, then a normal ResolveVirtual retry - virtual table still hashes to virtual's multiset and all 11 chain blocks' restored pasts match their headers (mainnet and testnet params). That kill point does NOT reproduce on a plain chain; testing the after-reversal kill point and a sibling tip at the resolution boundary next.
- crash repro 2026-09-15 (cont., work copy, scratchpad/htn005/interrupted_virtual_resolution_test.go): both kill points - right after the ResolveVirtual status commit, and after a full ReverseUTXODiffs but before the virtual update - with a sibling tip merged by virtual at the resolution boundary (so the resolution carries a selected-parent coinbase restamp): table == multiset and every chain block's restored past matches its header on mainnet and testnet params. Clean kill points between ResolveVirtual's commits do NOT reproduce. Still untested: a kill in the middle of ReverseUTXODiffs (needs a hook), pruning point advancement, headers-proof IBD, and blocks accepted under the inherited-offset tolerance on an offset baseline (the HTN-002 fix_plan test: import a pruning point set with one entry changed).
- repro attempt 2026-09-15: the same offset-baseline syncee (tolerant mode, HTN-002 test) keeps its virtual UTXO table hashing to its multiset after syncing and five more blocks - the tolerance path alone does not produce the table drift in this shape.
- repro attempt 2026-09-15 (scratchpad/htn005/pruning_advance_table_test.go, not committed): 150 rounds with sibling merges every third round and finality depth 5 - 37 pruning point advancements - virtual table matched virtual's multiset after every block on mainnet and testnet params. Pruning advancement alone does not reproduce. Not reproduced so far: simple restamp, relay simulation (25 seeds incl. 5 x ~320 blocks), kill after status commit, kill after reversal, tolerant offset baseline, pruning advancement. Remaining: a kill in the middle of ReverseUTXODiffs (needs a hook in consensus code), headers-proof IBD against a real peer set, and the history of this particular datadir (Exodus imports, earlier binaries).
- decision (user, 2026-09-15): add a test-only hook to simulate a kill in the middle of ReverseUTXODiffs.
- crash repro 2026-09-15 (mid-reversal, with the user-approved test-only hook: unexported reverseUTXODiffsInterruptHook set via export_test.go): TestReverseUTXODiffsInterruptedMidway kills ReverseUTXODiffs after 1..5 committed diffs (6 completes) on the sibling-tip shape, then retries ResolveVirtual: every kill point leaves virtual on the resolved tip, table == multiset, and every chain block's restored past matches its header, on mainnet and testnet params. Why: a half-reversed diff chain is still a consistent diff-child forest (unreversed blocks keep valid diffs to their selected parents), so the interruption costs restore speed, not correctness. All crash windows in virtual resolution are ruled out as the cause in this shape. Being committed as a guard.
- tests: domain/consensus/processes/consensusstatemanager TestReverseUTXODiffsInterruptedMidway passes (every kill point recovers); package green with and without -tags=ci; full go test ./... green before commit
- commit: 71358964b (test-only interrupt hook + test; not the cause - HTN-005 stays needs_human)

## HTN-006
- title: IBD adopts the peer's pruning point list and header-claimed blue score/work without validation
- status: open
- severity: high
- area: ibd
- evidence:
  - app/protocol/flows/v8/blockrelay/ibd_with_headers_proof.go:479-486 and domain/consensus/processes/blockprocessor/validate_and_insert_imported_pruning_point.go:13-29 — `IsValidPruningPoint` and `ArePruningPointsInValidChain` are commented out, so the IBD node imports whatever list `ImportPruningPoints` receives (domain/consensus/processes/consensusstatemanager/import_pruning_utxo_set.go:384-402), including the serving peer's HTN-001 lineage.
  - domain/consensus/processes/pruningmanager/pruningmanager.go:676 — even when used, `IsValidPruningPoint` accepts depth `pruningDepth-1`.
  - domain/consensus/processes/pruningproofmanager/pruningproofmanager.go:1155-1164 — level-0 GHOSTDAG data of proof blocks is overwritten with `header.BlueScore()` / `header.BlueWork()`.
  - domain/consensus/processes/blockvalidator/block_header_in_context.go:94 and :100 — the header blue-work and blue-score checks are disabled ("after finding reason for the issues with the blocks"), which suggests mainnet headers disagree with computed values.
  - An IBD node's blue scores near the pruning point can therefore differ from a full-sync node's, and finality/pruning math uses them.
- repro: static
- fix_plan: Measure first: offline scan (utxoforensics on a datadir copy) counting blocks where header BlueScore/BlueWork != stored GHOSTDAG data. Then re-enable pruning-point validity checks in report-only mode during IBD, and stop overriding with header values when they disagree with the proof's own GHOSTDAG. Test: headers-proof IBD node vs genesis-synced node on the same DAG; compare GHOSTDAG blue score of the pruning point and the next two pruning points chosen.
- decision (user, 2026-09-15): run utxoforensics on /mnt/data/htnd5 after shutting down the node; no code changes before that.
- measured 2026-09-15 (utxoforensics (built at 7c45a617c) on a copy (/mnt/data/.htnd5-forensics-copy) of the htnd5 mainnet datadir, taken after a clean SIGINT shutdown on 2026-09-15 07:33): -depthaudit 400000 - the pruning depth bracket implied by mined headers is EMPTY ((205387, 136884]), so headers in range were not produced with a single pruning depth (both 185798 and 136882 ruled out) - mainnet evidence of the HTN-001 disparity; 111,479 of 247,553 scanned headers commit to a pruning point this node does not hold (peers on different pruning point lineages). Recent pruning point blue scores are not near multiples of either 86400 or 54000. Header BlueScore/BlueWork vs stored GHOSTDAG data is NOT measured: utxoforensics has no such check (needs a tool-only addition).
- measured 2026-09-15 (same copy, new utxoforensics -headerghostdagscan 400000): 247,553 retained selected-chain blocks, 0 unreadable: 82,558 header and stored GHOSTDAG agree; 154,799 differ in blue score AND blue work; 10,196 differ in blue work only; header minus stored blue score in [-8, 3]; mismatches span the whole retained chain (oldest chain index 1, DAA 226073568; newest 15 blocks below the tip, DAA 226677770). This node's GHOSTDAG results differ from what most recent blocks' miners committed - consistent with nodes coloring under different K/version rules (HTN-001/003); which side is right is not established. The disabled header BlueScore/BlueWork checks are load-bearing today: re-enabling them would reject most recent blocks on this node. Do not re-enable without a coordinated rule decision.
- tests: not run
- commit: uncommitted

## HTN-007
- title: Header difficulty (bits) is never validated against the required difficulty
- status: open
- severity: high
- area: consensus
- evidence:
  - domain/consensus/processes/blockvalidator/pruning_violation_proof_of_work_and_difficulty.go:80 only calls `StageDAAData`; the bits are never compared with `RequiredDifficulty`.
  - domain/consensus/processes/blockvalidator/pruning_violation_proof_of_work_and_difficulty.go:132-158 checks only target > 0, target <= powMax, and PoW against the header's own bits.
  - `ruleerrors.ErrUnexpectedDifficulty` is declared (domain/consensus/ruleerrors/rule_error.go:45) but never returned.
  - Not a split, since every node accepts the same thing, but any bits up to powMax are valid; this weakens the blue-work comparisons that IBD and pruning proofs rely on.
- repro: static
- fix_plan: Needs a coordinated activation (a version-gated check) because historical blocks may not satisfy it. Measure first with a report-only comparison of header bits vs `RequiredDifficulty` on a datadir copy. Test: block with bits = powMax on a synced TestConsensus must be rejected after activation.
- decision (user, 2026-09-15): same as HTN-006 - measure with utxoforensics on /mnt/data/htnd5 first; needs testing before re-enabling.
- measured 2026-09-15: nothing yet - utxoforensics has no header bits vs RequiredDifficulty check; that needs a difficulty computation over each block's window (tool-only addition, larger than the HTN-006 check).
- tool 2026-09-15: utxoforensics -difficultyscan N (committed ae82b01cb, read-only) builds a difficulty manager over a copied datadir the way the factory wires level 0 and compares each selected-chain header's bits with RequiredDifficulty (current per-version window rules; mismatches broken down by the header's DAA-derived version). First run on the htnd5 copy: the newest 300 chain blocks (version 9) all EQUAL their required difficulty (~57 ms/block). 20,000-block scan running.
- measured 2026-09-15 (htnd5 copy): -difficultyscan 20000 - all 20,000 newest selected-chain blocks (version 9, DAA ~226.66M-226.68M) have header bits EQUAL to RequiredDifficulty, 0 easier, 0 harder, 0 unreadable. Recent mainnet blocks would pass a bits check; older eras (earlier versions, other window rules) are not measured yet.
- tests: not run
- commit: uncommitted

## HTN-008
- title: Non-MissingTxOut errors while populating inputs are silently dropped, leaving the block UTXO-valid
- status: fixed
- severity: medium
- area: utxo
- evidence:
  - domain/consensus/processes/consensusstatemanager/verify_and_build_utxo.go:266-269 — for an error that is not ErrMissingTxOut (e.g. a database error), the goroutine unlocks and returns without setting `firstErr`, so `validateBlockTransactionsAgainstPastUTXO` returns nil.
  - The transaction's inputs are never validated, and the block's status is persisted as valid.
  - A transient local fault therefore produces a durable status difference from peers.
- confirmed 2026-09-15: verify_and_build_utxo.go validateBlockTransactionsAgainstPastUTXO - after populateTransactionWithUTXOEntriesFromVirtualOrDiff fails, `if !isMissingTxOut { mu.Unlock(); return }` returns without recording firstErr; the only non-MissingTxOut sources are consensusStateStore.HasUTXOByOutpoint / UTXOByOutpoint errors (database reads). Not a consensus-rule change: the error is not a RuleError, so recording it aborts block processing instead of storing a status.
- repro: static
- fix_plan: Record any non-nil error as `firstErr` (and close `done`); only ErrMissingTxOut may reach the tolerance path. Test: inject a failing stagingArea/DB read for one transaction and assert the block is not StatusUTXOValid.
- tests: consensusstatemanager/dropped_input_error_test.go (stub state store returning a database error: validation returned nil before the fix); consensusstatemanager package -tags=ci, vet, gofmt, staticcheck pass
- commit: 34b2a9c3d

## HTN-009
- title: Pruning proof validation and application color historical headers with the ambient (tip) version rules
- status: fixed
- severity: medium
- area: ibd
- evidence:
  - domain/consensus/processes/pruningproofmanager/pruningproofmanager.go:484 and :1141 — GHOSTDAG runs on proof headers from every era with the global version (set to the tip by ibd_with_headers_proof.go:19), so pre-v6 headers get dynamic K (domain/consensus/processes/ghostdagmanager/ghostdag.go:64).
  - domain/consensus/processes/pruningproofmanager/pruningproofmanager.go:648-693 — blue work is then compared against the local DAG's GHOSTDAG data, which was computed under different rules.
  - Effect: acceptance depends on local state and on the rule mix; the selected tip can fail ErrPruningProofSelectedTipIsNotThePruningPoint and stall IBD, depending on the peer.
- decision (user, 2026-09-15): per-header version now - color each pruning-proof header by its own DAA-derived version (the proof GHOSTDAG managers get the activation table; with no DAA store they use the selected parent header's DAA score), shipped in the same coordinated release as HTN-001/003. Accepted risk: until peers upgrade, IBD between upgraded and non-upgraded nodes may fail proof validation.
- finding 2026-09-15: buildPruningPointProof only reads stored per-level GHOSTDAG data, so building is unaffected. ApplyPruningPointProof colors with the factory's level managers, which already get the activation table since e21594e04. The remaining gap is ValidatePruningPointProof: its internal managers (dagProcesses) and the target-reachability manager were built with no activation table, so a node validated a proof under the global's K and then applied it under each header's own K.
- repro: TestProofValidationColorsHeadersByTheirOwnVersion (internal, pruningproofmanager): validation's level-0 managers color a version-1 diamond merge 2 blues/0 reds at global 9 vs 1/1 at global 1. Cannot compile pre-fix (new field); mutation-checked on a HEAD copy.
- fix_plan: pruningProofManager gets powScores (factory passes config.POWScores) and passes them to the dagProcesses and target-reachability GHOSTDAG managers; the K-0 tmp manager stays as is. Apply after HTN-010 commits (same files).
- tests: TestProofValidationColorsHeadersByTheirOwnVersion passes (mutation-checked: no activation table gives 2 blues/0 reds at global 9 vs 1/1 at 1); gofmt, vet, staticcheck clean; domain+app -tags=ci suite exit 0, 71 ok
- commit: 4e46244df

## HTN-010
- title: Trusted data served to IBD peers and blocks kept at pruning use ambient window size and K
- status: fixed
- severity: medium
- area: p2p
- evidence:
  - app/protocol/flows/v8/blockrelay/handle_pruning_point_and_its_anticone_requests.go:64 and :111 — DAA window size and K for trusted data come from `GetBlockVersion()`.
  - domain/consensus/processes/pruningmanager/pruningmanager.go:2430-2433 — `TrustedBlockAssociatedGHOSTDAGDataBlockHashes` walks `K[ambient]`+1 blocks; a restarted server at version 1 sends 19 GHOSTDAG entries instead of 41.
  - domain/consensus/processes/pruningmanager/pruningmanager.go:511 — `calculateBlocksToKeep` uses the window size in effect when deletion runs, so a later request can need a block that was already deleted.
  - Effect: IBD nodes receive different trusted GHOSTDAG context per server, so pruning-point anticone coloring can differ or IBD can fail.
- triage 2026-09-15: covered by the HTN-003 per-block decision (no question needed). Window size for DAABlockWindow (consensus.BlockDAAWindowHashes) and calculateBlocksToKeep should be the one of the block's own version (selected parent's computed DAA score, matching the difficulty window). Additional finding: from version 6 a block is colored with DAGKnight dynamic K, and CalculateRank starts at k=0 and increases until a vote passes with no cap at the configured K table - so TrustedBlockAssociatedGHOSTDAGDataBlockHashes walking K[version]+1 ancestors can serve less GHOSTDAG context than the coloring used. Plan: walk max(stored DynamicK, K[version])+1 (sending more context is harmless to the syncee, which indexes into it). Fix and tests drafted in scratchpad/htn010, applied after the HTN-003 rules.
- repro: TestDAABlockWindowFollowsTheBlocksOwnVersion (a version-1 block's window: 10 blocks at global 1, 39 at 9) and TestTrustedGHOSTDAGContextFollowsTheBlocksOwnK (4 vs 20 blocks of context); both failed pre-fix on a HEAD copy.
- fix: blockversion.OfSelectedParent + Index; DAABlockWindow and calculateBlocksToKeep size windows by the block's own version; TrustedBlockAssociatedGHOSTDAGDataBlockHashes walks coloringK = max(stored DynamicK, K[own version]). Flow capacity hints still read the global (allocation only).
- tests: both pass; build, vet, gofmt, staticcheck clean; full suite 70 ok
- commit: 0765d2186

## HTN-011
- title: Deletion is driven by wall clock and local flags, and --deletion-depth is a no-op
- status: fixed
- severity: low
- area: pruning
- evidence:
  - domain/consensus/processes/pruningmanager/pruningmanager.go:1842-1870 — `shouldDeferDeletion` uses `time.Since` together with `--pruning-interval-hours` and `--data-retention-hours`.
  - domain/consensus/processes/pruningmanager/pruningmanager.go:625-626 — `deleteBlock` marks blocks HeaderOnly even on archival nodes.
  - Deletion does not change validation of new blocks. It does change what a node can serve (proofs, windows, UTXO diffs) and which diff method `updatePruningPoint` can use, which feeds HTN-005.
  - domain/consensus/processes/pruningmanager/pruningmanager.go:1809 — `CheckIfShouldDeletePastBlocks` has no callers, so the hidden `--deletion-depth` flag (infrastructure/config/config.go:160) has no effect.
- triage 2026-09-15: confirmed dead flag - --deletion-depth (config.go:160, hidden) is plumbed through component_manager.go:116 and factory.go:403 into pruningManager, but CheckIfShouldDeletePastBlocks has no callers, so the value is never read. Wiring it in changes which blocks nodes keep and serve; removing it drops a (hidden) config flag - either is a maintainer choice. Wall-clock deferral is local policy (HTN-005 dependency). needs_human.
- repro: static
- fix_plan: Keep deletion policy local but make the pruning-point UTXO set derivation independent of retained data (HTN-005). Either wire `CheckIfShouldDeletePastBlocks` into `updatePruningPoint` or remove the flag. Test: two nodes with different retention settings reach the same pruning point; assert identical bucket multiset and proof contents.
- decision (user, 2026-09-15): wire --deletion-depth in (call CheckIfShouldDeletePastBlocks from the pruning point update so a non-zero depth deletes only up to an older pruning point).
- tests: TestDeletionDepthKeepsTheBlocksOfRecentPruningPoints (default node vs --deletion-depth=3 on one 200-block chain; before the fix the depth-3 node deleted chain block 174 like the default node) passes; pruningmanager package, gofmt, vet, staticcheck clean; domain+app -tags=ci suite exit 0, 71 ok. Semantics as authored: under mainnet depths a depth of 1 or 2 never deletes.
- commit: ba6d7f062

## HTN-012
- title: Pruning-point UTXO bucket update is not atomic with the pruning point commit
- status: fixed
- severity: low
- area: pruning
- evidence:
  - domain/consensus/datastructures/pruningstore/pruning_store.go:156 — `UpdatePruningPointUTXOSet` writes directly through the database context, outside the staging commit.
  - domain/consensus/processes/pruningmanager/pruningmanager.go:2115-2185 — the flag is cleared only after deletion and commit.
  - After a crash the rerun at domain/consensus/factory.go:627 recomputes the diff; `pickConsistentPruningPointDiff` then compares against the partially-updated bucket and may pick a different method than an uninterrupted node would (crash-dependent divergence).
  - domain/consensus/consensus.go:864-888 — during that recovery the bucket changes while the pruning point hash stays the same, so a peer paging across the change receives a mix of old and new pages.
- analysis 2026-09-15: confirmed mechanism. verifyPruningPointDiffAgainstCommitment reads only stored multisets and headers, but pickConsistentPruningPointDiff hashes the live bucket (pruningPointBucketMultiset). The in-progress flag is staged with the pruning point move (pruningmanager.go:611) and cleared only after CommitAllChanges. After a crash between UpdatePruningPointUTXOSet and FinishUpdatingPruningPointUTXOSet, the rerun judges both diffs against a partly or fully updated bucket, so neither agrees and it applies the primary diff even where the uninterrupted node applied the alternate. Re-applying the same diff is idempotent (deletes/puts), so only the method choice diverges.
- proposed fix (needs decision - pruning-point UTXO derivation, same area as HTN-002 "measure first"): persist the chosen method under a pruning-store key before writing the bucket, reuse it on recovery instead of re-picking, delete it in FinishUpdatingPruningPointUTXOSet. Test: stop after UpdatePruningPointUTXOSet (test hook), rerun UpdatePruningPointIfRequired, compare bucket multiset with an uninterrupted run.
- repro: static
- fix_plan: Stage the bucket update in the same transaction as the pruning point, or write to a shadow bucket and swap. Test: interrupt `updatePruningPoint` after `UpdatePruningPointUTXOSet`, restart, and compare the bucket multiset with an uninterrupted run.
- decision (user, 2026-09-15): fix it - persist the chosen diff method before writing the bucket and reuse it on recovery.
- tests: TestPruningPointUTXOSetUpdateMethodBelongsToItsPruningPoint (pruningstore) and TestInterruptedPruningPointUTXOSetUpdateResumes (domain/consensus) pass; these guard the recovery path - the method divergence itself cannot be reproduced on a healthy test DAG (both derivations give the same diff); gofmt, vet, staticcheck clean; domain+app -tags=ci suite exit 0, 71 ok
- commit: f56f96423

---

# Mempool / p2p repair loop (session htnd-copy-1d)

IDs 101+ are used here so they never collide with the consensus audit above.

## HTN-101
- title: mempool getRedeemers has no visited set; diamond-shaped tx chains cause an exponential walk (DoS) and duplicate removals
- status: fixed
- severity: high
- area: mining
- evidence: domain/miningmanager/mempool/transactions_pool.go:204 pushed every child of every popped tx without dedupe; reached via removeTransaction(id,true) on expiry, block double-spend, count limit and RBF, under the mempool lock
- repro: go test ./domain/miningmanager/mempool -run TestGetRedeemersDiamond (40 diamonds timed out at 10s before fix)
- fix_plan: visited set in getRedeemers
- tests: go test ./domain/miningmanager/... -count=1 pass
- commit: 06eb14977

## HTN-102
- title: removeTransaction ignored removeRedeemers=false for orphans, so a mined orphan deleted its orphan children instead of promoting them
- status: fixed
- severity: medium
- area: mining
- evidence: domain/miningmanager/mempool/remove_transaction.go:9 always passed true to removeOrphan; handleNewBlockTransactions passes false
- repro: go test ./domain/miningmanager/mempool -run TestMinedOrphanPromotesItsOrphanChild (0 accepted before fix)
- fix_plan: pass the flag through
- tests: go test ./domain/miningmanager/... -count=1 pass
- commit: 47fc4a2d1

## HTN-103
- title: processOrphansAfterAcceptedTransaction never enqueued promoted txs, so grandchild orphans stayed orphaned until expiry
- status: fixed
- severity: medium
- area: mining
- evidence: domain/miningmanager/mempool/orphan_pool.go:143 queue only held the accepted tx
- repro: go test ./domain/miningmanager/mempool -run TestOrphanChainIsPromotedTransitively (2 of 4 accepted before fix)
- fix_plan: append each promoted tx to the queue
- tests: go test ./domain/miningmanager/... ./app/protocol/flows/v8/transactionrelay -count=1 pass
- commit: 959642cad

## HTN-104
- title: transactions held during IBD are bounded only by count (2M per peer), not bytes, so one peer can exhaust a syncing node's memory
- status: fixed
- severity: high
- area: p2p
- evidence: app/protocol/flows/v8/transactionrelay/handle_relayed_transactions.go:63 and :115 — holdTransaction stores fetched transactions with no validation, no per-tx size check and no byte budget; P2P max message is 4GB (infrastructure/network/netadapter/server/grpcserver/p2pserver.go:24). A peer advertises crafted tx IDs and serves large transactions while the node is in IBD; each flow keeps up to 2M of them. Even honest traffic at ~1KB/tx is ~2GB per peer.
- repro: unit test in pending_transactions_test.go holding transactions whose total size exceeds the budget
- fix_plan: add a per-flow byte budget using the serialized-size estimate; evict oldest until the new tx fits; drop any single tx larger than the budget
- tests: go test ./app/protocol/flows/v8/transactionrelay -count=1 pass; go build ./app/... pass (TestHoldTransactionIsBoundedByBytes held 1GiB and TestHoldTransactionDropsOversized failed before fix)
- commit: 975568650

## HTN-105
- title: GetMempoolEntry always reports fee 0
- status: fixed
- severity: medium
- area: rpc
- evidence: app/rpc/rpchandlers/get_mempool_entry.go:13 declares an empty `transaction`; the lookup at :26 assigns `mempoolTransaction`, but :39 returns `transaction.Fee`, which is always 0. Introduced when the handler moved to GetTransactionNoClone (12026a538); GetMempoolEntries and ByAddresses use the looked-up tx and are correct.
- repro: go test ./app/rpc/rpchandlers -run TestHandleGetMempoolEntryReportsFee (got fee 0 before fix)
- fix_plan: return mempoolTransaction.Fee and drop the placeholder
- tests: go test ./app/rpc/... -count=1 pass; go vet ./app/rpc/rpchandlers pass
- commit: 9714fcfce

## HTN-106
- title: RPC mempool handlers read Fee/Mass of mempool-owned transactions after the mempool lock is released
- status: fixed
- severity: low
- area: rpc
- evidence: app/rpc/rpchandlers/get_mempool_entries.go:13, get_mempool_entry.go:26, get_mempool_entries_by_addresses.go:18 use *NoClone getters; unorphanTransaction writes Fee/Mass/UTXOEntry on the same object under the write lock (domain/miningmanager/mempool/orphan_pool.go:202); rpccontext/verbosedata.go:135 may even write Mass via PopulateMass. Converter does not read UTXOEntry, so no nil-deref; effect is a data race with stale/torn Fee/Mass under -race.
- repro: static
- fix_plan: have NoClone getters copy Fee/Mass under the lock, or switch handlers back to cloning getters for orphans only; measure GC cost first (12026a538 introduced NoClone for GC churn)
- tests: covered by TestPopulateMassConcurrentCalls (91e64954d, other session) and TestPopulateFeeConcurrentReads (4a1f26dce) under -race; RPC converters never read UTXOEntry
- commit: 91e64954d (Mass, other session) + 4a1f26dce (Fee)

## HTN-107
- title: GetOrphanRoots reports parents this node already has as missing roots
- status: fixed
- severity: low
- area: p2p
- evidence: app/protocol/flowcontext/orphans.go:182 — `!found || !(PoWHash=="" && version>=PoWIntegrityMinVersion)` is true for any stored block that has a PoW hash, so known parents are queued as orphan-root invs (kaspad checks GetBlockInfo Exists/HeaderOnly). handle_relay_invs.go:236 then skips them via HasBlock, so the cost is full GetBlock reads under orphansMutex and queue churn, not wrong behaviour.
- repro: go test ./app/protocol/flowcontext -run TestGetOrphanRootsSkipsKnownParents
- fix_plan: treat a parent as a root only when it is missing or header-only (GetBlockInfo), keeping the missing-PoW skip
- tests: go test ./app/protocol/flowcontext ./app/protocol/flows/v8/blockrelay -count=1 pass; go vet pass; go build ./... pass (before fix the known parent was returned as a root)
- commit: 3a7b431bc

## HTN-108
- title: RequestAnticone serves up to MergeSetSizeLimit*5000 hashes per request, so any peer can make this node walk and send a huge anticone repeatedly
- status: wontfix
- severity: medium
- area: p2p
- evidence: app/protocol/flows/v8/blockrelay/handle_request_anticone.go:53 — limit is MergeSetSizeLimit(K*10)*5000 while the comment still describes a 2x factor (kaspad uses *2). The request is unauthenticated and loops forever; each one runs GetAnticone under the consensus lock and builds a BlockHeadersMessage of that many headers. Raised deliberately in 74fcae548 / d262c60bf ("increase max traversal") for sync gaps.
- repro: static
- fix_plan: maintainer decision (2026-09-14): keep the large limit, syncing peers may need it. Lowering the limit makes the server return a ban-worthy protocol error to honest syncees that legitimately need more, so it is a p2p behaviour change. Options: per-peer rate limit on RequestAnticone, or keep the cap but paginate headers.
- tests: not run
- commit: uncommitted

## HTN-109
- title: IBD body sync counts blocks it did not request toward the batch, then silently skips requested blocks that never arrived
- status: fixed
- severity: high
- area: ibd
- evidence: app/protocol/flows/v8/blockrelay/ibd.go:1096-1100 — any new hash increments receivedCount, whether or not it is in hashesToRequest; the loop exits once the count matches, and the processing loop at :1117 `continue`s past requested hashes missing from receivedBlocks. After any timeout+retry (:1066) the server answers both requests, so late duplicates of batch k arrive during batch k+1, finish it early, and its missing bodies are never inserted (later parents lack bodies / IBD re-finds missing bodies). A peer can also send arbitrary blocks to skip bodies.
- repro: go test ./app/protocol/flows/v8/blockrelay -run TestReceiveRequestedIBDBlocksIgnoresUnrequestedBlocks (second requested block missing before fix)
- fix_plan: count only hashes that are in the requested batch; ignore (debug log) anything else. Receive-side only, no wire change.
- tests: go test ./app/protocol/flows/v8/blockrelay -count=1 pass; go vet blockrelay and testing/integration pass; go build ./app/... ./testing/... pass
- commit: 03f598db7

## HTN-110
- title: IBD body sync retries a silent peer forever instead of disconnecting it
- status: fixed
- severity: medium
- area: ibd
- evidence: app/protocol/flows/v8/blockrelay/ibd.go:1044-1069 — on every IBDDequeueTimeout (default 5m) the missing hashes are re-requested with no retry bound; the flag's help text (infrastructure/config/config.go:181) says the peer is disconnected and another tried. checkPeriodicRate only runs inside the processing loop, so it never fires while waiting.
- repro: go test ./app/protocol/flows/v8/blockrelay -run TestReceiveRequestedIBDBlocksGivesUpOnSilentPeer (pre-fix: build failure since the bound did not exist; retry loop had no exit on timeout)
- fix_plan: bound retries per batch (small constant), then return a protocol timeout error so the peer is dropped and IBD moves on
- tests: go test ./app/protocol/flows/v8/blockrelay -count=1 pass; go vet pass; go build ./app/... pass
- commit: 6a1615559

## HTN-111
- title: relay-block request server silently drops a request it cannot serve, stalling the requester's relay flow for 10 minutes
- status: fixed
- severity: low
- area: p2p
- evidence: app/protocol/flows/v8/blockrelay/handle_relay_block_requests.go:51-60 — on GetBlock error or not-found the goroutine only sets `done` and logs; nothing is sent. handle_relay_invs.go readMsgBlock then waits common.DefaultTimeout (600s) before returning router.ErrTimeout (non-ban protocol error, disconnect). kaspad returns protocolerrors.Errorf(false, "Relay block %s not found") from the server instead. handle_ibd_block_requests.go:113-150 has the same pattern for IBD batches. For a single-hash relay request `done` is set after the loop already finished, so the error is never seen at all. Also spawns bare goroutines (not `spawn`), so a panic in GetBlock is not handled by the panic wrapper.
- repro: go test ./app/protocol/flows/v8/blockrelay -run TestBlockRequestServersFailOnMissingBlock
- fix_plan: on not-found/error return a non-ban protocol error from the server flow (kaspad behaviour) so the requester is disconnected immediately; use spawn for the workers
- tests: go test ./app/protocol/flows/v8/blockrelay -count=1 pass; -race on the new tests pass; go vet blockrelay and testing/integration pass; go build ./... pass (both servers neither served nor failed within 5s before fix)
- commit: 486460b82

## HTN-112
- title: a high-priority tx that fails revalidation with a rule error aborts revalidation, stays in the pool with cleared inputs, and can crash block template building
- status: fixed
- severity: high
- area: mining
- evidence: domain/miningmanager/mempool/revalidate_high_priority_transactions.go:98-103 — clearInputs nils every UTXOEntry, then any error from fillInputsAndGetMissingParents (RuleErrors such as ErrUnfinalizedTx, sequence locks, ErrImmatureSpend after a reorg) is returned without removing the tx. The error reaches app/protocol/flowcontext/blocks.go:82 and fails OnNewBlock (HandleError turns it into a ban of the relaying peer), and repeats every 30s. The tx keeps nil inputs: mempool.go:253 BlockCandidateTransactions calls UTXOEntry.IsCoinbase() on them (nil interface panic) for txs with >2 outputs, and transactions_pool.go getTransactionsByAddresses returns a hard error.
- repro: go test ./domain/miningmanager/mempool -run TestRevalidateRemovesTransactionFailingWithRuleError
- fix_plan: in revalidateTransaction treat a mempool RuleError as invalid: log, remove the tx (same as the missing-parents path) and return false,nil; keep propagating non-rule errors
- tests: go test ./domain/miningmanager/... -count=1 pass; go vet mempool pass (rule-error subtest failed before fix with ErrUnfinalizedTx returned)
- commit: 8b7e1f9e9

## HTN-113
- title: RPC transaction input with verbose data set makes the protowire converter loop forever (CPU DoS on the RPC server)
- status: fixed
- severity: high
- area: rpc
- evidence: infrastructure/network/netadapter/server/grpcserver/protowire/rpc_submit_transaction.go:159 — `for x.VerboseData != nil {` should be `if`; the condition never changes, so converting any RpcTransactionInput whose verboseData field is present spins forever in the connection's receive path. Inherited from kaspad (c5b0394bb, 2021). Go clients built from this repo never send it only because fromAppMessage drops input verbose data (HTN-114), so any other SDK or a hostile client can pin a core per SubmitTransaction message.
- repro: go test ./infrastructure/network/netadapter/server/grpcserver/protowire -run TestRpcTransactionInputWithVerboseDataConverts
- fix_plan: change `for` to `if`
- tests: go test ./infrastructure/network/netadapter/server/grpcserver/protowire -count=1 pass; go vet pass; go build rpcclient, app, cmd pass (new test timed out after 5s before fix)
- commit: fa59e3af0

## HTN-114
- title: RpcTransactionInput.fromAppMessage drops input verbose data because of a shadowed variable
- status: needs_human
- severity: low
- area: rpc
- evidence: infrastructure/network/netadapter/server/grpcserver/protowire/rpc_submit_transaction.go:178-181 — `verboseData := &RpcTransactionInputVerboseData{}` inside the if shadows the outer var, which stays nil, so RPC responses never carry input verbose data. Currently masks HTN-113 for Go clients.
  - 2026-09-15 recheck: still present (rpc_submit_transaction.go fromAppMessage, `verboseData := ...` inside the if). The fix_plan precondition is not met: the HTN-113 fix fa59e3af0 (2026-09-13) is in no release tag (`git tag --contains fa59e3af0` is empty; latest tag v2.17.0, tree version 2.17.2 untagged). Sending input verbose data before that fix ships would make Go clients on released versions loop forever converting responses. When to change the RPC output is a release decision - blocked until a release containing fa59e3af0 is out and older clients are considered upgraded.
- repro: static
- fix_plan: assign instead of redeclare, only after HTN-113 is fixed and released (older Go clients would hang on responses otherwise); RPC output change, so check consumers
- tests: not run
- commit: uncommitted

## HTN-115
- title: RequestIBDBlocks has no element-count cap, unlike every other P2P list message
- status: needs_human
- severity: low
- area: p2p
- evidence: infrastructure/network/netadapter/server/grpcserver/protowire/p2p_request_ibd_blocks.go:15 has no len check (compare MaxRequestRelayBlocksHashes in p2p_request_relay_blocks.go:19). app/protocol/flows/v8/blockrelay/handle_ibd_block_requests.go serves every hash (8*NumCPU goroutines), so one message can ask for the whole stored DAG; a peer can repeat it. Bandwidth/IO amplification of what IBD legitimately serves; syncee batch is getIBDBatchSize()=495.
- repro: static
- fix_plan: cap at a small multiple of getIBDBatchSize() in the converter (p2p protocol behaviour: needs care that honest syncees never exceed it, including retries)
- tests: not run
- triage (2026-09-15): v8 is the only protocol version and both senders (ibd.go batch loop and the missing-hash retry) stay within getIBDBatchSize()=495; the handler route carries only CmdRequestIBDBlocks, so its type assertion is safe. Batch history in git: 100 -> 99 (c81506220) -> 500 (3c60a2c8a) -> 99*5 (ab98fe313). A cap would be a new P2P acceptance rule that disconnects any peer (other implementations, forks) sending larger batches; that is a protocol decision, not a clear bug. Suggested if approved: cap at 4*getIBDBatchSize() in RequestIBDBlocksMessage.toAppMessage, mirroring MaxRequestRelayBlocksHashes.
- decision (user, 2026-09-19): approved as part of a batch of non-consensus needs_human fixes ("continue fixing").
- FIXED 2026-09-19, commit d25248fc2: added MaxRequestIBDBlocksHashes = 4*495 = 1980 to appmessage
  (hardcoded rather than importing blockrelay's getIBDBatchSize() to avoid a layering cycle), and
  enforced it in both RequestIBDBlocksMessage.toAppMessage and fromAppMessage, exactly mirroring
  MaxRequestRelayBlocksHashes' own pattern in p2p_request_relay_blocks.go.
- tests: TestRequestIBDBlocksHashesCap (protowire package) - table test asserting the boundary
  (exactly the cap succeeds, one over fails with "too many hashes") in both conversion directions.
  gofmt/vet/staticcheck clean, full protowire/appmessage package suites green.
- commit: d25248fc2

## HTN-116
- title: NewPebbleDB silently deletes the whole datadir when pebble reports corruption
- status: wontfix
- severity: high
- area: other
- evidence: infrastructure/db/database/pebble/pebble.go:27-41 — on errors.Is(err, pebble.ErrCorruption) at open, os.RemoveAll(path) then opens an empty DB. One corrupt SST (or a disk/permission fault surfaced as corruption) wipes all consensus data with only a warn log, forcing a full resync and destroying the evidence utxoforensics/c5-runs investigations depend on. Added deliberately in 6d3aa6198 (2025-07-10).
- repro: static
- fix_plan: maintainer decision (2026-09-14): delete the datadir unless pebble offers recovery; pebble v2.1.6 has no repair/recover API (only ErrCorruption marking in open.go), so the current behaviour is kept. Options: refuse to start with a clear message and a flag (e.g. --reset-corrupted-db) to opt into the wipe; or move the corrupted dir aside (rename with timestamp) instead of RemoveAll.
- tests: not run
- commit: uncommitted

## HTN-117
- title: DBTransaction.BatchPut does not record keyModifications, so Get/Has in the same transaction return the pre-transaction value
- status: fixed
- severity: low
- area: other
- evidence: infrastructure/db/database/pebble/transactions.go:90-97 — keys set via BatchPut are never added to keyModifications; Get/Has only consult the batch for tracked keys and otherwise read the committed DB. A key Deleted then BatchPut in one transaction still reads as deleted. No non-test callers today (latent).
- repro: go test ./infrastructure/db/database/pebble -run TestTransactionBatchPutIsVisibleInTransaction
- fix_plan: record each key in keyModifications like Put does (and use nil opts like Put)
- tests: go test ./infrastructure/db/database/pebble -count=1 pass; go vet pass; go build ./... pass (new test failed before fix)
- commit: 85d5322ef

## HTN-118
- title: pebble DB.Close skips every other open cursor, leaving iterators open when the database closes
- status: fixed
- severity: low
- area: other
- evidence: infrastructure/db/database/pebble/pebble.go:63-70 — ranges over db.cursors while cursor.Close() calls deregisterCursor, which removes the element from the same backing array; the range then skips the element that shifted into the current index. Also reads db.cursors without db.mu.
- repro: go test ./infrastructure/db/database/pebble -run TestCloseClosesAllCursors
- fix_plan: snapshot the cursor slice under db.mu, then close each snapshot entry
- tests: go test ./infrastructure/db/database/pebble -count=1 pass; go vet pass; go build ./... pass (before fix Close returned "leaked iterators: current")
- commit: f1e64a9e9

## HTN-119
- title: a bare multisig output in a block panics the node when any RPC listener subscribes to UTXOsChanged without addresses
- status: fixed
- severity: critical
- area: rpc
- evidence: app/rpc/rpccontext/notificationmanager.go:483-500 scriptPubKeyStringToAddressString calls address.String() whenever the class is not NonStandardTy, but txscript.ExtractScriptPubKeyAddress (domain/consensus/utils/txscript/standard.go:813-821) returns (MultiSigTy|MultiSigECDSATy, nil address, nil error), and typeOfScript does classify bare multisig (standard.go:289-292). Nil interface method call panics inside the spawned consensusEventsHandler goroutine, which exits the process. MultiSigPKH classes instead return an extraction error that fails NotifyUTXOsChanged. Reached in broadcast mode (listener subscribed with an empty address list, or after unsubscribing all), added in 230e98b86. Bare multisig is standard in the mempool, so one ordinary transaction triggers it.
- repro: go test ./app/rpc/rpccontext -run TestBroadcastUTXOsChangedHandlesScriptsWithoutAddress
- fix_plan: in scriptPubKeyStringToAddressString return "" (no error) when extraction errors or yields no single address, as the existing comment intends and as NonStandardTy already does
- tests: go test ./app/rpc/rpccontext ./app/rpc/rpchandlers -count=1 pass; go vet pass; go build ./app/... pass (before fix: nil pointer panic for bare multisig, parse error for malformed script)
- commit: 04d5c9774

## HTN-120
- title: unsubscribing a listener's last UTXOsChanged address switches it to receiving every UTXO change on the network
- status: fixed
- severity: medium
- area: rpc
- evidence: app/rpc/rpccontext/notificationmanager.go:371-384 StopPropagatingUTXOsChangedNotifications deletes addresses but keeps propagateUTXOsChangedNotifications=true; convertUTXOChangesToUTXOsChangedNotification treats an empty address map as broadcast mode (added in 230e98b86; before it, an empty map sent nothing). A wallet removing its last address gets a firehose, and the node pays address extraction for every change per block.
- repro: go test ./app/rpc/rpccontext -run TestStopPropagatingLastUTXOsChangedAddress
- fix_plan: when Stop removes the last address from a non-empty map, stop propagating UTXOsChanged for that listener; keep explicit empty-list subscriptions as broadcast
- tests: go test ./app/rpc/rpccontext ./app/rpc/rpchandlers -count=1 pass; go vet pass; go build ./app/... pass (last-address subtest failed before fix)
- commit: 7be59287d

## HTN-121
- title: CommitStagingConsensus returns before swapping the in-memory consensus if deleting the old prefix fails, leaving the node running on a consensus whose data was just marked inactive
- status: fixed
- severity: low
- area: ibd
- evidence: domain/domain.go:151-167 — after dbTx.Commit() flips the prefixes, DeleteInactivePrefix(d.db) runs before atomic.StorePointer; on its error the function returns with d.consensus still the old instance (now the inactive prefix, possibly half-deleted) and d.stagingConsensus still set, so a follow-up DeleteStagingConsensus would delete the old data again while the committed instance is dropped from memory. Restart recovers (New deletes the inactive prefix and opens the active one). Same ordering as kaspad. Also Consensus() reads d.consensus non-atomically while Commit stores atomically (race detector).
- repro: go test ./domain -run TestCommitStagingConsensusSwapsEvenIfCleanupFails
- fix_plan: swap the pointer (and clear stagingConsensus) immediately after dbTx.Commit(), then attempt the delete and return its error; approved by maintainer 2026-09-14
- tests: go test ./domain -run TestCommitStagingConsensusSwapsEvenIfCleanupFails|TestCreateStagingConsensus pass; go vet ./domain pass; go build ./... pass (old consensus served after failed cleanup before fix)
- commit: 7e620e5c0

## HTN-122
- title: reused outbound P2P routers can be reset while goroutines from the previous connection still hold their routes
- status: fixed
- severity: low
- area: p2p
- evidence: infrastructure/network/netadapter/netadapter.go:155-163 caches outbound routers per address and Router.Reset reopens the same *Route objects (router/route.go:71-91, router.go:47-74; added 51d87e7db/68219d48a). The cache is purged by the disconnect handler (netadapter.go:183), but that handler is installed only after newNetConnection and not at all on the ErrorMessage early return (:170), so a connection that fails before it is installed leaves the router cached; the next connection to that address resets it while the old flows may still Enqueue (e.g. a late verack) into the reopened outgoing route, which the new connection then sends. Route.Reset also swaps closedChan under closeLock while Dequeue reads it unlocked (data race). Plausible, not demonstrated.
- repro: static; reassessed 2026-09-14 after HTN-138: the disconnect handler purges the cache entry (pointer-matched), and HTN-138 purges it on the handshake-failure and disconnected-during-init paths, so Reset is reached only if a second outbound connection to the same address comes up while the first is live - connmanager prevents that (requested check runs on the full connection set before outgoing; RandomAddresses excludes connected addresses)
- fix_plan: purge the cache entry on every path where the connection does not reach setOnDisconnectedHandler, or install the handler before starting the router; alternatively stop reusing routers across connections
- tests: covered by ghost_connection_test.go (outbound=true asserts the cache is purged); netadapter/grpcserver/app/protocol tests pass
- commit: 235ad08d1 (with HTN-138). Remaining theoretical Route.Reset closedChan read race only on the unreachable duplicate path - not changed

## HTN-123
- title: RPCClient reconnect never closes the previous gRPC ClientConn
- status: fixed
- severity: low
- area: rpc
- evidence: infrastructure/network/rpcclient/rpcclient.go:106-153 — disconnect() only calls stream.CloseSend(); connect() then creates a new grpc.ClientConn and overwrites c.GRPCClient, so each reconnect leaks a ClientConn (TCP connection, transport goroutines). htnminer reconnects on every GetBlockTemplate timeout (cmd/htnminer/mineloop.go:208). Same in kaspad. Also c.timeout/c.GRPCClient/lastDisconnectedTime are written without synchronization while calls read them.
- repro: infrastructure/network/rpcclient/reconnect_test.go - 3 Reconnects left 4 open server-side TCP connections before the fix
- fix_plan: not a one-liner: the old client's receive-loop error callbacks act on the RPCClient's current router, so closing the old conn must first detach its handlers (grpcclient.handleError panics with no handler) - restructure so each GRPCClient carries its own generation and stale callbacks are ignored, then Close the old conn
- tests: go test ./infrastructure/network/rpcclient/... pass; -race -count=3 pass; go build ./... and CI staticcheck subset pass
- commit: dcd59f8fa (timeout/lastDisconnectedTime/GRPCClient field races left as in kaspad)

## HTN-124
- title: promoting an orphan does not check the mempool for double spends, so two pool transactions can spend the same outpoint
- status: fixed
- severity: medium
- area: mining
- evidence: domain/miningmanager/mempool/orphan_pool.go:196-241 unorphanTransaction validates against consensus and validateTransactionInContext (validate_transaction.go:48, no double-spend check) and then addMempoolTransaction; checkDoubleSpends runs only in validateTransactionPreUTXOEntry for arriving txs, and checkOrphanDoubleSpend only compares orphans. Sequence: orphan O spends missing P:0 and confirmed F:0; T spending F:0 arrives and is accepted (O is not in the pool); P arrives, O is promoted. mempool_utxo_set.addTransaction then overwrites transactionByPreviousOutpoint[F:0]; templates include both (BuildBlockTemplate invalid-tx retry), and removing either deletes the index entry the other still relies on, so later double spends of F:0 are admitted too. Same in kaspad.
- repro: go test ./domain/miningmanager/mempool -run TestUnorphanRejectsDoubleSpendOfPoolTransaction
- fix_plan: in unorphanTransaction run mempoolUTXOSet.checkDoubleSpends before adding (RejectDuplicate rule error, orphan already removed)
- tests: go test ./domain/miningmanager/... ./app/protocol/flows/v8/transactionrelay -count=1 pass; go vet pass (new test failed before fix: orphan promoted, 2 accepted)
- commit: 61962e159

## HTN-125
- title: gRPC peer seeding has no deadline, so an unresponsive seed leaks a goroutine and connection per reseed
- status: fixed
- severity: low
- area: p2p
- evidence: infrastructure/network/dnsseed/seed.go:157 client.GetPeersList(context.Background(), req). connmanager calls seedFromDNS every 30s loop while short of outbound peers (infrastructure/network/connmanager/outgoing_connections.go:57), spawning one call per configured seed; a seed that completes the HTTP/2 handshake but never answers blocks each call forever. No network in domain/dagconfig/params.go sets GRPCSeeds, so only nodes run with a custom gRPC seed flag are affected.
- repro: go test ./infrastructure/network/dnsseed -run TestRequestGRPCPeersTimesOut
- fix_plan: context.WithTimeout (e.g. 30s) around GetPeersList
- tests: go test ./infrastructure/network/dnsseed -count=1 pass; go vet pass; go build ./... pass (pre-fix: no deadline existed, call blocked until the server answered)
- commit: e6343fd1c

## HTN-126
- title: RBF accepts a replacement that spends an output of a transaction it evicts, leaving a pool transaction with a nonexistent input
- status: fixed
- severity: low
- area: mining
- evidence: domain/miningmanager/mempool/validate_and_insert_transaction_replacement.go:43-96 fills inputs (from parents in the pool) before removing the conflicts and their redeemers, then recomputes parents and adds the transaction. If the replacement spends an output of an evicted transaction (C spends X, D spends C:0, R spends X and D:0), R enters the pool with an input whose transaction is gone: it looks ready (no parents in pool), is relayed and put in templates, and can never be valid. RPC-only (local submissions).
- repro: go test ./domain/miningmanager/mempool -run TestReplacementCannotSpendEvictedOutput
- fix_plan: after computing the removal set, reject the replacement if any input spends an output of a transaction in that set
- tests: go test ./domain/miningmanager/... ./app/protocol/flows/v8/transactionrelay ./app/rpc/rpchandlers -count=1 pass; go vet pass (new test failed before fix: replacement accepted)
- commit: d7ea98c5b

## HTN-127
- title: block locator request flow exits silently when the locator comes back empty, leaving the peer waiting out the 10-minute timeout
- status: fixed
- severity: low
- area: p2p
- evidence: app/protocol/flows/v8/blockrelay/handle_request_block_locator.go:42-44 — `if err != nil || len(locator) == 0 { return errors.Wrapf(err, ...) }`; Wrapf(nil, ...) is nil, so an empty locator with no error ends the flow with nil (no error reaches errChan, the connection stays up, later RequestBlockLocator messages are never read). The requester's isBlockInOrphanResolutionRange then waits common.DefaultTimeout (600s). A non-protocol err is turned into a ban by HandleError, or silently swallowed if it is database not-found. kaspad returns a protocol error here.
- repro: go test ./app/protocol/flows/v8/blockrelay -run TestRequestBlockLocatorEmptyLocatorIsProtocolError
- fix_plan: return a non-ban protocol error (wrapping err when present) so the peer is disconnected instead of left waiting
- tests: go test ./app/protocol/flows/v8/blockrelay -count=1 pass; go vet pass; go build ./... pass (before fix the flow returned nil for an empty locator)
- commit: ee753d8dd

## HTN-128
- title: FlowContext.HandleError drops database not-found errors without disconnecting, so the failing flow dies on a live connection
- status: fixed
- severity: medium
- area: p2p
- evidence: app/protocol/flowcontext/errors.go:33-36 returns before sending to errChan when database.IsNotFoundError(err). The flow goroutine has already exited, so the peer stays connected (and counts toward outbound targets) while that flow's route is never read again: requests to it time out on the peer's side, and a dead HandleRelayInvs means no more blocks from that peer. IsNotFoundError follows Unwrap, so protocol errors wrapping a not-found are dropped too. Several flows work around it with local `if database.IsNotFoundError(err) { continue }` (handle_relay_invs.go:250,364,385). HTN-127 was one instance.
- repro: go test ./app/protocol/flowcontext -run TestHandleErrorDisconnectsOnNotFound (error dropped before fix)
- fix_plan: maintainer decision (2026-09-14): disconnect the peer. Implementation: convert not-found into a non-ban protocol error (disconnect) instead of dropping it; the comment says not-found can come from races, so measure how often it fires before changing, since every occurrence would become a disconnect
- tests: go test ./app/protocol/flowcontext ./app/protocol/flows/... -count=1 pass; go vet pass; go build ./... pass
- commit: 494c0280c

## HTN-129
- title: htnwallet merge step can reselect a coin the split transactions already spend, so a large send fails after the splits are broadcast
- status: fixed
- severity: low
- area: wallet
- evidence: cmd/htnwallet/daemon/server/split_transaction.go:96-104 mergeTransaction asks moreUTXOsForMergeTransaction for extra funds when split outputs minus fees fall short of the payment; that function (:276-310) excludes only the new split outputs, not the original transaction's inputs the splits consume, and does not consult usedOutpoints (only filled at broadcast or compounding, broadcast.go:58, create_unsigned_transaction.go:130). With send-all every spendable coin is already an input, so the pick is always a double spend; the merge then fails to broadcast after the splits went out, leaving funds compounded in the change address. Also `amount - feePerInput` there and in createSplitTransaction (:229-230) wraps for inputs smaller than feePerInput, producing an over-spending tx the node rejects. Same shape as kaspawallet.
- repro: static (the path calls s.rpcClient.GetBlockDAGInfo, and existing tests build the server without an RPC client)
- fix_plan: pass the original transaction's input outpoints into moreUTXOsForMergeTransaction's exclusion set and skip unexpired usedOutpoints; guard the fee subtractions. Needs a test seam for the virtual DAA score before changing wallet code.
- tests: merge_utxo_selection_test.go (TestSelectMoreUTXOsForMergeTransaction); mutation check: disabling each of the excluded/usedOutpoints/dust guards fails it; wallet server tests, go build ./..., staticcheck subset pass
- commit: 73d1923ca

## HTN-130
- title: an empty PruningPoints message from an IBD peer indexes headers[-1] and panics the node
- status: wontfix
- severity: low
- area: ibd
- evidence: app/protocol/flows/v8/blockrelay/ibd_with_headers_proof.go validateAndInsertPruningPoints computes consensushashing.HeaderHash(headers[len(headers)-1]) with no length check on the peer-supplied list. The IBD flow runs under spawn, whose panic handler exits the process, so a peer the node syncs from can crash it by answering with an empty pruning points list. Same line in kaspad.
- repro: static - not reachable: domain/consensus/processes/pruningmanager/pruningmanager.go:699-720 ArePruningPointsViolatingFinality returns (true, nil) for an empty list (loop body never runs), so the flow returns "pruning points are violating finality" before indexing headers[len-1]. The guard is indirect and fragile; an explicit length check would be clearer but is not needed.
- fix_plan: reject an empty list with a banning protocol error before any use (honest servers always send at least the genesis/current pruning point)
- tests: not run (path proven unreachable by reading)
- commit: uncommitted

## HTN-131
- title: a pruning point proof with an empty level 0 panics ValidatePruningPointProof and crashes the node
- status: fixed
- severity: critical
- area: ibd
- evidence: domain/consensus/processes/pruningproofmanager/pruningproofmanager.go:366-371 rejects a proof with no levels, then reads level0Headers[len(level0Headers)-1]; a proof whose first level holds no headers indexes -1. The protowire converter (infrastructure/network/netadapter/server/grpcserver/protowire/p2p_pruning_point_proof.go:17-27) and MsgPruningPointProofToDomainPruningPointProof accept empty levels, and consensus.ValidatePruningPointProof (domain/consensus/consensus.go:1557) has no recover. It is called from the IBD flow (ibd_with_headers_proof.go syncAndValidatePruningPointProof) on the peer's proof, under spawn, so the panic exits the process. Any peer can reach it: relay an orphan whose header claims more blue work than the tip, answer the locator with unknown hashes so IBD with headers proof starts, then serve Headers=[[]].
- repro: go test ./domain/consensus -run TestValidatePruningPointProofRejectsEmptyLevel (panic: index out of range [-1] before fix)
- fix_plan: return ErrPruningProofEmpty when level 0 is empty, before indexing. Input validation only: no proof that could ever pass validation changes outcome (consensus_touch, flagged)
- tests: new test pass; go test ./domain/consensus/processes/blockprocessor pass; go vet pruningproofmanager pass; go build ./... pass
- commit: abdbf55d8

## HTN-132
- title: out-of-range trusted-data indices from an IBD peer panic processBlockWithTrustedData and crash the node
- status: fixed
- severity: critical
- area: ibd
- evidence: app/protocol/flows/v8/blockrelay/ibd_with_headers_proof.go processBlockWithTrustedData indexes data.DAAWindow[index] and data.GHOSTDAGData[index] with index taken straight from the peer's MsgBlockWithTrustedDataV4.DAAWindowIndices / GHOSTDAGDataIndices, with no bounds check. The IBD flow runs under spawn, so the panic exits the process. Reached during headers-proof IBD after the proof, pruning points and pruning-point block (all of which a peer can relay honestly) - the indices are the only crafted part. Same code in kaspad.
- repro: go test ./app/protocol/flows/v8/blockrelay -run TestProcessBlockWithTrustedDataRejectsOutOfRangeIndices
- fix_plan: check each index against the trusted data length and return a banning protocol error when out of range
- tests: go test ./app/protocol/flows/v8/blockrelay -count=1 pass; -race on new test pass; go vet pass; go build ./... pass (panic: index out of range [3] with length 0 before fix)
- commit: 56d7b74f0

## HTN-133
- title: DomainTransaction.Fee is written without synchronization while RPC and relay paths read it
- status: fixed
- severity: low
- area: mining
- evidence: Fee is set during in-context validation (ValidateTransactionAndPopulateWithConsensusData on mempool/unorphan paths, domain/miningmanager/mempool/orphan_pool.go unorphanTransaction) while RPC handlers read it through NoClone getters after the mempool lock is released (app/rpc/rpchandlers/get_mempool_entries.go, get_mempool_entry.go). Mass, the cached transaction ID and header block level were made atomic in 91e64954d/71539ea40/68899e962; Fee was not. Maintainer note: it is only set during in-context validation, which does not run on the shared genesis, but should be made atomic like Mass.
- repro: temporary -race test (population + plain Fee reads) reported DATA RACE at transaction_in_context.go:82 before fix; go test -race ./domain/consensus/processes/transactionvalidator -run TestPopulateFeeConcurrentReads after
- fix_plan: store Fee atomically with Load/Store accessors mirroring LoadMass, update writers and readers
- tests: -race fee and mass tests pass; go test transactionvalidator, miningmanager/..., rpchandlers, ruleerrors, externalapi pass; go vet pass; go build ./... pass; staticcheck subset clean
- commit: 4a1f26dce

## HTN-134
- title: offline datadir tools open pebble with NewPebbleDB, which deletes the whole directory on corruption
- status: fixed
- severity: medium
- area: other
- evidence: NewPebbleDB (infrastructure/db/database/pebble/pebble.go:27-41) runs os.RemoveAll on pebble.ErrCorruption; the maintainer keeps that for the node's own datadir (HTN-116). The same call is used by cmd/utxoforensics/main.go:231,258, tools/pruningproof-harness/main.go:93 and cmd/htnexodus/node.go:45. These tools are run on copies of datadirs, often because the node's data is suspect; opening a corrupted copy silently wipes the evidence being investigated, contrary to the CLAUDE.md safety notes.
- repro: go test ./infrastructure/db/database/pebble -run TestOpenPebbleDBKeepsCorruptedDirectory
- fix_plan: add a non-deleting open (return the corruption error) and use it in the offline tools; keep NewPebbleDB's behaviour for the node
- tests: go test ./infrastructure/db/database/pebble -count=1 pass (TestOpenPebbleDBKeepsCorruptedDirectory x3); go vet pebble, utxoforensics, htnexodus, pruningproof-harness pass; go build ./... pass
- commit: 882fe4914

## HTN-135
- title: NewPebbleDB never recognises pebble corruption, so the maintainer-chosen recreate-on-corruption never runs
- status: fixed
- severity: medium
- area: other
- evidence: infrastructure/db/database/pebble/pebble.go NewPebbleDB checks github.com/pkg/errors.Is(err, pebble.ErrCorruption). Pebble marks corruption with cockroachdb/errors.Mark (pebble v2.1.6 open.go:973, base.CorruptionErrorf), which the standard/pkg Is does not follow. A corrupted WAL fails open with "pebble: error when replaying WAL: pebble/record: invalid chunk" and std Is returns false, so the node fails to start instead of recreating the datadir as decided in HTN-116.
- repro: go test ./infrastructure/db/database/pebble -run TestNewPebbleDBRecreatesCorruptedDirectory (open failed with invalid chunk before fix)
- fix_plan: use cockroachdb/errors.Is for the corruption check
- tests: TestNewPebbleDBRecreatesCorruptedDirectory x3 pass (failed with invalid chunk before fix); go build ./... pass
- commit: 882fe4914

## HTN-136
- title: P2PBroadcast stops at the first peer whose outgoing route is full and fails the calling flow
- status: fixed
- severity: medium
- area: p2p
- evidence: infrastructure/network/netadapter/netadapter.go P2PBroadcast skips only ErrRouteClosed; ErrRouteCapacityReached (a non-ban protocol error, NetConnection installs no capacity handler) is returned immediately. Peers after the saturated one never get the message (block/tx invs), and the error propagates through FlowContext.Broadcast into the flow that relayed (e.g. handle_relay_invs relayBlock, transaction propagation), where HandleError disconnects that flow's peer - not the saturated one.
- repro: go test ./infrastructure/network/netadapter -run TestP2PBroadcastSkipsFullRoutes
- fix_plan: treat a full outgoing route like a closed one: log and continue with the remaining peers
- tests: go test ./infrastructure/network/netadapter -count=1 pass (broadcast failed with route capacity has been reached before fix); go vet pass; go build ./... pass
- commit: de342eb4b

## HTN-137
- title: disconnecting an outbound peer blocks until that peer sends something, forever for a silent peer
- status: fixed
- severity: medium
- area: p2p
- evidence: infrastructure/network/netadapter/server/grpcserver/grpc_connection.go receive() holds streamLock.RLock for the whole blocking stream.Recv(); closeSend() takes streamLock.Lock() before closing the ClientConn, so a locally initiated Disconnect of an outbound connection waits for the pending Recv to return. The outbound stream is opened with context.Background() (p2pserver.go Connect), so only the peer sending data or closing unblocks it. Meanwhile onDisconnectedHandler has not run, so the connection stays in NetAdapter.p2pConnections and keeps an outbound slot; a pending writer also blocks new RLocks, so send() stalls too.
- repro: go test ./infrastructure/network/netadapter/server/grpcserver -run TestCloseSendDoesNotWaitForSilentPeer
- fix_plan: close the low-level ClientConn before taking the stream lock (it cancels the stream so Recv returns), then CloseSend under the lock
- tests: go test ./infrastructure/network/netadapter/server/grpcserver -count=1 pass, -race on new test pass (closeSend blocked >5s before fix); go vet pass; go build ./... pass
- commit: 075dcff20

## HTN-138
- title: a P2P connection disconnected while its router initializes (ban check) stays registered forever
- status: fixed
- severity: medium
- area: p2p
- evidence: infrastructure/network/netadapter/netadapter.go onP2PConnectedHandler calls newNetConnection (which runs the protocol router initializer; its spawned goroutine checks IsBanned and calls netConnection.Disconnect, app/protocol/protocol.go), then installs the adapter's disconnected handler, adds the connection to p2pConnections and starts it. If the disconnect lands before the handler is installed, NetConnection.onDisconnectedHandler is nil when the gRPC connection fires it, the entry is added afterwards, and Start on an already-disconnected gRPC connection exits without calling the handler again (Disconnect is a one-shot CAS). The entry never leaves p2pConnections (inflating the inbound count that checkIncomingConnections enforces - its Disconnect of the ghost is a no-op, so real peers get disconnected instead) and an outbound router stays cached (HTN-122's path).
- repro: go test ./infrastructure/network/netadapter -run TestConnectionDisconnectedDuringInitializationIsNotKept
- fix_plan: after installing the handler and registering, if the connection is already disconnected, remove it (and purge the cached outbound router) and return
- tests: go test ./infrastructure/network/netadapter ./infrastructure/network/netadapter/server/grpcserver ./app/protocol/... -count=1 pass (inbound connection still registered before fix); go vet pass; go build ./... pass
- commit: 235ad08d1
## HTN-139
- title: htnwallet send-all over coins worth less than their fees (or a near-uint64 amount) builds a transaction paying ~2^64 sompi
- status: fixed
- severity: low
- area: wallet
- evidence: cmd/htnwallet/daemon/server/create_unsigned_transaction.go:472-481 - for isSendAll, totalReceived = totalValue - fee wraps when the selected coins are worth less than feePerInput each, and the insufficient-funds check compares totalValue with totalSpend = totalValue so it never fires; the wallet then builds and signs a payment of about 2^64 sompi that the node rejects with an unrelated error. For a normal send, spendAmount + fee (:459, :475) wraps for an amount near MaxUint64 (the gRPC amount is not validated), passing the check the same way. selectUTXOsForCompounding already guards totalValue <= fee.
- repro: merge-style unit test on a DAA-score-parameterised selection (no RPC)
- fix_plan: split selectUTXOsForTransaction so the selection takes the virtual DAA score; reject amounts above constants.MaxSompi and send-all selections whose value does not exceed the fees
- tests: send_utxo_selection_test.go (TestSelectUTXOsForTransactionFeeArithmetic); mutation check: disabling either guard fails it with the wrapped payment; wallet server tests, go build ./..., staticcheck subset pass
- commit: 7dca07ac8
## HTN-140
- title: StopNotifyingUTXOsChanged answers an address parse error with a NotifyUTXOsChanged response
- status: fixed
- severity: low
- area: rpc
- evidence: app/rpc/rpchandlers/stop_notifying_utxos_changed.go:20 builds appmessage.NewNotifyUTXOsChangedResponseMessage() on the parse-error path. A client waiting on the StopNotifyingUTXOsChanged response route never gets its reply (times out - rpcclient's default is 10 minutes), and the stray NotifyUTXOsChanged response is later consumed by the next NotifyUTXOsChanged call as if it were its own. Only handler with mixed response types (scanned all rpchandlers).
- repro: unit test calling HandleStopNotifyingUTXOsChanged with an invalid address
- fix_plan: return NewStopNotifyingUTXOsChangedResponseMessage on that path
- tests: stop_notifying_utxos_changed_test.go; mutation check: reverting the parse-error branch fails it with the NotifyUTXOsChanged type; rpchandlers tests, go build ./..., staticcheck subset pass
- commit: 925b3b4d1
## HTN-141
- title: ShutDown RPC closes the interrupt channel, panicking if it was already closed by a signal or an earlier ShutDown
- status: fixed
- severity: low
- area: rpc
- evidence: app/rpc/rpchandlers/shut_down.go:27 spawns close(context.ShutDownChan). ShutDownChan is the channel from signal.InterruptListener (app/app.go:91, component_manager.go:181), which closes it itself on SIGINT (infrastructure/os/signal/signal.go:39). A ShutDown RPC after Ctrl+C, or two ShutDown RPCs within the 1s pause, closes it twice: "close of closed channel" panics in a spawned goroutine, the process exits through the panic handler (and the auto panic reporter) instead of shutting down cleanly.
- repro: unit test calling HandleShutDown twice with a closed channel
- fix_plan: request shutdown through signal.ShutdownRequestChannel (the listener's own request path, which tolerates repeats) or guard the close
- tests: shut_down_test.go (TestHandleShutDownTwice); mutation check: restoring close(ShutDownChan) makes the test binary die ~1s in; rpchandlers tests, go build ./..., staticcheck subset pass
- commit: f2ec6bec1
## HTN-142
- title: transaction IDs queued within 500ms of a propagation are not relayed until some later transaction is enqueued
- status: fixed
- severity: medium
- area: p2p
- evidence: app/protocol/flowcontext/transactions.go:81-85 maybePropagateTransactions holds IDs enqueued within TransactionIDPropagationInterval of the previous broadcast (and fewer than MaxInvPerTxInvMsg); they are only sent by a later EnqueueTransactionIDsForPropagation call. The only periodic caller, broadcastTransactionsAfterBlockAdded (blocks.go:102-104), returns early when a block brings no mempool transactions and no rebroadcast is due, so new blocks never flush the queue. Two transactions arriving close together on a quiet network: the second is not relayed to any peer until another transaction enters the mempool (relayed txs are not high priority, so the 30s rebroadcast does not include them).
- repro: unit test on a FlowContext with a held ID and an elapsed interval; a block with no mempool transactions must flush it
- fix_plan: let broadcastTransactionsAfterBlockAdded call EnqueueTransactionIDsForPropagation even with nothing new, so each block flushes held IDs once the interval has passed (maybePropagateTransactions never sends an empty inv)
- tests: transaction_propagation_test.go (3 tests); mutation checks: restoring the early return leaves the held ID queued, removing the empty-queue guard restarts the interval; flowcontext/transactionrelay/blockrelay tests, go build ./..., staticcheck subset pass
- commit: 7cf0678a6
## HTN-143
- title: a peer advertising an address with a negative timestamp crashes the node
- status: fixed
- severity: high
- area: p2p
- evidence: infrastructure/network/netadapter/server/grpcserver/protowire/common.go:118 converts the peer's int64 timestamp unchecked; app/protocol/flows/v8/addressexchange/receiveaddresses.go:38 (and the handshake's version address, handshake.go:93) pass it to AddressManager.AddAddresses; addAddressNoLock (addressmanager.go:91-114) only checks routability, then addressStore.add -> serializeAddress (store.go:252-255) panics "timestamp is negative". Flows run under spawn, so the panic exits the process: any peer can take the node down with one address message carrying a new routable IP and a pre-1970 timestamp.
- repro: addressmanager unit test adding a routable address with a negative timestamp (panics before the fix)
- fix_plan: ignore addresses whose timestamp precedes the Unix epoch in addAddressNoLock (covers every caller); do not reject at the wire converter (that would change message validity rules)
- tests: negative_timestamp_test.go (TestAddAddressWithNegativeTimestamp); mutation check: disabling the guard reproduces "panic: timestamp is negative"; addressmanager/handshake/addressexchange/connmanager tests, go build ./..., staticcheck subset pass
- commit: c692c8a75
## HTN-144
- title: block template building panics on a transaction with 3+ outputs and more inputs than outputs
- status: fixed
- severity: high
- area: mempool
- evidence: domain/miningmanager/mempool/mempool.go:259-265 BlockCandidateTransactions skips only txs with <=2 outputs; for the rest numExtraOuts = outputs - inputs, the spam filter short-circuits on numExtraOuts > 2, and line 265 then calls checkedUint64FromExtraOutputs(numExtraOuts), which panics on a negative value (ParseUint("-1")). Such transactions pass admission (validate_transaction.go:88 has the same numExtraOuts > 2 guard) and relay normally, so once one is in a mining node's mempool, the next GetBlockTemplate panics in its spawned RPC goroutine and the node exits.
- history: introduced by e1e833f26 (2026-05-02 "Fix golangci-lint run ./..."), which replaced uint64(numExtraOuts) - a silent wrap - with the panicking checked conversion
- repro: mempool test with a ready 3-output/4-input transaction calling BlockCandidateTransactions
- fix_plan: treat a negative extra-output count as zero before the conversion (extra outputs cannot be negative; 0 and positive counts keep their meaning)
- tests: block_candidate_negative_extra_outputs_test.go; mutation check: removing the clamp reproduces panic ParseUint "-1"; domain/miningmanager/... tests, go build ./..., staticcheck subset pass
- commit: 4a5923c92
## HTN-145
- title: relayed compound orphans are high priority, so the orphan pool can neither evict nor expire them and grows without bound
- status: fixed
- severity: high
- area: mempool
- evidence: user-reported live log 2026-09-14: "Number of high-priority transactions in orphanPool (1201..1207) is higher than maximum allowed (100)", climbing. validate_and_insert_transaction.go:25 raisePriorityIfCompound (78fcbddaf, 2026-09-12) raises relayed compound-shaped transactions to high priority before the orphan branch; orphan_pool.go:74-93 limitOrphanPoolSize only evicts non-high-priority orphans (logs and breaks otherwise) and expireOrphanTransactions (orphan_pool.go ~296-322) never expires high-priority orphans. A relayed compound orphan whose parents never arrive therefore stays forever; any peer can grow the pool (memory) without limit.
- repro: orphan_pool_relayed_priority_test.go - 5 relayed compound orphans against a limit of 2 stay at 5 under the old rule
- fix_plan: orphan pool protects only orphans that are high priority AND locally submitted (randomNonHighPriorityOrphan, expireOrphanTransactions). Relayed orphans return to their pre-78fcbddaf orphan lifetime; the transaction-pool keep-alive for relayed compounds is untouched. Rationale: unorphanTransaction already inserts promoted orphans with isHighPriority=false, so orphan protection never carried into the pool; 78fcbddaf itself names bounded expiry as the alternative if unevictable compounds became a problem
- tests: TestOrphanPoolEvictsRelayedCompoundOrphans, TestIsProtectedOrphan; mutation check: restoring the high-priority-only exemption fails both (pool holds 5 > 2); domain/miningmanager/... (incl. TestHighPriorityTransactions), go build ./..., staticcheck subset pass
- commit: c7bb663dd
## HTN-146
- title: promoting an orphan drops its priority, so a relayed compound that arrived before its parents loses the 78fcbddaf keep-alive
- status: needs_human
- severity: low
- area: mempool
- evidence: domain/miningmanager/mempool/orphan_pool.go unorphanTransaction builds the pool entry with model.NewMempoolTransaction(..., false, ...) and does not call raisePriorityIfCompound, so an orphan's high priority (local or compound-raised) is not carried into the transaction pool. 78fcbddaf raises priority "before the orphan branch so a compound transaction arriving ahead of its parents is protected while it waits", but once the parents arrive the promoted compound has the ordinary lifetime and can expire before it is mined. Kaspad also inserts promoted orphans as not high priority.
- repro: static
- fix_plan: policy decision - either pass orphan.IsHighPriority() into NewMempoolTransaction (also keeps local high-priority orphans high priority after promotion, a change from kaspad) or re-run raisePriorityIfCompound on promotion (compound shape only). Not changed without maintainer input.
- decision (user, 2026-09-19): approved option 1 (pass orphan.IsHighPriority() through) as part of a
  batch of non-consensus needs_human fixes ("continue fixing").
- FIXED 2026-09-19, commit 31fb8dd99: unorphanTransaction now passes transaction.IsHighPriority()
  into model.NewMempoolTransaction instead of a hardcoded false, so both a compound orphan's
  raised priority and a locally-submitted high-priority orphan's priority survive promotion.
  isProtectedOrphan's own comment, which described the dropped-on-promotion behavior as current, is
  updated to match.
- tests: TestUnorphanCarriesThePriorityTheOrphanEarned - a relayed compound orphan (2 inputs,
  CompoundTxMinInputsThreshold=2) waits for its parent, is confirmed high priority while orphaned,
  then confirmed still high priority once promoted after the parent arrives. Verified it fails
  without the fix (reverted to false, ran red) and passes with it restored. Full
  domain/miningmanager/... suite green, gofmt/vet/staticcheck clean.
- commit: 31fb8dd99
## HTN-147
- title: a peer sending RequestNextHeaders out of turn crashes the node through an unchecked type assertion
- status: fixed
- severity: high
- area: p2p
- evidence: app/protocol/flows/v8/register.go:103-104 registers HandleRequestHeaders for both CmdRequestHeaders and CmdRequestNextHeaders on one route; handle_request_headers.go:145 receiveRequestHeaders does message.(*appmessage.MsgRequestHeaders) without checking. A MsgRequestNextHeaders arriving while the flow waits for a new request (first message, or after DoneHeaders) panics in the spawned flow goroutine and the node exits. The other unchecked assertions in flows sit on single-command routes.
- repro: blockrelay test feeding MsgRequestNextHeaders into HandleRequestHeaders' route (panics before the fix)
- fix_plan: check the type in receiveRequestHeaders and return a banning protocol error, as the same flow already does for an unexpected message inside the loop (handle_request_headers.go:121-124)
- tests: request_headers_unexpected_message_test.go; mutation check: restoring the unchecked assertion reproduces "interface conversion: ... is *MsgRequestNextHeaders"; blockrelay tests, go build ./..., staticcheck subset pass
- commit: b53ae5e0b
## HTN-148
- title: GetMempoolEntriesByAddresses reports at most one sending and one receiving transaction per address
- status: fixed
- severity: medium
- area: mempool/rpc
- evidence: domain/miningmanager/mempool/transactions_pool.go:273-297 and orphan_pool.go:381-406 build model.ScriptPublicKeyStringToDomainTransaction (map[string]*DomainTransaction) by assignment, so each address keeps only the last transaction seen in random map order. app/rpc/rpchandlers/get_mempool_entries_by_addresses.go:44-104 then emits one entry per pool and direction. An address with several pending spends reports one; htnwallet refreshUTXOs (sync.go) excludes only that transaction's inputs, so the wallet (and any wallet using this RPC) can select coins other pending transactions already spend and get "already spent" rejections. The response message already carries lists, so the wire format needs no change. Same limitation exists in kaspad upstream.
- evidence2: domain/miningmanager/mempool/mempool.go:141,168 (both GetTransactionsByAddresses wrappers) assign the orphan pool's sending map to sendingInTransactionPool and return sendingInTransactionPool in both sending slots. With includeOrphanPool (htnwallet asks for it), every transaction-pool send is dropped and orphan sends are reported twice, once as IsOrphan=false - a wallet does not see its own pending spends at all.
- repro: mempool test with two ready transactions spending coins of the same address
- fix_plan: make the by-address maps map[string][]*DomainTransaction (each transaction listed once per address), update GetTransactionsByAddresses(NoClone) signatures and the RPC handler to emit every transaction
- tests: transactions_by_addresses_test.go; mutation checks: restoring the orphan swap lists 0 pool sends, restoring one-per-address lists 1 of 2; miningmanager/rpc tests, go build ./..., staticcheck pass
- commit: 1d6defaf5
## HTN-149
- title: htnwallet far-address scan permanently skips a batch of indexes when the RPC route closes mid-scan
- status: fixed
- severity: low
- area: wallet
- evidence: cmd/htnwallet/daemon/server/sync.go:180-194 collectAddresses logs and returns nil on router.ErrRouteClosed without scanning; collectFarAddresses (:116-126) still advances nextSyncStartIndex by 100, so that index range is never scanned by the far scan again. collectRecentAddresses (:145-165) likewise advances nextSyncStartIndex past a skipped batch, and syncLoop sets firstSyncDone after a possibly skipped initial scan (the recent scan repeats every 2s, so only addresses beyond maxUsedIndex+1000 stay missed).
- repro: sync_skipped_scan_test.go - real keys and a closed RPC client; far scan advanced to 100, recent scan to 1000 before the fix
- fix_plan: make collectAddresses report whether it scanned; advance nextSyncStartIndex only for scanned batches
- tests: TestAddressScansSkippedOnClosedRouteAreRetried; mutation checks: disabling either guard fails it; wallet server tests, go build ./..., staticcheck pass
- commit: 01dc2a4e1
## HTN-150
- title: RPCClient.GetUsableAddresses waits without a timeout, so a stalled node hangs the wallet daemon while it holds its lock
- status: fixed
- severity: medium
- area: rpc/wallet
- evidence: infrastructure/network/rpcclient/rpc_get_usable_addresses.go uses c.route(...).Dequeue() where every other call uses DequeueWithTimeout(c.timeout). htnwallet's collectAddressesWithLock (cmd/htnwallet/daemon/server/sync.go:167-172) holds s.lock across that call, so if the node never answers (stalled or lost response), the sync loop blocks forever and every wallet RPC taking s.lock (Send, CreateUnsignedTransactions, GetBalance, ...) blocks with it. The call also logs at Info level on every request (twice per batch, every 2s).
- repro: usable_addresses_timeout_test.go - call still blocked at the 5s deadline before the fix
- fix_plan: use DequeueWithTimeout(c.timeout) like the other calls (router.ErrTimeout surfaces to the wallet)
- tests: TestGetUsableAddressesTimesOut (failed at the 5s deadline before the fix, returns router.ErrTimeout after); rpcclient and wallet server tests, go build ./..., staticcheck pass
- commit: bd297a91d
## HTN-151
- title: RPCClient GetMempoolEntriesByAddresses and GetBalancesByAddresses also wait without a timeout
- status: fixed
- severity: medium
- area: rpc/wallet
- evidence: infrastructure/network/rpcclient/rpc_get_mempool_entries_by_address.go:11 and rpc_get_balances_by_addresses.go:11 use Dequeue() where other calls use DequeueWithTimeout(c.timeout) (same class as HTN-150). htnwallet's refreshUTXOs calls GetMempoolEntriesByAddresses from createUnsignedTransactions / createUnsignedCompoundTransaction while Send and CreateUnsignedTransactions hold s.lock, so a node that never answers hangs the wallet daemon.
- repro: rpcclient test against a server that never answers either request
- fix_plan: DequeueWithTimeout(c.timeout) in both
- tests: TestAddressRequestsTimeOut; mutation check: reverting both to Dequeue fails it at the 5s deadline; rpcclient and wallet server tests, go build ./..., staticcheck pass
- commit: 90f9891b7
## HTN-152
- title: a second htnwallet daemon Shutdown RPC closes the shutdown channel again and crashes the daemon
- status: fixed
- severity: low
- area: wallet
- evidence: cmd/htnwallet/daemon/server/shutdown.go:12 close(s.shutdown) with no guard; the gRPC server (server.go grpc.NewServer without a recovery interceptor) does not recover handler panics, so a repeated Shutdown call - e.g. a client retrying while GracefulStop is still draining - panics with "close of closed channel" and the process exits abnormally instead of finishing the graceful stop. Same class as HTN-141.
- repro: unit test calling Shutdown twice
- fix_plan: close only if not already closed; calls are serialized by s.lock
- tests: TestShutdownTwice (panicked "close of closed channel" before the fix); wallet server tests, go build ./..., staticcheck pass
- commit: 683eeac4b
## HTN-153
- title: htnwallet keys file Save rewrites the seed file in place without truncation, temp file, rename or fsync
- status: needs_human
- severity: medium
- area: wallet
- evidence: cmd/htnwallet/keys/keys.go:293-316 Save opens the keys file with os.O_WRONLY|os.O_CREATE (no O_TRUNC) and json-encodes over the existing bytes. A shorter encoding (e.g. an older file pretty-printed, or with fields a newer toJSON omits) leaves the old tail after the new value; ReadKeysFile (keys.go:245-248) uses json.Decoder.Decode, which stops at the first value, so this does not break loading today. But Save is also not atomic and never fsyncs: a crash or power loss during a save (the daemon saves on every NewAddress and every change address) can leave a truncated or mixed file holding the encrypted mnemonics, which then fails to load. Not changed: hard rule restricts key-file edits to proven bugs.
- repro: static
- fix_plan: maintainer decision - write to a temp file in the same directory with 0600, fsync, rename over the original (and fsync the directory); keep the JSON format unchanged
- decision (user, 2026-09-19): approved as part of a batch of non-consensus needs_human fixes
  ("continue fixing" / "continue") - explicit sign-off to touch the key-file save path.
- FIXED 2026-09-19, commit 03f6b7b6f: Save now writes to os.CreateTemp in the same directory
  (guaranteed 0600 permissions, same filesystem as the real path), encodes into it, fsyncs the file,
  closes it, os.Rename's it over the real path, then opens and fsyncs the containing directory. A
  leftover temp file from an early return is cleaned up via a deferred os.Remove (a no-op once the
  rename has succeeded). The on-disk JSON format is unchanged.
- tests: TestSaveRoundTrips (ordinary Save/ReadKeysFile round trip), TestSaveLeavesNoTempFileBehind
  (a successful Save leaves exactly the real file, no stray .tmp-* file), and
  TestSaveOverwritingALongerFileLeavesNoTrailingBytes (seeds the path with a 4KB file, Saves much
  shorter content, requires the result to parse as standalone JSON with json.Unmarshal - a leftover
  tail would still parse with the old json.Decoder.Decode but not with Unmarshal). Verified the third
  test fails on the old in-place-write code (reverted, ran red: saved file was 4110 bytes with the
  old padding trailing) and passes with the fix restored. Full cmd/htnwallet/... suite green,
  gofmt/vet/staticcheck clean.
- commit: 03f6b7b6f
## HTN-154
- title: malformed partially signed transaction bytes crash htnwallet (nil proto fields, input count mismatch)
- status: fixed
- severity: medium
- area: wallet
- evidence: cmd/htnwallet/libhtnwallet/serialization/serialization.go:106-122 partiallySignedTransactionFromProto passes protoPartiallySignedTransaction.Tx unchecked to transactionFromProto, which reads protoTransaction.Version (:186) and protoTransaction.SubnetworkId.Bytes (:208) directly - bytes without a transaction or subnetwork id panic on a nil pointer. Nothing checks len(PartiallySignedInputs) == len(Tx.Inputs); libhtnwallet/sign.go:54 and transaction.go:255 then index Tx.Inputs[i] out of range. The daemon's Sign and Broadcast RPCs (and the htnwallet sign/parse CLI) take these bytes from the client or a cosigner; the daemon's gRPC server does not recover handler panics, so one malformed transaction takes the wallet daemon down.
- repro: serialization test deserializing a PSTX without Tx / with a PartiallySignedInput but no Tx input
- fix_plan: reject nil nested messages and mismatched input counts in partiallySignedTransactionFromProto (and nil fields in the transaction/input/output converters) with errors
- tests: TestDeserializeMalformedPartiallySignedTransaction (malformed cases panicked with a nil pointer before the fix; valid roundtrip still passes); cmd/htnwallet/... tests, go build ./..., staticcheck pass
- commit: 6fc22a32c
## HTN-155
- title: htnwallet parse panics on a transaction with a non-standard or bare multisig output
- status: fixed
- severity: low
- area: wallet
- evidence: cmd/htnwallet/parse.go:71-80 calls scriptPublicKeyAddress.EncodeAddress() before checking scriptPublicKeyType == NonStandardTy; txscript.ExtractScriptPubKeyAddress returns a nil address for non-standard scripts and bare multisig, so `htnwallet parse` dereferences a nil interface and panics instead of printing the placeholder it already has for that case. Also: the printed fee allInputSompi-allOutputSompi wraps when outputs exceed inputs (display only, not changed).
- repro: CLI test parsing a PSTX whose output script is non-standard
- fix_plan: use the placeholder whenever the extracted address is nil, before calling EncodeAddress
- tests: TestParseNonStandardOutput (panicked with the committed parse.go); cmd/htnwallet tests, go build ./..., staticcheck pass
- commit: 24392910b
## HTN-156
- title: htnwallet sweep leaves a UTXO behind at every split boundary
- status: fixed
- severity: medium
- area: wallet
- evidence: cmd/htnwallet/sweep.go:192-204 createSplitTransactionsWithSchnorrPrivteKey adds each UTXO to currentTx; when that pushes the mass over the limit it appends lastValidTx (which does not contain this UTXO), resets totalSplitAmount and currentTx, and continues to the next UTXO - the UTXO that did not fit is never added to any split. Every split boundary therefore leaves one coin unswept at the source address, and if the last UTXO is the one that does not fit, it is dropped as well. Sweep is used to move funds off an imported (possibly exposed) private key, so coins silently remain there. Also: totalSplitAmount - fee (:188) wraps for dust UTXOs (node rejects; not changed).
- repro: sweep_test.go - 200 UTXOs, every outpoint must appear in exactly one split
- fix_plan: start the next split with the UTXO that did not fit instead of skipping it
- tests: TestSweepSplitsIncludeEveryUTXO (failed before the fix with coins missing from every split); cmd/htnwallet tests, go build ./..., staticcheck pass
- commit: 09869e60c
## HTN-157
- title: htnwallet vote panics without --from-address, reports success on invalid input, and busy-loops while waiting for funds
- status: fixed
- severity: low
- area: wallet
- evidence: cmd/htnwallet/vote.go:123 indexes conf.FromAddresses[0], but --from-address is optional (config.go:92, no default), so `htnwallet vote -i <poll> -v 0` panics. :123-133 return errors.Wrap(err, ...) with err == nil, which pkg/errors turns into nil, so an invalid vote (e.g. -v -1) exits successfully doing nothing. :165-172 the "Insufficient funds for send" branch does attempt-- and continues with no delay, hammering the daemon's CreateUnsignedTransactions in a tight loop. When retries are exhausted vote returns nil. Fixed voting address and vote API untouched.
- repro: vote_test.go on the extracted flag validation (the panic needs a keys file and daemon before the fix)
- fix_plan: validateVoteConfig before reading keys/connecting; real errors; sleep retryDelay while waiting for funds; error after exhausting retries
- tests: TestValidateVoteConfig (6 cases); cmd/htnwallet tests, go build ./..., staticcheck pass; retry sleep/exhaustion not unit-tested (needs daemon)
- commit: e1c494718
## HTN-158
- title: htnwallet send (and vote) rebuild and rebroadcast after a broadcast error, which can pay the recipient twice
- status: fixed
- severity: high
- area: wallet
- evidence: cmd/htnwallet/send.go:105-111 on any Broadcast error (including broadcastCtx timeout) continues the retry loop, which calls CreateUnsignedTransactions again and broadcasts brand-new transactions. A broadcast error is ambiguous: the daemon submits transaction by transaction (daemon/server/broadcast.go) and marks each submitted transaction's inputs in usedOutpoints, so a payment that reached the node before the error (or whose response was lost to the timeout) stays in the mempool, the retry selects other coins, and the recipient is paid again. vote.go:221-227 has the same loop (an extra 1 HTN vote payment). Also send.go:61-63 spins without delay while waiting for funds, and both return nil after exhausting retries.
- repro: CLI test against a fake wallet daemon whose Broadcast fails once; count CreateUnsignedTransactions calls
- fix_plan: never rebuild after a broadcast error - return it (retrying create/sign stays); sleep while waiting for funds; return an error after exhausting retries
- tests: TestSendDoesNotRebuildAfterBroadcastError (fake daemon + real keys file; before the fix send requested transactions repeatedly); cmd/htnwallet tests, go build ./..., staticcheck pass
- commit: f931ee103
## HTN-159
- title: --freeze-address replaces the built-in frozen address list instead of adding to it, and is never validated
- status: needs_human
- severity: low
- area: mempool/config
- evidence: app/component_manager.go:143-146 sets mempoolConfig.FrozenAddresses = cfg.FrozenAddresses whenever the flag is given, discarding DefaultConfig's built-in list (domain/miningmanager/mempool/config.go:111-113), although the flag description ("Address to freeze (can be specified multiple times)") reads as adding one. So an operator freezing one more address silently unfreezes the default one. The strings are matched exactly against EncodeAddress() (wallet_freezing_manager.go:123) and never decoded at startup, so a mistyped, differently cased or wrong-network address freezes nothing without any warning. Freezing is not exposed over RPC.
- repro: static
- fix_plan: maintainer decision - whether the flag should append to or replace the default list; decode flag addresses with util.DecodeAddress at startup and fail on invalid ones
- decision (user, 2026-09-19): append (not replace), and validate - approved as part of a batch of
  non-consensus needs_human fixes ("continue fixing").
- FIXED 2026-09-19, commit 952ddce97: extracted mergedAndValidatedFrozenAddresses (app/component_manager.go)
  - appends cfg.FrozenAddresses to the mempool's built-in list instead of replacing it, and validates
  each with util.DecodeAddress against the active network's prefix, failing node startup with the
  invalid address named instead of silently freezing nothing.
- tests: TestMergedAndValidatedFrozenAddresses (app package) - appends instead of replacing, rejects a
  malformed address, rejects a wrong-network address, and leaves the defaults untouched when nothing
  extra is given. gofmt/vet/staticcheck clean, full app package suite green.
- commit: 952ddce97
## HTN-160
- title: a negative --minrelaytxfee is accepted and wraps into a huge minimum relay fee
- status: fixed
- severity: low
- area: config
- evidence: infrastructure/config/config.go converts the flag with util.NewAmount, which only rejects NaN and infinities (util/amount.go:79-92); util.Amount is uint64, so round() turns a negative fee into an enormous amount, and the check only refused a zero amount although its comment says "Disallow 0 and negative min tx fees". A negative flag was accepted as a huge minimum relay fee, which the mempool clamps to MaxSompi, so the node rejected essentially every transaction without an obvious cause.
- repro: static (LoadConfig creates the real app dir, so the check was extracted into minRelayTxFeeIsValid)
- fix_plan: reject values <= 0
- tests: TestMinRelayTxFeeIsValid (flag and amount); mutation check: trusting only the converted amount fails it for negative fees (exit status checked); config tests, go build ./..., staticcheck pass. First commit 2e7824942 checked the converted amount and its own test failed (masked by a `| tail` pipeline) - amended
- commit: d266a16dc
## HTN-161
- title: auto-updater archive extraction writes outside the extraction directory (Zip Slip, escaping symlinks)
- status: fixed
- severity: medium
- area: autoupdate
- evidence: infrastructure/autoupdate/archive.go:61 (zip) and :130 (tar) build each output path as filepath.Join(destDir, entryName) with no check that it stays under destDir, so an entry named ../../x (or with enough ../ segments) is written anywhere the node user can write. The tar symlink branch (:166-171) joins header.Linkname the same way and creates the link, so a link can point outside destDir and a later entry written through it lands outside too. VerifyChecksum (downloader.go:159) is not called by the updater, so nothing verifies the archive before extraction. Reaching this needs a malicious release archive (compromised release or account); it widens that from replacing the binary to overwriting arbitrary files.
- repro: archive test with ../ entries and an escaping symlink
- fix_plan: reject entries whose cleaned path is not inside destDir (zip, tar files, tar symlink targets); do not decide checksum/signature policy (separate needs_human note)
- tests: TestExtractArchiveStaysInsideDestination (zip ../, tar ../, tar escaping symlink; failed before the fix with files written outside destDir); autoupdate tests (exit status checked), go build ./..., staticcheck pass
- commit: 4d512bd14
## HTN-162
- title: the auto-updater installs downloaded release archives without verifying them
- status: needs_human
- severity: medium
- area: autoupdate
- evidence: infrastructure/autoupdate/downloader.go:159 defines VerifyChecksum, but nothing calls it (grep over the repo); updater.go downloads the release asset, extracts it (updater.go:341) and installs it with no checksum or signature check. Transport is HTTPS to GitHub, so this trusts whoever can publish a release (or a compromised account/token) completely, with --autoupdate-install replacing the node binary automatically. Auto-update is opt-in (all flags default false).
- repro: static
- fix_plan: maintainer decision on the trust model - e.g. require a signed checksum file published separately from the archive, verified with a key compiled into the node, before extraction/install; not invented here
- tests: not run
- commit: none
## HTN-163
- title: auto-update install deletes the running node binary before the replacement is in place
- status: fixed
- severity: medium
- area: autoupdate
- evidence: infrastructure/autoupdate/updater.go:623-650 installBinary os.Remove(dest)s the current binary and only then copyFile(src, dest)s the new one. Any failure after the removal (source unreadable, disk full, crash mid-copy) leaves no binary or a truncated one. The caller (:386-411) restores only if the backup exists, and createBackup failures are logged and ignored ("Continue without backup"), so e.g. a full disk that fails both the backup and the copy leaves the node with no binary; a crash mid-copy leaves a truncated binary that the in-process restore never runs for. Auto-update install is opt-in.
- repro: installBinary with a missing source removes the existing destination
- fix_plan: write the new binary to a temporary file next to dest, set permissions, then rename it over dest (atomic on the same filesystem); never remove dest first
- tests: TestInstallBinaryKeepsCurrentBinaryOnFailure (current binary deleted before the fix), TestInstallBinaryReplacesCurrentBinary; autoupdate tests (exit status checked), go build ./..., staticcheck pass
- commit: 5d4463e66
## HTN-164
- title: an automatic update restarts the node with os.Exit, skipping graceful shutdown and racing the new process for the database lock
- status: needs_human
- severity: low
- area: autoupdate
- evidence: infrastructure/autoupdate/updater.go:444-453 installUpdate calls RestartNode when AutoInstall is set; RestartNode (:696-717) starts the new binary with the same arguments and immediately os.Exit(0)s from the updater goroutine. The node's component shutdown (P2P, RPC, utxoindex, database close) never runs, so every automatic update is effectively a crash of the old process (pebble recovers, but in-flight state is dropped), and the new process starts while the old one still holds the datadir lock - if it reaches the lock first it fails to open the database and exits, leaving no node running. Auto-install is opt-in.
- repro: static
- fix_plan: maintainer decision on restart supervision - e.g. request shutdown through signal.ShutdownRequestChannel, let the app finish its normal shutdown, and exec/start the new binary only after the database is closed (or leave restarting to the service manager)
- investigated further 2026-09-19: the fix_plan's own suggested shape ("request shutdown, wait for it
  to finish, then start the new binary") does not actually work in this process model. RestartNode
  runs in a background goroutine spawned by the updater's own check loop, not in main()'s own
  goroutine. In Go, the moment main() returns, the runtime terminates the entire process immediately,
  killing every other goroutine mid-flight - including RestartNode's. So there is no way for
  RestartNode to "wait for app.main() to return, then start the new binary" using ordinary channel
  synchronization: by the time app.main() has actually returned, RestartNode's own goroutine no
  longer exists to act on it. Starting the new binary necessarily has to happen before this process's
  main() returns, which is the ordering the current code already uses.
- what a real fix needs: a hook wired into app.go's own shutdown sequence itself - e.g. a callback
  registered by the updater that app.main() invokes right before it returns (after the DB and every
  other component are actually closed), so the new binary is started from the shutdown path itself
  rather than from a goroutine racing that path. This is a real change to the node's core lifecycle,
  not a quick patch, and getting the ordering subtly wrong risks a new startup/shutdown correctness
  bug in every normal restart, not just auto-update ones. Not attempted this session - needs a
  deliberate design pass. Stays needs_human.
- tests: not run
- commit: none
## HTN-165
- title: ldbtool (and --dbtype=leveldb) can rewrite a pebble datadir through LevelDB corruption recovery
- status: fixed
- severity: medium
- area: db/tools
- evidence: infrastructure/db/database/ldb/leveldb.go:28-33 NewLevelDB retries leveldb.RecoverFile(path, nil) whenever OpenFile returns ErrCorrupted. ldb.FuseLevelDB opens the destination and every source with NewLevelDB (fuse.go:100, :137), and cmd/ldbtool's fuse/copy commands pass user paths straight to it with no engine check. htnd writes pebble by default; if goleveldb reports a pebble directory as corrupted, RecoverFile rebuilds a LevelDB manifest in place and the pebble datadir - even one the user only meant to read as a copy source - is destroyed. CLAUDE.md documents this hazard ("Opening a pebble datadir with the leveldb engine destroys it") but nothing prevents it. The node's --dbtype=leveldb path uses the same wrapper.
- repro: create a pebble database, open it with ldb.NewLevelDB, check the pebble database still opens
- fix_plan: in NewLevelDB, refuse to open (and never recover) a directory that contains pebble's own files; a false positive can only fail the open, never lose data
- tests: TestNewLevelDBLeavesPebbleDirectoryIntact (failed before the fix: the LevelDB open went through on a real pebble directory); ldb and pebble tests (exit status checked), go build ./..., staticcheck pass
- commit: e0a2e143b
## HTN-166
- title: network message-size limits and RPC exposure defaults let unauthenticated clients make the node buffer multi-GB messages
- status: needs_human
- severity: medium
- area: p2p/rpc
- evidence: infrastructure/network/netadapter/server/grpcserver/p2pserver.go:24 p2pMaxMessageSize = 4 GiB (deliberately raised in cff75920a "Increase P2P message max size", 2025-07-23; its comment still says "1GB") and :30 p2pMaxInboundConnections = 0, so gRPC accepts and fully buffers a single inbound P2P message of up to 4 GiB from any unauthenticated connection before any flow validates it - the 500-peer limit (config.go:63 defaultMaxInboundPeers) is enforced by connmanager only after the handshake. RPC: rpcserver.go:15 RPCMaxMessageSize = 1 GiB, config.go:69 DefaultMaxRPCClients = 500, and with no --rpclisten the RPC listener binds all interfaces (config.go:559-561 net.JoinHostPort("", RPCPort)); SafeRPC defaults off. A handful of concurrent maximum-size messages exhausts memory. Same shape as kaspad defaults. Not changed: these are network parameters.
- repro: static
- fix_plan: maintainer decision - size P2P limits to the largest legitimate message (IBD batches, blocks) and/or add a per-connection receive budget; consider a lower RPC max message and localhost as the default RPC listener; fix the stale "1GB" comment
- PARTIALLY FIXED 2026-09-19, commit e6cbe8099: fixed only the stale "1GB" comment next to
  p2pMaxMessageSize (now correctly says 4GiB, with the commit that raised it). The actual sizing
  decision - what the real limits should be, a per-connection receive budget, RPC's default listen
  address - is a genuine operational tradeoff (legitimate IBD/block traffic needs headroom) that
  needs real numbers from production usage, not a value picked here. Still needs_human for that part.
- commit: e6cbe8099
## HTN-167
- title: flow errors that are neither protocol nor rule errors ban the peer, so a local fault bans honest peers
- status: fixed
- severity: low (banning is opt-in: EnableBanning has no default, so without --enablebanning every protocol error only disconnects)
- area: p2p
- evidence: app/protocol/flowcontext/errors.go:50-55 HandleError turns any error that is not a ProtocolError, RuleError, wire-format error or database not-found into protocolerrors.Errorf(true, "unexpected error in %s", ...), i.e. a banning protocol error; app/protocol/protocol.go:115-117 bans the peer's IP when EnableBanning is set. That catch-all is mostly this node's own failures (database I/O errors, a full disk, internal invariant errors from consensus or the mempool), which recur with every peer, so the node bans its honest peers one after another and isolates itself for the ban duration. Introduced by c734ff8da (2026-08-13 "Fix one todo and and return error instead of panic"), whose aim was to stop panicking - the ban flag looks incidental.
- repro: flowcontext test - HandleError with a plain local error must disconnect without banning; rule and wire-format errors keep banning
- fix_plan: keep the non-panicking conversion but with ShouldBan false for unexpected errors
- tests: TestHandleErrorBansOnlyForPeerFaults (local fault got ShouldBan=true before the fix; rule violation and wire-format errors still ban); app/protocol/... tests (exit status checked), go build ./..., staticcheck pass
- commit: 433c430f2
## HTN-168
- title: two P2P ban paths can ban honest peers for this node's own errors (wire-format heuristic, anticone query failures)
- status: needs_human
- severity: low
- area: p2p
- evidence: (1) app/protocol/flowcontext/errors.go:39-45 bans (ShouldBan true) any flow error whose text matches isWireFormatError ("proto: ... wire-format", "protobuf ... parse"). Flows never decode protobuf bytes themselves (no UnmarshalVT/protowire use in app/protocol); peer message decoding fails earlier and is already banned as "received bad message" (protocol.go:54). The consensus stores return raw UnmarshalVT errors unwrapped (e.g. blockstore/block_store.go:191-193), so a corrupted local record read inside a flow produces exactly that text and bans an honest peer - the HTN-167 class by another route. The heuristic was added in 207b30620 to stop a panic on that error, with an explicit "should ban the peer". (2) app/protocol/flows/v8/blockrelay/handle_request_anticone.go:55 wraps every GetAnticone error as a banning protocol error, including local database errors and limit/unknown-hash errors for a peer syncing from a point this node does not have (kaspad does the same). Also reviewed and left as deliberate policy: ibd.go:156 IBD timeout ban and ibd.go:1283 low-IBD-rate ban (both logged as --enablebanning policy). Banning is opt-in (EnableBanning defaults false).
- repro: static
- fix_plan: maintainer decision - e.g. treat wire-format errors reaching HandleError as non-banning (peer decode errors are already banned upstream), and ban anticone failures only for rule/limit violations attributable to the request
- decision (user, 2026-09-19): approved as part of a batch of non-consensus needs_human fixes
  ("continue fixing").
- FIXED 2026-09-19, commit facb59d98: (1) errors.go's isWireFormatError branch now builds a
  non-banning protocolerrors.Errorf(false, ...), matching the not-found branch right above it. (2)
  handle_request_anticone.go's GetAnticone error is now returned unwrapped (return err) instead of
  protocolerrors.Wrap(true, err, ...), matching the sibling GetHashesBetween/GetBlockHeaders calls in
  handle_request_headers.go, so flowcontext.HandleError's own classifier decides - bans only for an
  actual ruleerrors.RuleError. ibd.go's timeout/low-rate bans were reviewed again and left untouched
  (deliberate --enablebanning policy, not this class of bug).
- tests: TestHandleErrorBansOnlyForPeerFaults updated (the wire-format-looking case now expects
  shouldBan=false, was true) plus new TestHandleRequestAnticoneDoesNotForceABanOnNotFound (a fake
  consensus returning a not-found from GetAnticone must not produce a banning protocol error).
  Verified the anticone test fails on the old code (reverted, ran red) and passes with the fix
  restored. Full app/... suite green, gofmt/vet/staticcheck clean.
- commit: facb59d98
## HTN-169
- title: prefix.Deserialize panics on an empty stored prefix instead of returning an error
- status: fixed
- severity: low
- area: db
- evidence: domain/prefixmanager/prefix/prefix.go:36-46 rejects len(prefixBytes) > 1 but then reads prefixBytes[0] without checking for zero length. The node always stores exactly one byte (Serialize), so this only matters for a corrupted or truncated active-prefix/inactive-prefix value, where ActivePrefix / InactivePrefix / DeleteInactivePrefix (prefixmanager/prefix.go:24, :43, :62) panic with index out of range at startup instead of reporting an invalid prefix.
- repro: prefix test deserializing an empty slice
- fix_plan: require exactly one byte
- tests: TestDeserialize (empty and nil panicked before the fix); prefixmanager and domain tests (exit status checked), go build ./..., staticcheck pass
- commit: 0aadca68f
## HTN-170
- title: an empty database version file makes htnd panic at every start
- status: fixed
- severity: low
- area: app/db
- evidence: app/db_version.go:17-24 checkDatabaseVersion reads the version file and immediately takes &versionBytes[0] for unsafe.String without checking the length. createDatabaseVersionFile creates the file and only then writes the version, so a crash, power loss or full disk between the two leaves an empty file; from then on every start panics with index out of range in openDB instead of reporting that the version file is empty.
- repro: app test calling checkDatabaseVersion on a directory whose version file is empty
- fix_plan: return a clear error for an empty version file (do not guess the version)
- tests: app/db_version_test.go (empty file panicked before the fix; create failure mutation-checked by restoring return nil); go build ./..., gofmt, staticcheck ./app pass
- commit: 56d0c8f48

## HTN-171
- title: DecodeAddress accepts multisig-PKH addresses of any payload length, silently changing the script hash
- status: fixed
- severity: low
- area: util/address
- evidence: util/address.go DecodeAddress case multiSigPKHAddrID does `var hash [32]byte; copy(hash[:], decoded)` with no length check. Every other address type requires an exact payload length (newAddressPubKey, newAddressPubKeyHashFromHash, newAddressScriptHashFromHash, ...). A multisig-PKH address string with a valid checksum but a short payload decodes to a zero-padded hash, and a long one is truncated, so wallet sends and RPC address queries use a script hash that differs from the one encoded in the address - funds sent to it are unspendable. NewAddressMultiSigPKH only takes *[32]byte, so no correctly generated address has another length. Not consensus: address parsing is client/RPC side. Found while fuzzing DecodeAddress (6.9M execs, no panics).
- repro: util test decoding encodeAddress(prefix, 20-byte payload, multiSigPKHAddrID) and a 40-byte payload
- fix_plan: reject len(decoded) != blake2b.Size256 before building the hash, like newAddressScriptHashFromHash
- tests: util/multisig_pkh_length_test.go (0-byte payload decoded to an all-zero hash before the fix); util, txscript, libhtnwallet tests, build, gofmt, staticcheck ./util pass
- commit: 819141d76

## HTN-172
- title: GetPaginatedUTXOsByAddresses skips one UTXO at every offset, so offset 0 never returns the first UTXO
- status: fixed
- severity: medium
- area: utxoindex/rpc
- evidence: domain/utxoindex/store.go PaginatedUTXOs collects entries only when `iterator > offset` (both in the limit==0 counting pass and the fill pass). With offset 0 the first stored UTXO is skipped; paging with offset += limit misses one UTXO per page, and since offset is uint32 no request can ever return the first UTXO of an address. Wallets and explorers using the paginated RPC under-report balances and cannot spend the skipped coins through it.
- repro: store test with 3 UTXOs for one script: PaginatedUTXOs(offset 0, limit 0) should return 3, (offset 1, limit 1) should return the second entry
- fix_plan: `iterator >= offset` in both passes
- tests: domain/utxoindex/paginated_offset_test.go (offset 0 returned 4 of 5 before the fix; page walk visits every UTXO once); utxoindex and app/rpc tests, build, gofmt, staticcheck pass
- commit: bc87ad238

## HTN-173
- title: rpcclient.GetPaginatedUTXOsByAddresses panics on every response because it asserts the non-paginated message type
- status: fixed
- severity: low
- area: rpcclient
- evidence: infrastructure/network/rpcclient/rpc_get_paginated_utxos_by_addresses.go:15 asserts *appmessage.GetUTXOsByAddressesResponseMessage on the route for CmdGetPaginatedUTXOsByAddressesResponseMessage, whose messages are *GetPaginatedUTXOsByAddressesResponseMessage (handler returns that type). The unchecked assertion panics in the caller's process.
- root cause (2026-09-15): protowire GetPaginatedUtxosByAddressesResponseMessage.toAppMessage builds *appmessage.GetUTXOsByAddressesResponseMessage (command GetUTXOsByAddressesResponse, code 102), confirmed by a round-trip test. The client router therefore delivers the paginated response to the non-paginated route: GetPaginatedUTXOsByAddresses always times out, and a concurrent GetUTXOsByAddresses call on the same client can receive the paginated page as its answer. The rpcclient assertion is wrong too, so fixing only the converter would turn the timeout into a panic.
- repro: rpcclient/paginated_utxos_response_test.go (in-process gRPC server) times out before the fix
- fix_plan: converter returns *appmessage.GetPaginatedUTXOsByAddressesResponseMessage; rpcclient asserts and returns that type (no in-tree callers)
- tests: rpcclient/paginated_utxos_response_test.go (timed out before the fix); rpcclient and protowire tests, go build ./..., go vet ./cmd/..., gofmt, staticcheck pass. Converter name scan over protowire found no other return-type mismatch (25 hits are Msg*/sub-type naming only)
- commit: a7a842bc6

## HTN-174
- title: StopNotifyingPruningPointUTXOSetOverride starts notifications instead of stopping them, and its response cannot be sent
- status: fixed
- severity: low
- area: rpc/appmessage
- evidence: app/appmessage/rpc_notify_pruning_point_utxo_set_override.go:59-61 and :76-78 - StopNotifyingPruningPointUTXOSetOverrideRequestMessage.Command() returns CmdNotifyPruningPointUTXOSetOverrideRequestMessage and the Stop response returns the Notify response command. app/rpc/rpc.go dispatches by request.Command(), so a stop request runs HandleNotifyPruningPointUTXOSetOverrideRequest: it enables the notifications the client asked to stop and replies with a Notify response. protowire/wire.go toRPCPayload also has no case for StopNotifyingPruningPointUTXOSetOverrideResponseMessage (and there is no fromAppMessage for it), so even the right handler's response could not be encoded. rpcclient.StopNotifyingPruningPointUTXOSetOverride therefore never succeeds. The proto messages already exist (messages.proto 1070/1071), so no regeneration is needed. Present since kaspad (2adb4f5d0). Not consensus; handlers ignore the message argument, so no crash.
- repro: appmessage Command() of both Stop messages; protowire FromAppMessage of the Stop response round-trips to the Stop type
- fix_plan: return the Stop commands from both Command() methods; add HoosatdMessage_StopNotifyingPruningPointUTXOSetOverrideResponse.fromAppMessage and the wire.go case, mirroring the Notify response
- tests: appmessage/stop_notifying_pruning_point_utxo_set_override_test.go (request command was Notify before the fix) and protowire/stop_notifying_pruning_point_utxo_set_override_test.go (FromAppMessage: unknown message type before the fix); appmessage, protowire, app/rpc, rpcclient tests, build, gofmt, staticcheck pass
- commit: 506978ef5

## HTN-175
- title: rpcclient Ban and Unban wait on the request route, so they always time out
- status: fixed
- severity: low
- area: rpcclient
- evidence: infrastructure/network/rpcclient/rpc_ban.go:7 and rpc_unban.go:7 dequeue from c.route(appmessage.CmdBanRequestMessage / CmdUnbanRequestMessage). The server answers with BanResponse/UnbanResponse, which the client router puts on the response routes, so both methods always return a timeout even though the node applied the ban. No in-tree callers. Found by scanning rpcclient methods for route/assertion mismatches (all other hits are multi-route notification registrations).
- repro: in-process gRPC server answering BanRequest with BanResponse; client.Ban times out before the fix
- fix_plan: dequeue from CmdBanResponseMessage / CmdUnbanResponseMessage
- tests: rpcclient/ban_response_route_test.go (Ban timed out before the fix); rpcclient tests, gofmt, staticcheck pass
- commit: 3ceed7332

## HTN-176
- title: ldbtool copy --fresh deletes the source database when the destination is the source or contains it
- status: fixed
- severity: medium
- area: cmd/ldbtool
- evidence: cmd/ldbtool/main.go copy subcommand runs os.RemoveAll(dest) when --fresh is set, before CopyLevelDB opens anything and without comparing dest to src. `ldbtool copy --fresh db db`, a trailing-slash or relative spelling of the same path, or a dest that is a parent of src (`copy --fresh /data/htnd/datadir2/db /data/htnd`) removes the source datadir and then fails or copies nothing. The flag is documented as removing the existing destination, not the source. This adds a safety check; it does not change what --fresh does to a real destination.
- repro: package main test of the guard with same path, alternate spelling, and parent directory
- fix_plan: before RemoveAll, resolve both paths (Abs + Clean, EvalSymlinks when they exist) and refuse when dest equals src or src lies inside dest
- tests: cmd/ldbtool/fresh_copy_test.go (same path, trailing separator, relative, unclean, parent, grandparent, symlink refused; siblings incl. shared prefix, inside source, elsewhere allowed); built-binary repro deleted both marker sources before the fix and keeps both after; vet, gofmt, staticcheck pass
- commit: 4433b193d

## HTN-177
- title: a prebuilt 27 MB pebble-tool ELF binary is committed without its source
- status: needs_human
- severity: low
- area: tools/pebble-tool
- evidence: `git ls-files tools/pebble-tool` lists README.md and pebble-tool; the latter is an x86-64 ELF executable (27,607,704 bytes, not stripped) and there is no Go source in the directory. CLAUDE.md describes pebble-tool as a tool that inspects or modifies pebble datadirs. A binary that cannot be rebuilt or reviewed from the repository is a supply-chain and reproducibility risk for a tool pointed at node datadirs, and it bloats every clone.
- repro: static (git ls-files, file)
- fix_plan: maintainer decision - restore the source (or point the README at where it lives) and remove the binary from git, or document its provenance
- investigated further 2026-09-19 (`git log --all -- tools/pebble-tool`): both the binary and the
  README were added together in a single commit, 7148270f0 ("Revert WAL disable by default - fix
  tips growing and DAAScore not updating", authored 2026-08-09, "Generated by Mistral Vibe") - a
  commit whose actual subject is a completely unrelated WAL setting fix. The tool was never
  deliberately added with its own history; it was bundled into an unrelated automated commit, and no
  source has ever existed in this repository at any point. The README's own "From Source: cd
  tools/pebble-tool && go build ." instructions describe a source layout that was never actually
  committed.
- left alone: given this is a tool that reads AND WRITES pebble datadirs directly (per CLAUDE.md,
  "never run except on a copy"), writing a from-scratch reimplementation to match the README's
  documented command surface without the original source to verify against would be guessing at the
  exact behavior of a data-mutating tool - a materially different risk than this session's other
  fixes. Not attempted. The user's call is either: someone locates the real source (it may exist
  outside git, e.g. wherever the Mistral Vibe run that produced 7148270f0 kept its working files) and
  it gets committed properly, or the binary is removed and the tool is rewritten deliberately with
  review, or its provenance is accepted and documented as-is.
- tests: not applicable
- commit: none

## HTN-178
- title: peer-advertised addresses with a malformed IP length pass routability checks and make the node dial its own localhost ports
- status: fixed
- severity: low
- area: addressmanager
- evidence: infrastructure/network/addressmanager/network.go IsValid only rejects a nil IP. protowire NetAddress.toAppMessage copies the peer's IP bytes unchecked, so an addresses/version message can carry an IP of 0, 3, 5 or 17 bytes; the RFC and IsLocal checks never match such a slice, IsRoutable returns true, and addAddressNoLock stores it (scratch test: len 0/3/5/17 stored, nil rejected; all collapse onto one zero key per port). connmanager dials netAddress.TCPAddress().String(): an empty IP renders as ":<port>", which Go dials on the local system, bypassing the IsLocal filter that keeps loopback addresses out of the table (and P2PConnect's loopback refusal is off by default and does not recognise an empty host). A peer can thereby make nodes open connections to arbitrary local ports. Legitimate NetAddresses always come from ParseIP/To4/To16 and are 4 or 16 bytes.
- repro: addressmanager test - IsValid/IsRoutable of 0/3/5/17-byte IPs, and AddAddresses must not store an address whose TCPAddress renders without a host
- fix_plan: IsValid also requires len(IP) to be net.IPv4len or net.IPv6len
- tests: addressmanager/malformed_ip_test.go (0/3/5/17-byte IPs accepted and stored before the fix, stored entry dialed "?0808080808:42421"); addressmanager, connmanager, netadapter, app/protocol tests, build, gofmt, staticcheck pass
- commit: ed41fbd54

## HTN-179
- title: checkRequestedConnections renames activeRequested entries while ranging over the map, so a live requested peer can be treated as disconnected and redialed
- status: fixed
- severity: low
- area: connmanager
- evidence: infrastructure/network/connmanager/connection_requests.go:39-45 - inside `for address, connReq := range c.activeRequested`, a request matched to a differently spelled live address (hostname vs IP:port via addressesMatch) is deleted and reinserted under matchedAddress, then its connection is removed from connSet. The Go spec allows entries added during range to be visited in the same loop; when the new key is visited, findRequestedConnectionInSet no longer finds the connection (already removed from connSet), so the request is dropped as disconnected: a permanent request goes back to pending with nextAttempt=now and the pending loop immediately dials the already-connected peer again (rejected as an existing peer), and a one-try request stops being tracked so its connection is left to the regular outbound/inbound handling. Reachable with --addpeer/--connect or the AddPeer RPC given a hostname. RemoveConnection panics "unimplemented" but has no callers (dead code).
- measured (go1.27.1, scratch program renaming entries during range, 20000 runs each): maps of 1-8 entries never visit a renamed key; 9 entries 44% (one rename) / 75% (all renamed), 16 entries 72% / 99%, 64 entries 93% / 100%. So it bites nodes with 9 or more requested peers (addpeer/connect lists or AddPeer RPC) when any is a hostname matched to a live IP connection; a single request never triggers it.
- repro: connmanager/requested_rename_test.go - two real NetAdapters, hostname request matched to the live 127.0.0.1 connection plus 8 unreachable one-try requests, 300 attempts
- fix_plan: collect the renames and apply them after the range loop
- tests: connmanager/requested_rename_test.go (dropped the live request on attempt 3 before the fix; passes 3x300 attempts after); connmanager tests -count=3, build, gofmt, staticcheck pass
- commit: c0bf8ad80

## HTN-180
- title: a relayed transaction spending an out-of-range output index of a mempool transaction crashes the node
- status: fixed
- severity: high
- area: mempool
- evidence: domain/miningmanager/mempool/fill_inputs_and_get_missing_parents.go fillInputs does `parent.Transaction().Outputs[input.PreviousOutpoint.Index]` for every input whose PreviousOutpoint.TransactionID is in the transaction pool. Nothing bounds the index first: getParentTransactionsInPool matches by transaction ID only, validateTransactionPreUTXOEntry runs only mempool standardness (skipped with --acceptnonstandard) and checkDoubleSpends (a map lookup), and consensus validation (ValidateTransactionAndPopulateWithConsensusData) runs after fillInputs. The P2P relay flow calls MiningManager.ValidateAndInsertTransaction for transactions received from peers, with no recover. Mempool transaction IDs are public (they are relayed), so any peer can send a transaction with an input (mempoolTxID, index >= len(outputs)) and panic the node with index out of range.
- repro: miningmanager test - insert a transaction, then submit a child spending output index len(parent.Outputs) of it
- fix_plan: in fillInputs skip inputs whose index is out of range, leaving UTXOEntry nil so consensus reports the outpoint as missing (the same outcome as spending a non-existent output of any other transaction)
- tests: mempool/out_of_range_parent_output_test.go (panicked "index out of range [1] with length 1" at fill_inputs_and_get_missing_parents.go:44 before the fix; indexes len(outputs) and MaxUint32, with and without orphans); domain/miningmanager/... and transactionrelay tests, build, gofmt, staticcheck pass
- commit: 0fec96f9d

## HTN-181
- title: HandleRelayedTransactions queues peer inv messages without a bound while waiting for transaction responses
- status: fixed
- severity: medium
- area: p2p/transactionrelay
- evidence: app/protocol/flows/v8/transactionrelay/handle_relayed_transactions.go readMsgTxOrNotFound appends every MsgInvTransaction that arrives while it waits for a requested transaction to flow.invsQueue, with no cap. One request can cover up to MaxInvPerMsg = 1<<17 = 131,072 transaction IDs and each response is awaited for up to common.DefaultTimeout = 600s, so a peer that answers slowly (just inside the timeout) can keep the flow in receiveTransactions for a very long time while sending inv messages continuously; the route capacity (10,000) only bounds messages not yet dequeued, and each dequeued inv (up to 131,072 IDs, several MB as DomainTransactionIDs) is retained in invsQueue. Memory grows until the node runs out. Same code as upstream kaspad. Related to the message-size limits parked in HTN-166.
- repro: static
- fix_plan: maintainer decision, since it is a P2P acceptance rule: cap the queued invs (count or total IDs) and either drop further invs (they are optional; the receive loop already drops invs on a full route) or disconnect without banning when the cap is exceeded
- tests: transactionrelay/queued_invs_bound_test.go (7 full invs queued 917504 IDs before the fix, bound 524288); transactionrelay tests -race, build, gofmt, staticcheck pass
- decision: user asked to continue with critical issues (2026-09-15); excess invs are dropped, no disconnect
- commit: a42b094e8

## HTN-182
- title: one peer can block every other node from syncing the pruning point and its anticone from this node
- status: fixed
- severity: medium
- area: p2p/blockrelay (IBD serving)
- evidence: app/protocol/flows/v8/blockrelay/handle_pruning_point_and_its_anticone_requests.go takes the package-global isBusy flag (CompareAndSwap 0->1, released by defer) for the whole of a request. After every getIBDBatchSize() (495) blocks it waits for MsgRequestNextPruningPointAndItsAnticoneBlocks with incomingRoute.Dequeue() - no timeout, by design ("we don't care if the syncee takes its time"). A peer that sends RequestPruningPointAndItsAnticone and then never sends the next request, while staying connected (answering pings), keeps isBusy set indefinitely; every other peer's request meanwhile returns "node is busy with other pruning point anticone requests" and is disconnected. With an anticone larger than one batch this lets a single connection deny pruning-point IBD from this node to the whole network. The flag also ignores the first message's type (a RequestNext message starts a full serve), which is harmless.
- repro: static
- fix_plan: maintainer decision (changes IBD serving behaviour): e.g. release isBusy while waiting for the syncee's next request and re-acquire it per batch, or bound the wait with a generous timeout, or make the limit per peer / a small semaphore instead of one global flag
- tests: blockrelay/pruning_point_anticone_busy_test.go (stub consensus, 496-block anticone: second syncee refused "node is busy" while the first waited, before the fix); blockrelay package tests, -race x5, build, gofmt, staticcheck pass
- decision: user asked to continue with critical issues (2026-09-15); fix keeps the busy refusal for concurrent computation and unlimited syncee time, only releases the slot during the idle wait
- commit: 81100af4b

## HTN-183
- title: IBDBlockLocator messages have no hash-count cap, and serving one costs a full block read per hash under the consensus lock
- status: fixed
- severity: medium
- area: p2p/blockrelay
- evidence: infrastructure/network/netadapter/server/grpcserver/protowire/p2p_ibd_block_locator.go converts BlockLocatorHashes without a count check, unlike BlockLocatorMessage (MaxBlockLocatorsPerMsg = 500, p2p_block_locator.go). app/protocol/flows/v8/blockrelay/handle_ibd_block_locator.go calls Consensus().GetBlock (full block) and IsInSelectedParentChainOf for every hash until one is on targetHash's selected chain; each call takes the consensus lock. A peer can repeat a known hash that is not in targetHash's selected parent chain, so no early break happens, and a P2P message may be up to 4 GiB (HTN-166): millions of full block reads contending with block processing. Honest locators are logarithmic (both syncmanager builders double the step each iteration, so tens of hashes at most), and no code in this repository sends MsgIBDBlockLocator (only the constructor exists), so a 500 cap affects no HTND syncee - but it is still a new P2P acceptance rule for other implementations.
- repro: static
- fix_plan: maintainer decision, same as HTN-115: cap len(BlockLocatorHashes) at appmessage.MaxBlockLocatorsPerMsg in IbdBlockLocatorMessage.toAppMessage
- tests: blockrelay/ibd_block_locator_bound_test.go (10000 block reads for one 10000-hash locator before the fix, at most 500 after); blockrelay tests, -race, build, gofmt, staticcheck pass
- decision: user asked to continue with critical issues (2026-09-15); work bounded in the handler instead of rejecting the message, so no peer is disconnected
- commit: 1425b20bb

## HTN-184
- title: htnd overrides a GOMEMLIMIT written with a unit suffix (or "off") with its 8 GB default
- status: fixed
- severity: low
- area: main
- evidence: main.go init() calls debug.SetMemoryLimit(getEnvInt("GOMEMLIMIT", 8_000_000_000)); getEnvInt only accepts a plain positive integer (strconv.Atoi) and otherwise returns the default. The Go runtime has already parsed GOMEMLIMIT at startup, including the documented forms with a unit suffix ("16GiB", "512MiB") and "off", so for those values htnd replaces the operator's limit with 8 GB: a node on a small machine configured with GOMEMLIMIT=4GiB runs with an 8 GB soft limit and can be OOM-killed, and a large machine's higher limit is lowered, causing needless GC pressure. CLAUDE.md documents GOMEMLIMIT as a tunable with main.go supplying the 8 GB default.
- repro: scratch program applying the same init logic under GOMEMLIMIT=4GiB / off
- fix_plan: apply the 8 GB default only when GOMEMLIMIT is unset; when it is set the runtime has already applied it
- tests: main_test.go TestApplyDefaultMemoryLimit (unset -> 8 GB; 4GiB, off, plain bytes -> untouched); scratch program before the fix: 4GiB/16GiB/off all ended at 8000000000; go build, vet, staticcheck, gofmt pass
- commit: 9d54ad3b1

## HTN-185
- title: GetTransactionStatus reports about 2^64 confirmations for a transaction in a block above the selected parent's blue score
- status: fixed
- severity: low
- area: rpc
- evidence: app/rpc/rpchandlers/get_transaction_status.go computed confirmations := selectedParentInfo.BlueScore - blockInfo.BlueScore + 1 on uint64 values before the acceptance verdict, and returned it with the "pending" and "unknown" (inconclusive search) answers. A containing block can have a higher blue score than the virtual selected parent - an unmerged tip with more blues but less blue work, or a block beyond a virtual that is still being resolved - and then the subtraction wrapped. The accepted path uses the same expression with the accepting chain block, which cannot be above the selected parent. Found while checking a note from the HTN-1xx sweep (AGENT_STATE "minor, not fixed").
- repro: TestConfirmationsSinceDoesNotWrap (new helper; before the fix it could only fail to compile - mutation with the old in-place subtraction gives 18446744073709551567 confirmations for a block 50 above the selected parent)
- fix_plan: confirmationsSince(selectedParentBlueScore, blockBlueScore) returns 0 for a block above the selected parent, otherwise the old count; used on both paths
- tests: rpchandlers test passes; mutation-checked; app/rpc/... tests, go build ./..., vet, gofmt, staticcheck pass (app-only change, domain suite not needed)
- commit: 6b8bccd52
\n
## HTN-186
- title: RandomAddresses can return the same address twice, so connmanager dials it twice in one round
- status: fixed
- severity: low
- area: p2p/addressmanager
- evidence: infrastructure/network/addressmanager/addressrandomize.go - RandomAddresses zeroes the weight of each picked address, but weightedRand did not skip zero-weight entries: a random point of exactly 0 (1 in 2^24 per pick) matched a zero-weight first entry, and when float32 rounding left the cumulative shares short of the random point the scan fell through to the last entry regardless of its weight. Scratch simulation over realistic weight tables: shares end below the largest random point in about half the tables, short by ~1.2e-7 (100 addresses) to ~3.5e-5 (20,000). connmanager/outgoing_connections.go dials every returned address; the duplicate dial adds no connection, wastes that round's slot and calls MarkConnectionFailure against a peer that just connected. Found while checking a note from the HTN-1xx sweep (AGENT_STATE "minor, not fixed").
- repro: TestWeightedRandNeverReturnsAPickedAddress (weightedRandAt seam; mutation with the old scan returns the picked index for random point 0 and for a seeded rounding-shortfall table)
- fix_plan: skip zero-weight entries and fall back to the last entry that has weight; odds among unpicked addresses unchanged
- tests: addressmanager and connmanager tests, go build ./..., vet, gofmt, staticcheck pass
- commit: 25c113eca
\n
## HTN-187
- title: htnwallet daemon Broadcast drops the IDs of transactions already submitted when a later one in the request fails
- status: fixed
- severity: low
- area: wallet
- evidence: cmd/htnwallet/daemon/server/broadcast.go submits a request's transactions one by one and returned only the failing transaction's error; the gRPC error replaces the response, so the IDs of transactions already accepted into the node's mempool never reached the caller. send.go/vote.go report how many were broadcast before the failing batch, but not which transactions inside that batch went out. Found while checking a note from the HTN-1xx sweep (AGENT_STATE "Note only").
- repro: TestBroadcastFailureNamesTheTransactionsAlreadySubmitted (in-process node accepting the first submission and rejecting the second; before the fix the error did not name the first transaction)
- fix_plan: wrap the error with the IDs of the request's already-submitted transactions (also on a decode failure after earlier submissions); keep the node's message as the cause so the used-outpoint release check still matches; no response or proto change
- tests: cmd/htnwallet/... tests (7 ok), go build ./..., vet, gofmt, staticcheck pass
- commit: 7c45a617c

## HTN-188
- title: Race job times out in blockvalidator because two pruning tests now need ~29k testnet blocks for the pruning point to move
- status: fixed
- severity: medium
- area: tests/ci
- evidence:
  - Nightly race job 2026-09-15 (`go test -timeout 20m -race -p 2 ./...`, master after ee22f57e9): `FAIL domain/consensus/processes/blockvalidator 1200.160s`, "panic: test timed out after 20m0s", running `TestCheckPruningPointViolation/hoosat-testnet (1m20s)`, stack in `tc.AddBlock` inside the loop at domain/consensus/processes/blockvalidator/pruning_violation_proof_of_work_and_difficulty_test.go:250. The test running at the deadline had only used 1m20s. The ~18 minutes before it went to the preceding test, `TestCheckParentBlockBodiesExist/hoosat-testnet`, which also loops until the pruning point moves.
  - Both tests shrink only the version-1 parameters, "to reduce the pruning depth to 6 blocks": `FinalityDuration = []time.Duration{2 * TargetTimePerBlock[0]}` and `K[0] = 0` (pruning_violation_proof_of_work_and_difficulty_test.go:236-237, block_body_in_context_test.go:96-97). They then run `for { AddBlock; if PruningPoint() != genesis { break } }` with no upper bound.
  - Since ee22f57e9 (HTN-001) the pruning depth follows `blockversion.Current` (the DAA-derived version of the chain) and is no longer frozen at construction with the global version 1. Testnet `POWScores` is `[1 50 100 150 200 ...]` (domain/dagconfig/params.go:536), so the test chain reaches version 5 at DAA score 200. Mainnet's activation scores are out of reach, so the mainnet subtests are unaffected.
  - Depth at version 5 with the test's config, from `PruningDepthForBlockVersion` (domain/dagconfig/params.go:271): the single FinalityDuration entry clamps to 2s, divided by 200ms gives 10; PruningMultiplier[4]=1; K[4]=40 (the test only zeroed K[0]); MergeSetSizeLimit=180. Result: 2*10*1 + 4*180*40 + 2*40 + 2 = **28902 blocks**, compared with 6 before ee22f57e9. Each subtest therefore builds a ~29k-block chain. Under `-race` this does not fit in 20m. HTN-001's own notes hit the same problem in TestValidateAndInsertImportedPruningPoint and pinned that test to version 1.
  - Local `-race -v` run of the package, 2026-09-15: TestCheckBlockIsNotPruned/hoosat-testnet passes in 0.49s. It also sets `DifficultyAdjustmentWindowSize = []int{0}`, so the DAA score, and with it the version, never advances. Measured with `-race` (timeout raised to 60m): TestCheckParentBlockBodiesExist/hoosat-testnet 904.00s, TestCheckPruningPointViolation/hoosat-testnet 916.96s, both PASS. The package took 1836.975s in total. Every other test and subtest in it finishes in under 3s, and both mainnet subtests take about 0.47s. The two testnet subtests account for 99% of the package time, and either one uses about 15 of the 20 minutes by itself.
  - Local run without `-race`, 2026-09-15: `TestCheckPruningPointViolation/hoosat-testnet` alone takes 114.49s, which makes the race slowdown about 8x.
  - The tests are also no longer testing what their comments say. The "pruning point violation" and "header-only parent below the pruning point" cases are exercised against a 28902-deep pruning point on a version-5+ chain, not the intended 6-block one.
  - 2026-09-15 update: blockprocessor TestGetPruningPointUTXOs, listed below as "passes today", fails in a plain `go test ./...`: hoosat-testnet "Returned an unexpected amount of UTXOs. Want: 904, got: 936" after ~105s (~29k blocks), identically on committed HEAD; it passes on c7bb663dd in 0.1s. Fixed in the working tree with this issue's fix_plan (POWScores={MaxUint64}, overrides indexed at version 1, K table copied) - passes in 0.2s; committed 79a613da2. The two blockvalidator tests are still unfixed.
- repro: CI log above; `go test -tags=ci -race -run 'TestCheckPruningPointViolation/hoosat-testnet' ./domain/consensus/processes/blockvalidator/`
- fix_plan: Pin both tests' chains to version 1 with `consensusConfig.POWScores = []uint64{math.MaxUint64}`, as ee22f57e9 did for TestValidateAndInsertImportedPruningPoint and pruningmanager/pruning_test.go:55. Give the "add blocks until the pruning point changes" loops a bound, e.g. fail after `2*PruningDepthForBlockVersion(1)+10` blocks, so a future parameter change fails in seconds instead of hanging the package until the 20m alarm. Audit the other tests that shrink only `K[GetBlockVersion()-1]` without pinning POWScores: blockprocessor TestGetPruningPointUTXOs (passes today), integration TestIBDWithPruning on simnet (POWScores [5], so version 2 at DAA 5 with K[1]=18), and stability-tests fast-pruning-ibd-test generate_test.go. Check that each still runs the depth its comment claims. Verify with `-race` on the blockvalidator package: it should finish in well under a minute, as it did before ee22f57e9.
- tests: TestCheckPruningPointViolation and TestCheckParentBlockBodiesExist pass on both nets in <0.2s (were ~115s each); blockvalidator package -race -tags=ci 16.5s (was 1837s) and non-ci ok; TestGetPruningPointUTXOs 0.2s; full go test ./... green with and without -tags=ci before each commit
- commit: 79a613da2 (blockprocessor TestGetPruningPointUTXOs), d6da9e370 (blockvalidator pruning tests). Not done: loop bounds; TestCheckBlockIsNotPruned left alone (DAA window 0 keeps it at version 1)

## HTN-189
- title: Integration tests race on gRPC's global buffer pool: newGRPCServer calls experimental.SetDefaultBufferPool while leaked RPC clients reconnect
- status: fixed
- severity: medium
- area: rpc/tests
- evidence:
  - Nightly race job 2026-09-15, `testing/integration` (the "Found 0 WALs" pebble lines precede the report; the output was truncated before the package's FAIL line): "WARNING: DATA RACE" on grpc's package variable `mem.defaultBufferPool`.
    - Read: `grpc.defaultDialOptions` (grpc@v1.83.1 dialoptions.go:726, `mem.DefaultBufferPool()`) <- `grpc.NewClient` <- grpcclient.Connect (infrastructure/network/rpcclient/grpcclient/grpcclient.go:38) <- `RPCClient.connect` <- `Reconnect` <- `handleClientDisconnected` <- `handleClientError` <- the receive loop's Recv error.
    - Previous write: `internal.SetDefaultBufferPool` closure (mem/buffer_pool.go:60) <- `experimental.SetDefaultBufferPool` <- `grpcserver.newGRPCServer` (infrastructure/network/netadapter/server/grpcserver/grpc_server.go:57) <- NewRPCServer <- NewNetAdapter <- `app.NewComponentManager` (a later test's `setApp`). The serialization/consensushashing frames interleaved in the write stack are race-detector history corruption, not real callers.
  - Library contract: grpc documents `SetDefaultBufferPool` as "must only be called during initialization time (i.e. in an init() function), and is not thread-safe". HTND calls it on every server construction: twice per node (P2P and RPC servers), again for every in-process node a test starts, and in the htnwallet daemon (cmd/htnwallet/daemon/server/server.go:145), where it runs *after* `syncLoop` has been spawned. That is a real-process race if the sync loop's RPC client reconnects at that moment.
  - Why a client was reconnecting mid-run: testing/integration/rpc_test.go `TestRPCMaxInboundConnections` does `rpcClients := make([]*testRPCClient, 0, RPCMaxClients); defer closeRPCClients(t, rpcClients)`. The deferred call's arguments are evaluated at the defer, so it receives the empty slice. The 500 (`DefaultMaxRPCClients`) accepted clients and the replacement client are never closed. On teardown, `app.Stop()` drops their streams. Each one's receive loop errors, `handleClientError -> Reconnect` runs because `isClosed` is 0, and `Reconnect` retries `connect()` (grpc.NewClient) every 10s for the rest of the test binary. Later tests (selected_parent_chain, tx_relay, utxo_index, virtual_selected_parent_blue_score) build new nodes and so call SetDefaultBufferPool again: the reported race.
  - Side effect of the same leak: the orphaned clients keep dialling `rpcAddress1`, which later tests reuse. They can connect to a later test's node, run their GetInfo version check, and take RPC client slots (limit 500) and server goroutines. That is a plausible source of flakiness in later integration tests. Not observed directly.
  - 2026-09-15 check: `go list -deps ./cmd/htnwallet/daemon/server/` includes netadapter/server/grpcserver, and the repo has exactly two SetDefaultBufferPool call sites (grpc_server.go:57 newGRPCServer, cmd/htnwallet/daemon/server/server.go:145). So an init() in grpcserver runs before the daemon main and its own late call can be removed. Edit script prepared: scratchpad/htn189/apply_htn189.py (not applied; waits for the HTN-190 gate to finish so that gate tests an unchanged tree).
  - 2026-09-15 fix verification (fix applied in tree, uncommitted): `go test -race -tags=ci ./testing/integration/` reported 0 DATA RACE and 0 reconnect warnings, but TestRPCClientGoroutineLeak failed once ("Number of goroutines is increasing ... (42 -> 54)"; the test allows +10 goroutines 10ms after each client close, unchanged since kaspad 9ee409afa). Run alone under -race it passes twice on HEAD without the fix and twice on the tree with it (2.4-2.6s), so the one failure depends on package order/timing, not on the fix. Full go test ./... without race: 107 ok with and without -tags=ci. Package-level race runs on HEAD vs tree running (scratchpad/htn189/pkgrace_*.log).
- repro: CI log above; `go test -race -tags=ci -run 'TestRPCMaxInboundConnections|TestSelectedParentChain' ./testing/integration/`. The second test must start a node while the leaked clients are between 10s retries, so it may need a sleep of more than 10s between the tests to trigger reliably.
- fix_plan: Two independent changes, both needed.
  - (1) Test leak: `defer func() { closeRPCClients(t, rpcClients) }()` so the closure sees the filled slice, including the replacement client stored in `rpcClients[0]`.
  - (2) Global pool: set the tiered pool once at process init and never at server construction. Move the `mem.NewTieredBufferPool(...)` and `experimental.SetDefaultBufferPool` pair into an `init()` in the grpcserver package, and remove the call from `newGRPCServer`. For the htnwallet daemon, either rely on the grpcserver package init if it is already linked in, or add its own `init()`. Keep the process-global setter rather than switching to per-server `experimental.BufferPool`/`WithBufferPool` options, because the global also sets the proto codec's pool, which has no per-connection option. Note that the node's outgoing P2P and RPC clients use the tiered pool too, which is already true today for any client created after the first server.
  - Regression check: `go test -race -tags=ci ./testing/integration/` passes, and a goroutine count after TestRPCMaxInboundConnections's teardown (or a check that no `Attempting to reconnect` warning is logged) confirms no client outlives its test.
- tests: go test -race -tags=ci ./testing/integration/ 2/2 pass with the fix (0 DATA RACE, 0 reconnect warnings) and 2/2 on the previous HEAD; one earlier run with the fix failed TestRPCClientGoroutineLeak (42->54 vs +10 allowance) - passes alone under -race with and without the fix (2+2) and did not recur, race-detector timing; race run without the ci tag OOM-killed at a 10 GB cap (not completed); go test ./... 107 ok with and without -tags=ci
- commit: a6bb32462 (grpcserver init() default pool; htnwallet daemon late call removed; TestRPCMaxInboundConnections closure defer)

## HTN-191
- title: Clean-sync ResolveVirtual crawls when the imported pruning point UTXO set matches its header: the first resolved block fails its UTXO commitment and the chain is disqualified block by block
- status: needs_human
- severity: high
- area: consensus/ibd
- evidence:
  - User report 2026-09-15: after IBD, 100-block resolve chunks grow linearly from 5s (11%) to 38s (~40% after 65 min), lockWait about equal to chunk duration.
  - Local repro, fresh HEAD (ae82b01cb) node, 2026-09-15 11:32: imported pruning point 17895f2f3fe85e092db1b090a30d1ac17de87bfe2be628bf5e6e413686f980bd from peer 219.88.72.130 "UTXO set matches its own header commitment". Resolve started 11:42:54; the first resolved block 183f8b75046fd3affc371191bf8c35031886c9f617aedbb64d36beb9ef110840 failed "UTXO commitment is invalid - block header indicates e9b189a7..., but calculated value is 702ef209...: ErrBadUTXOCommitment". No tolerated-issue lines (baseline verified, so blockInheritsKnownUTXOCommitmentOffset is false). The first chunk took 3m26.9s; a transaction in block 4dd05c21... was then rejected for a missing coin (8186226b...:0).
  - CPU profile 30s into resolve: 67% in verifyMultisetSelfConsistency (full 21.6M-entry UTXO set MuHash rebuild, capped at 3 runs/process by expensiveDiagnosticRunsRemaining); goroutine dump in resolveSingleBlockStatus's RuleError branch. After the cap, each disqualified block still pays selectedParentPastUTXOSet.DiffFrom over a past diff that grows while virtual cannot advance - the linear chunk growth.
  - Earlier HEAD attempt the same day imported pruning point cb4a0512... from 104.237.1.174 whose set did NOT match its header (would run tolerant).
  - htnd5 logs: every IBD it did (2026-09-13 19:42 and 20:59, 2026-09-14 10:08 on c7bb663dd) imported a set that did NOT match its header -> "offset UTXO baseline; inherited per-block commitment mismatches are tolerated" -> resolve finished in minutes. So the fast/slow split follows whether the served set matches its header, not the build, unless the c7bb663dd comparison below says otherwise.
  - Code on this path (verify_and_build_utxo.go tolerance, resolve_block_status.go, multisets.go, utxo package) is unchanged since c7bb663dd except the non-missing-input error recording (34b2a9c3d) and the accepted-ID merkle order by block version (same result for v9 blocks).
  - DECIDED 2026-09-15 12:10: a fresh c7bb663dd node synced from the same peer imported the same pruning point 17895f2f (set matches its header) and failed the same first block 183f8b75 with the identical calculated commitment 702ef209...; first chunk 3m20s (HEAD 3m27s), then the same missing-coin rejection. Not a regression from this session's commits: any build that imports a header-matching set disqualifies the network's chain from its first block after the pruning point. Changing that is consensus policy.
  - Chunk growth confirmed on c7bb663dd (same run): seconds per 1% of resolve progress after the first chunk 9, 6/pt (2->4%), 16, 22, 27 - growing linearly as in the user report. Base node stopped 12:15.
- repro: fresh sync with --connect 219.88.72.130:42421 (the peer that served a header-matching set); scratchpad/repro/head.stdout, cpu_early*.
- decision (user, 2026-09-15): operational workaround - no consensus change. Sync clean nodes from a peer that serves the offset pruning point set (as every htnd5 IBD did); document it, and investigate whether IBD peer selection can prefer such peers.
  - Investigation 2026-09-15 (peer choice, no code changed): the import (consensusstatemanager/import_pruning_utxo_set.go verifyAndRepairImportedPruningPointUTXOSet) accepts both kinds of set; a set it rejects with ErrBadPruningPointUTXOSet makes IBD try another peer without banning (blockrelay/ibd_with_headers_proof.go fetchMissingUTXOSet). The only existing knob is the INVERSE of the workaround: hidden --enable-sanity-check-pruning-utxo (config EnableSanityCheckPruningUTXOSet, factory.go:363) sets refuseMismatchedImportedPruningPointUTXOSet, which refuses a set that does NOT match its header. An opt-in "decline a header-matching set" would reuse the same retry path, but it has not been implemented (it would deliberately reject the set the chain committed to). Caveat: the one observed non-matching set (104.237.1.174) was for a different pruning point (cb4a0512) than the matching one (17895f2f from 219.88.72.130), so whether a set matches may follow the pruning point rather than the peer; a fresh HEAD sync with --connect 104.237.1.174:42421 is running to check (scratchpad/repro/offset.stdout).
  - Workaround check 2026-09-15 13:11 (fresh HEAD node, --connect 104.237.1.174:42421, scratchpad/repro/offset.stdout): imported pruning point 49ba41949266eac1167f03223d4e199d195b40283535902339cb3dce709cf2ed "UTXO set does not match its own header" and "still does not match its header after recomputation" -> offset baseline, per-block mismatches tolerated. So this peer served a non-matching set at this pruning point (it also did at cb4a0512 earlier). 250397 block bodies synced from 13:20; resolve speed pending.
  - WORKAROUND CONFIRMED 2026-09-15 13:30: the same HEAD build synced from 104.237.1.174 (offset baseline, pruning point 49ba4194) started resolving at 13:30:21 (virtual DAA 226520440) and ran at a steady 3-4s per 1% of progress (0->1% 8s, then 3,3,3,4,3,3,4,4,4,4,3s), 38% after ~2 minutes, no growth. Compare the header-matching run on c7bb663dd: 9,12,16,22,27,29,51s per 1% and growing. So syncing from a peer that serves the offset set avoids the crawl.
  - WORKAROUND COMPLETED 2026-09-15: the offset-baseline node reached "Resolved virtual" at 13:37:54 and logged "IBD with peer 104.237.1.174:42421 finished successfully" - about 7.5 minutes from the start of resolve (13:30:21), of which one chunk took 3m44s because the pruning point moved during resolve and the pruning point UTXO set was rebuilt (a one-off, not the per-chunk growth). It then accepted 255 of 260 held transactions. Node stopped afterwards.
  - MECHANISM (code read 2026-09-15 17:3x, user asked to fix the slow path): once the first block above a header-matching pruning point is disqualified, every later chain block takes ResolveBlockStatus's disqualified branch (resolve_block_status.go ~75), which computes its past UTXO and stages its diff with stageDiff(block, diff, previousBlockHash) - diff child = its selected parent. resolve.go only calls ReverseUTXODiffs when the processing point is StatusUTXOValid, so on a disqualified chain the diff-child pointers are never turned toward virtual. Each chunk then starts with selectedParentInfo -> restorePastUTXO(disqualified selected parent) (resolve_block_status.go ~203), which walks utxoDiffChild back across every disqualified block resolved so far and merges all their diffs (calculate_past_utxo.go restorePastUTXO), so the per-chunk cost grows with blocks resolved - the linear chunk growth. Confirm with CPU profiles at 10%/25% (scratchpad/slowpath, job b70xe9sns).
  - CODE FIX READY, UNCOMMITTED (2026-09-15 ~17:50): the mechanism above was wrong in detail. updateSelectedTipUTXODiff resets the tip child to nil, so walks stay ~100 hops. The real bug: updateSelectedTipUTXODiff reads the disqualified tip's parent-relative diff as virtual-relative, so each chunk's tip past loses the chunk's other changes; the next chunk commits that into virtual's UTXO set and the carried diffs grow (DiffFrom 48% of CPU in htnd5 profile p10). Proof: TestResolveVirtualInChunksOverDisqualifiedChain (21-block chain, root disqualified, chunks of 4) - pre-fix virtual set 16/20 mainnet, 32/40 testnet, tip diff 3->11. Fix 1: utxodiffstore UTXODiffChild honors a staged nil child (46a032640 regression vs kaspad) + TestUTXODiffStoreStagedNilChildHidesStoredChild. Fix 2: resolve.go stages disqualified processing point diff vs virtual and re-points previous VSP (utxo_diffs.go stageDisqualifiedProcessingPointAsSelectedTip). Package tests + vet + mutation OK. Full go test ./... gate pending until htnd5 stops resolving. Commit messages: scratchpad/slowpath/commit_store.txt, commit_resolve.txt. needs_human: htnd5 DB already carries wrong UTXO state from old-code chunks; clean resync with a fixed binary is the user's call. The survey adds ~24% CPU on this path.
  - LIVE VERIFICATION (2026-09-15 18:13 htnd5 on fixed binary 2.17.2-c36e4bc5c-dirty, synced from 192.168.1.170 again, survey on): a0a376ed still the only resolve failure (cascade unchanged). Resolve 1%->99% in 2m51s (18:17:18 -> 18:20:09); old binary reached 30% in 41 min (16:38 run) / 21% in 20 min (17:36 run). The ~3m20s before 1% is unchanged in all runs: verifyMultisetSelfConsistency full UTXO scans (not gated by --enable-utxo-debug-diagnostics, expensiveDiagnosticRunsRemaining=3) plus pebble compaction after the import - candidate follow-up. The 4 "is being TOLERATED" warns appear in every run (survey verifyUTXO on cascaded blocks), not new. NEW: 186 coinbase dumps only in the fixed run - survey verdicts moved from offset to strict for 3679 of 11226 blocks compared with the old run (selected parent multiset now matches header), and those strict checks show coinbase fee outputs higher than the block paid (e.g. +836000 and +44000 sompi on outputs 6/7): this node accepts transactions (fees) the miner did not. Real divergence data for the a0a376ed root cause.
- fix_plan: needs_human (consensus policy). Options: (1) operational, no code change: sync from a peer that serves the offset set (as every htnd5 IBD did), e.g. --connect to such a peer; (2) treat a commitment failure on the first chain block above a header-matching imported pruning point as the network offset and tolerate it (consensus-relevant, coordinated release); (3) keep strict, but stop the disqualification cascade from costing a growing DiffFrom per block (performance-only, node still does not sync). Measuring c7bb663dd chunk growth to confirm the slowdown pattern.
- tests: n/a
- commit: none
- COMMITTED: 6de7c3fc0 (utxodiffstore staged nil child) + d1e08b890 (disqualified chain resolve) (committed 2026-09-15 ~19:35 on master at the user's choice without the full go test ./... gate - not enough free memory; targeted package tests + vet passed; full gate still owed). Live: htnd5 resync 18:13 resolved 1->99% in 2m51s vs 30% in 41 min before.

## HTN-190
- title: Integration TestUTXOIndex never receives a UTXOsChanged notification and hangs until the package timeout
- status: fixed
- severity: high
- area: rpc/notifications
- evidence:
  - `go test -count=1 -run '^TestUTXOIndex$' -timeout 150s ./testing/integration/` times out at testing/integration/utxo_index_test.go:83 (the unbounded `<-onUTXOsChangedChan` receive) on the working tree, on 1425b20bb and on c7bb663dd, so it predates this session's commits (2026-09-15).
  - Instrumented copy (15s timeout per receive): "only 0 of 100 notifications arrived" after mining 100 blocks to the registered mining address.
  - The test is guarded by `ci.SkipLongTest` (disabled on CI by 17b63c80d), so only a non-ci `go test ./...` hits it, where it holds the package for the whole -timeout (25m in the previous run).
  - Path read, no fault found yet: consensus sendVirtualChangedEvent -> rpc Manager consensusEventsHandler -> notifyVirtualChange (needs Config.UTXOIndex and a non-nil VirtualUTXODiff) -> UTXOIndex.Update -> NotificationManager.NotifyUTXOsChanged (skips notifications that are empty for the listener's addresses, kaspad behavior since 053bb351b).
  - ROOT CAUSE 2026-09-15 (instrumented scratch copy, scratchpad/utxoidx): every hop works up to the listener filter - 101 virtual changes reach RPCManager.notifyVirtualChange (utxoindex on, diff non-nil), UTXOIndex.Update adds 1 UTXO per block, 1 listener registered - but convertUTXOChangesToUTXOsChangedNotification keeps 0 of them. The map keys differ in encoding: the added entry key is utxoindex.ScriptPublicKeyString(spk.String()) = raw bytes version(LE u16)+script (hex 0000|2079be667ef9...ac); the key registered by rpccontext ConvertAddressStringsToUTXOsChangedNotificationAddresses (utxos_by_addresses.go:187-189) is the ASCII hex text of the script without the version ("2079be667ef9...ac"). Every other user of the key (utxoindex store, filter lookups, unsubscribe delete, broadcast-mode address extraction) uses raw spk.String(). The address is right, so address-filtered UTXOsChanged notifications are never delivered to any RPC subscriber, not only in the test.
- repro: command above
- fix_plan: register listener keys with utxoindex.ScriptPublicKeyString(scriptPublicKey.String()) (the encoding every reader uses); add an rpccontext test that registers an address and expects convertUTXOChangesToUTXOsChangedNotification to deliver an added UTXO for its script; re-enable TestUTXOIndex in the non-ci gate once it passes.
- tests: new app/rpc/rpccontext TestUTXOsChangedNotificationReachesRegisteredAddress (0 of 1 added/removed delivered before the fix, passes after); integration TestUTXOIndex passes in 0.51s (hung before); full go test ./... 107 ok with and without -tags=ci, nothing skipped
- commit: dea0a8cba (registration keys by scriptPublicKey.String(); mismatch introduced by 8c25c1e43)

## HTN-192
- title: Closing an RPCClient while it is reconnecting panics in the receive loop and exits the process
- status: fixed
- severity: medium
- area: rpcclient
- evidence:
  - infrastructure/network/rpcclient/rpcclient.go handleClientDisconnected: after a disconnect it calls c.Reconnect() and does `panic(err)` on any error. Reconnect loops with a 10s retry delay and returns errors.Errorf("Stopped reconnecting to %s because the client was closed") once Close() has set isClosed. So a client closed while its node is unreachable - the normal shutdown path of htnwallet's daemon, htnminer or htnctl during a node outage - panics at the next loop check.
  - The handler runs on the grpcclient receive or send loop (grpcclient.go AttachRouter -> handleError -> onErrorHandler/onDisconnectedHandler), started with spawn = panics.GoroutineWrapperFunc(log), whose recovered panic ends in os.Exit(1) (util/panics/panics.go exit). The process exits with status 1 instead of shutting down.
  - Not a duplicate: HTN-1xx reconnect entry (dcd59f8fa) fixed the leaked ClientConn, not this panic; not filed elsewhere (grep of ISSUES.md and AGENT_STATE.md, 2026-09-15).
- repro: infrastructure/network/rpcclient/close_while_reconnecting_test.go TestCloseWhileReconnectingDoesNotExitTheProcess (child process). Before the fix the child exited with status 1: "[WRN] RPCC: Received error from client: rpc error: code = Unavailable desc = error reading from server: EOF", "Attempting to reconnect", then "[CRT] RPCC: Exiting: Fatal error in goroutine `GRPCClient.AttachRouter-receiveLoop 2`: Stopped reconnecting to 127.0.0.1:... because the client was closed" with stack Reconnect <- handleClientDisconnected (rpcclient.go:216) <- handleClientError <- connect.func2 <- grpcclient handleError <- AttachRouter receive loop <- panics.handleSpawnedFunction. Note: a first version of the test used fixed sleeps and passed before the fix (connect can block up to grpcclient defaultStreamSetupTimeout=30s); it now waits on isReconnecting.
  - Completeness check 2026-09-15: the other panics on this path - handleClientDisconnected on a c.disconnect() error, and grpcclient.handleError on a Disconnect() error after router.ErrRouteClosed (the route Close() closes) - both depend on GRPCClient.Disconnect = stream.CloseSend returning an error. grpc v1.83.1 clientStream.CloseSend (stream.go:1054) always returns nil ("Always return nil"; "We don't return an error here"), so those panics are unreachable today; only the Reconnect-after-Close panic is reachable, and that is the one fixed.
- fix_plan: in handleClientDisconnected, return quietly when Reconnect fails because the client was closed (isClosed set) instead of panicking; keep panicking on other failures, as today. Make the 10s retry delay a package variable with the same default so a test can shorten it.
- tests: TestCloseWhileReconnectingDoesNotExitTheProcess fails before the fix (child exit status 1) and passes after (0.24s); infrastructure/network/rpcclient/... ok plain, -race and -tags=ci; CI staticcheck subset clean; full go test ./... 107 ok with and without -tags=ci
- commit: 0db87cc88 (handleClientDisconnected returns when Reconnect fails because the client was closed; reconnectRetryDelay package var, 10s default)

## HTN-193
- title: DomainSubnetworkID.String hex-encodes into one package-level buffer, so concurrent calls can return another transaction's subnetwork ID
- status: fixed
- severity: medium
- area: consensus-model/rpc
- evidence:
  - domain/consensus/model/externalapi/subnetworkid.go: `var DomainSubnetworkIDBuf [256]byte` and `func (id DomainSubnetworkID) String() string { return fastHex(DomainSubnetworkIDBuf[:], id[:]) }`, where fastHex does hex.Encode(dst, src) then string(dst[:n]). The encode and the copy are not atomic: a concurrent String call can overwrite the shared buffer in between, so the copy returns the other ID's hex (and it is a data race). Introduced by 8c25c1e43 (2026-02-21, "Optimize some uses of hex.EncodeToString to do less GC churn"), the same commit that caused HTN-190.
  - Reachable concurrently: app/appmessage/domainconverters.go:365 `transaction.SubnetworkID.String()` in DomainTransactionToRPCTransaction, used by RPC handlers (GetBlock with transactions, mempool entry calls) on per-client router goroutines and by block-added notifications on the consensus events handler; plus implicit %s formatting of subnetwork IDs in logs and errors. A coinbase and a native transaction converted at once can swap subnetwork IDs in RPC output.
  - Not consensus: no map key or comparison uses the string (grep 2026-09-15); serialization and hashing use the bytes.
  - cmd/htnwallet/parse.go has the same fastHex+global sigBuf pattern, but only in the sequential CLI parse loop - left alone.
- repro: domain/consensus/model/externalapi/subnetworkid_concurrent_string_test.go TestDomainSubnetworkIDStringIsSafeConcurrently (8 goroutines x 20000 String calls on two IDs; plain and -race)
- fix_plan: encode into a per-call [2*DomainSubnetworkIDSize]byte array in String (one allocation for the string, as before) and remove the shared DomainSubnetworkIDBuf and the package fastHex if nothing else uses them.
- tests: TestDomainSubnetworkIDStringIsSafeConcurrently failed before the fix (returned the other ID's hex; -race: 4 DATA RACE at subnetworkid.go:14/15/20) and passes after, 3x plain and 3x -race; externalapi + appmessage ok plain and -race; CI staticcheck subset clean; full go test ./... 107 ok with and without -tags=ci
- commit: c36e4bc5c (per-call buffer in String; shared DomainSubnetworkIDBuf and fastHex removed)

## HTN-194
- title: Shutdown closes the database while the RPC consensus events handler is still writing to the UTXO index, so the node panics and exits with status 1 during shutdown
- status: FIXED, committed 646f779c8 (2026-09-15). Status line was stale (still said "open") - the fix
  and its test (app/rpc/consensus_events_handler_test.go) were already in the tree; corrected 2026-09-18.
  The full go test ./... gate it was "owed" at commit time (skipped then for low memory) has since run
  repeatedly on this exact code as part of HTN-208/HTN-207/HTN-196's gates today, all green - considered
  paid.
- severity: high
- area: app/shutdown, utxoindex
- evidence:
  - htnd5 2026-09-15 (build 79a613da2-dirty): SIGINT at 13:11:14; the running IBD finished resolving virtual at 13:11:44, then "htnd shutdown complete", "Gracefully shutting down the database...", and 5ms later "[CRT] RPCS: Exiting: Fatal error in goroutine `consensusEventsHandler 3`: pebble: closed". Stack: rpc Manager.initConsensusEventsHandler -> notifyVirtualChange -> notifyUTXOsChanged -> utxoindex UTXOIndex.Update -> utxoIndexStore.commit -> storedUTXOAmount -> pebble DBTransaction.Get on the closed DB.
  - app/component_manager.go Stop closes the consensus events channel (line 97) but does not wait for the RPC manager's consensusEventsHandler goroutine to drain its buffered events (channel capacity 100e3) and exit; app.go then closes the database (line ~124). Any virtual changes queued at shutdown - an IBD resolve always produces many - race the close. The panic runs on a spawned goroutine, so panics.HandlePanic calls os.Exit(1) in the middle of the database close.
  - The next start (13:21) came up with an empty consensus (HTN-195); whether this crash contributed is not established.
- repro: stop a node while a large resolve's virtual-change events are still queued (e.g. SIGINT during IBD resolve with --utxoindex)
- fix_plan: have the RPC manager's events handler signal when it has exited, and make shutdown close the events channel and wait for that signal before closing the database (with a bounded wait and a log line if it expires).
- tests: not run
- commit: uncommitted
- COMMITTED: 646f779c8 (committed 2026-09-15 ~19:35 on master at the user's choice without the full go test ./... gate - not enough free memory; targeted package tests + vet passed; full gate still owed)

## HTN-195
- title: htnd5 restarted at 2026-09-15 13:21 on an empty consensus (active prefix back to 0) although its synced data loaded normally at 12:47, forcing a full headers-proof resync
- status: needs_human (confirm launch command; likely --reset-db, not a code bug)
- severity: critical (the miners' submit node was out of service while it resynced; the user reports the network went down)
- area: domain/prefixmanager, startup
- evidence:
  - 12:47:53 start (79a613da2-dirty) loaded the synced consensus: UTXO index reset processed thousands of virtual UTXOs; normal IBD "Found highest known syncer chain block 86387033..." from 192.168.1.170; pruning point moved 17895f2f -> db54fd5d at 13:05, past blocks deleted 13:09:53, pruning point UTXO set update finished 13:10:11.
  - 13:21:38 start (0db87cc88) loaded a consensus at genesis: "Pruning point UTXO set update is required ... Deletion of past blocks below c3003a48" (c3003a48 is the mainnet genesis, domain/dagconfig/genesis.go:53), "UTIN: Processed 1000 virtual UTXOs" only, then "Found highest known syncer chain block <nil>" and "Starting IBD with headers proof". No "Deleting database prefix" line at this startup or at staging init.
  - The staging commit at 13:52:37 logged "Deleting database prefix &{0}" - CommitStagingConsensus logs the OLD active prefix - so the active prefix at 13:21 was 0, while the Sep 14 10:12 commit (also "&{0}") had left it at 1. Between 12:47 and 13:21 the active-prefix record went from 1 to 0 or went missing (domain.New silently makes prefix 0 active when the record is missing). The synced data under prefix 1 is gone after the 13:21 staging consensus was built on prefix 1.
  - Earlier empty restarts (Sep 13 19:34, Sep 13 20:50, Sep 14 09:57) each followed a headers-proof commit made by builds older than 7e620e5c0, whose CommitStagingConsensus could leave the node on the old consensus instance after marking its prefix inactive - that bug was fixed 2026-09-14. Today's is the only one after that fix.
  - Ruled out so far: prefix and UTXO-index deletions delete full keys (pebble DBCursor.Key trims the bucket path only to rebuild bucket.Key(suffix), whose Bytes() re-prepends it), non-empty bucket paths always end in "/" so utxo-index/, utxo-index-counts/, consensus prefixes 0x00/ and 0x01/ and the root active-prefix/inactive-prefix keys cannot overlap, and nothing opens a cursor over an empty bucket; no startup migration ran today (no "Starting migration" in htnd5's log, so domain.migrate did not flip prefixes); pebble bucket cursors are upper-bounded (BytesPrefix), initStagingConsensus refuses when an inactive prefix exists, htnd5 runs with WAL enabled (no HTND_PEBBLE_DISABLE_WAL/HTND_TEST_MODE). The run before 13:21 ended in HTN-194's crash during the database close.
- repro: not yet. The synced scratch node /mnt/data/.htnd-repro-head (IBD from 104.237.1.174 finished 13:37) shut down cleanly at 13:41 ("Shutdown complete", exit 0), so restarting it only repeats the clean-shutdown case that loaded fine at 07:39. Planned once htnd5 has resolved (to avoid CPU/disk contention with its recovery sync): copy that datadir, restart the copy with --utxoindex on scratch ports, let it catch up so resolve queues virtual-change events, SIGINT while they are queued to reproduce HTN-194's crash on a pre-fix binary (0db87cc88), restart, and check whether the consensus comes back at genesis (startup "Deletion of past blocks below c3003a48" / "UTIN: Processed 1000 virtual UTXOs").
  - LIKELY CAUSE 2026-09-15 17:23: htnd5 was restarted by the user at 16:23 and again at 17:20:47 with a command line that ends in `--reset-db` (/proc/<pid>/cmdline of pid 679449). app.go removeDatabase just os.RemoveAll()s the datadir without logging, so a start with this flag is indistinguishable in the log from the "empty consensus" restarts: the 17:20 start shows the same "Pruning point UTXO set update is required ... Deletion of past blocks below c3003a48 (genesis)" and a full headers-proof IBD. The 13:21 command line was truncated in my capture, so --reset-db there is not proven but is the simplest explanation; no code path found that drops the active-prefix record. Action: remove --reset-db from the launch command; code follow-up: log the removal at startup.
- fix_plan: pending root cause. Candidate mitigation regardless of cause: refuse to start (instead of silently creating an empty prefix-0 consensus) when prefix data exists but the active-prefix record is missing, and cross-check DeleteInactivePrefix against the active prefix before deleting.
- tests: n/a
- commit: none

## HTN-196
- title: Fresh headers-proof sync gets stuck forever: the imported pruning point is not on the headers tip's selected chain, missingBlockBodyHashes returns no bodies, and IBD "finishes successfully" in a loop
- status: FIXED (the specific virtual-genesis discriminator only) 2026-09-18, commit b687caf80 - see
  entry below for what shipped, what was deliberately left narrower than the original fix_plan, and
  what is still open
- severity: critical (the recovered htnd5 node, the miners' submit node, cannot sync)
- area: syncmanager/ibd
- evidence:
  - htnd5 restarted 2026-09-15 15:17 by the user on a fresh datadir (/mnt/data/.htnd5-fresh, build 0db87cc88, --connect 104.237.1.174:42421). IBD came from inbound peer 178.121.114.34 (userAgent htnd:2.17.2-ace480940); 104.237.1.174 never appears in the log and the node has no outbound connection.
  - Headers-proof IBD: headers downloaded 15:29, UTXO set received 15:38 (21,733,772 UTXOs), imported pruning point 8df9bedf9e17275c7487b6598fa2494d095b70faa5f12994cc69a697a19a04c0 "does not match its own header" (offset baseline), staging consensus committed 15:41:47.
  - Since then 26+ IBD rounds with the same peer, each: "Found highest known syncer chain block 8df9bedf..." -> headers -> "SYNC: missingBlockBodyHashes: pruning point 8df9bedf is not on <relay/tip>'s selected parent chain and the two chains only meet at virtual genesis ... skipping body sync for this segment" (54 times) -> "Found 0 missing block bodies" -> "IBD ... finished successfully" -> repeat. No "Start of virtual", no resolve.
  - RPC GetBlockDagInfo 15:5x: blockCount 4, headerCount 248329, virtualParentHashes [8df9bedf], pruningPointHash 8df9bedf, tipHashes [fe17c2e8..., d99cff03..., 8df9bedf..., 8df9bedf...] - the pruning point appears TWICE in the tips list. isSynced false.
  - The empty-result branch is the deliberate fallback from 6424bc68e (2026-08-29) and e0862d6c3 (2026-09-13): on "only meet at virtual genesis" it returns [] with nil error so IBD completes, expecting block relay to track the tip. With no bodies below the headers tip, relay can never add blocks, so the node loops instead of retrying or picking another peer.
  - htnd5's previous datadir never logged this warning (Sep 12 - Sep 15, builds 2.16.0 through 0db87cc88), and the same binary synced bodies normally at 13:21 from 192.168.1.170 (pruning point db54fd5d). So it is specific to this pruning point / peer data, not a general failure of the build.
  - Connected peers are all inbound and run mixed builds: 178.121.114.34 ace480940, 104.234.167.214 b53ae5e0b, 219.88.72.130 9d54ad3b1-dirty, 91.66.117.194 34b2a9c3d; htnd5 runs 0db87cc88 with this session's consensus commits. A GHOSTDAG divergence between rule sets is not ruled out.
- repro: current state of the running recovered node
  - Mitigation 2026-09-15 15:58:12: `htnctl Ban 178.121.114.34` on the running node (BanByIP disconnects that IP's connections and bans it). A reconnect from it was refused at 15:58:30 ("is banned. Disconnecting..."), but GetConnectedPeerInfo still listed its two earlier connections 30s later, while its last IBD round (started 15:57:05) was in progress. By then 104.237.1.174:42421 was connected outbound, along with 192.168.1.170 and others inbound. Undo with `htnctl Unban 178.121.114.34`.
  - Ban result 16:17: the ban held (178.121.114.34 reconnects refused every ~30s), but no IBD of any kind started with any other peer in 19 minutes, although 104.237.1.174:42421 (outbound, htnd:2.17.0-3100a74b6), 192.168.1.170 (ace480940-dirty) and five more peers were connected and none was IBD peer. State unchanged: blockCount 4, headerCount 248404, virtual parent and pruning point 8df9bedf, isSynced false. A node that committed a pruning point off its headers' selected chain does not recover in place; recovery needs a clean resync whose headers-proof IBD can only come from a peer with a consistent pruning point (--connect without --listen, so listening is disabled).
- fix_plan: pending. Operational: get IBD from a different peer (ban 178.121.114.34 on the running node via RPC, or a fresh sync that cannot take inbound peers). Code: the empty-result fallback should not report success when no body can ever be fetched - fail the IBD so the node disconnects that syncer and tries another, and investigate the duplicate pruning point entry in the tips store.
- tests: n/a
- commit: none
- FIXED, narrower than fix_plan's "the empty-result fallback" phrasing (2026-09-18): missingBlockBodyHashes
  has FIVE give-up branches that return an empty result with a nil error (IsInSelectedParentChainOf DB
  error, findLowHashInHighHashSelectedParentChain DB error, virtual-genesis-only shared ancestor,
  SelectedChildIterator construction error, and "no header-only block found but lowHash != highHash").
  The function's own top comment says this leniency "must be hit even on a completely fresh sync, so it
  must not fail IBD" - a blanket change to all five would risk breaking that documented normal case, and
  I have no evidence the other four ever loop the way the fifth does. Only the branch the log evidence
  actually shows looping (pruning point and highHash's chain share nothing but virtual genesis) was
  changed: domain/consensus/model/externalapi/errors.go gained a sentinel
  ErrPruningPointDataDoesNotReconcile (same pattern as ErrVirtualHasNoUsableTip); that one branch in
  domain/consensus/processes/syncmanager/antipast.go now returns it instead of an empty success.
  app/protocol/flows/v8/blockrelay/ibd.go's syncMissingBlockBodies catches it with errors.Is and wraps
  it protocolerrors.Wrapf(false, err, ...) - ShouldBan=false because this is a network-wide baseline-
  consistency condition (the same one HTN-002/HTN-208 are about), not evidence this specific peer
  misbehaved. Without that wrap the error would reach protocol.go's handleError unrecognised and PANIC
  the connection goroutine (handleError panics on anything that isn't a ProtocolError or one of five
  named sentinels) - confirmed by reading handleError, not assumed.
- tests: new domain/consensus/processes/syncmanager/missing_block_body_hashes_test.go
  (TestMissingBlockBodyHashesFailsWhenChainsOnlyShareVirtualGenesis), package-internal with fake
  PruningStore/DAGTopologyManager/GHOSTDAGDataStore reproducing the exact shape from the log (pruning
  point whose selected-parent walk reaches virtual genesis before rejoining highHash's chain). Verified
  to fail (nil error, "old infinite-loop behavior") against the pre-fix antipast.go and pass after.
  domain/consensus/... and app/... full suites green, go test -tags=ci ./... 108 ok, rest of tree 48 ok,
  cmd/htnwallet -p 1 ok, testing/integration full non-ci run (all long tests) green. staticcheck + gofmt
  clean repo-wide.
- left alone / still open: the other four give-up branches in missingBlockBodyHashes (not confirmed to
  loop, and the function's own comment warns a blanket change risks a real fresh-sync case); the
  duplicate pruning-point-in-tips-list observation from the evidence (GetBlockDagInfo showing 8df9bedf
  twice in tipHashes) - not investigated, may be a separate bug; whether a currently-stuck node (like the
  htnd5-fresh datadir in the evidence) needs anything beyond restarting IBD against a peer once this fix
  is deployed - the fix only changes what happens on the NEXT IBD attempt that hits this branch, it does
  not retroactively unstick a node sitting on a bad commit from before the fix existed.

## HTN-197
- title: Header IBD rejects every header with "blockHash is nil" while retrying an unfinished pruning point UTXO set update
- status: needs_info
- reported: 2026-09-15 by the user, log from a node not on this machine (15:00:40, peer 188.241.30.226, header 93b889aff6248663...). Not in any htnd5/.htnd5-fresh/repro log; htnd5 was resolving virtual at 15:00.
- build: stack line numbers match 993c95892 (2026-09-12), not HEAD. Since then pruningmanager.go got f56f96423 (resume with recorded method), ee22f57e9, 0765d2186, ba6d7f062; none changes the walk.
- mechanism (code read): validateAndInsertBlock calls UpdatePruningPointIfRequired after every insert, header-only included; when HadStartedUpdatingPruningPointUTXOSet is set it runs updatePruningPoint. The acceptance-data diff fails first (a selected-chain block between the previous and current pruning point has no acceptance data), then the diff-chain-walk fallback (calculateDiffBetweenPreviousAndCurrentPruningPoints) takes UTXODiffChild, which in HTND returns nil,nil for "no child" (kaspad returns not-found), and passes that nil to ghostdagDataStore.Get -> "blockHash is nil". The error propagates, so the header is rejected, and the next insert retries the same thing. Still present at HEAD (pruningPointUTXOSetDiff fallback path).
- needs: which node and version; the log lines just before (the "Calculating pruning points diff failed <reason>. Falling back" line names the acceptance-data failure); whether it repeats on every header and whether IBD ever proceeds.
- fix_plan: first establish why the flag is set while the chain data between pruning points is missing (header-only blocks?). Only then decide between deferring the update until the data exists and erroring; a nil check alone only changes the message.
- 2026-09-18 re-audit (code reading only, no new log - two of the three "needs" items above are now
  answered from the code, the third still needs the user):
  - CONFIRMED still present at HEAD: utxodiffstore.UTXODiffChild (utxo_diff_store.go:104-131) returns
    (nil, nil) for "no child recorded" (both the staged-nil-child case at line 109 and the
    database.ErrNotFound case at line 118-120) - this is deliberate HTND behavior, not a bug in that
    function itself (its own comment explains the staged-nil semantics). ghostdagdatastore.Get
    (ghostdag_data_store.go:57-59) does NOT panic on a nil hash - it returns a clean
    errors.New("blockHash is nil"). So the mechanism is a diff-child walk (calculateDiffBetween
    PreviousAndCurrentPruningPoints, pruningmanager.go:978/991) hitting a legitimate "no child yet"
    and mistaking it for "walk cannot continue", not a crash risk - it fails cleanly but wrongly.
  - ANSWERED "does it repeat on every header": YES, by construction. UpdatePruningPointIfRequired
    (pruningmanager.go:1821) runs after every insert (header-only included per the original mechanism
    note) while HadStartedUpdatingPruningPointUTXOSet is set, and that flag is only cleared inside
    updatePruningPoint() on success (FinishUpdatingPruningPointUTXOSet) - an error leaves it set, so
    the identical failing walk retries on every subsequent insert with no backoff.
  - ANSWERED "does IBD ever proceed": NO, once triggered this is permanent for that sync attempt -
    same reasoning: the flag never clears, so every header hits the same error and gets rejected.
  - NEW theory, not yet confirmed: the immediately-preceding acceptance-data fallback failure ("a
    selected-chain block between the previous and current pruning point has no acceptance data") is
    itself the signature of a header-only block in that range - acceptance data only exists once a
    block is UTXO-resolved. That is the same shape HTN-196 fixed (header-only blocks whose bodies
    never arrive because the syncer chain and this node's pruning point don't reconcile). Possible
    that HTN-196's fix reduces how often this state is reached, since it stops IBD from silently
    "succeeding" while stuck on such a gap - not verified, would need a fresh occurrence to check.
  - Deliberately NOT fixed here: the original fix_plan's caution stands - "a nil check alone only
    changes the message" - and this touches pruning-point UTXO diff computation, which every other
    pruning/UTXO-diff issue this session (HTN-002/004/005) treats as consensus-adjacent and
    measure-first territory. Severity is arguably higher than "needs_info" suggested (this is a
    permanent-stall class like HTN-196, not a transient one), but the right fix - defer vs. surface a
    clearer error vs. something else - still needs either a live repro or the user's call. Still
    needs_human on which node/version reported it and whether it's worth reproducing deliberately now
    that HTN-196 is fixed.

## HTN-198
- title: ResolveVirtual moves virtual onto a lighter pending chain when DAGKnight orders its tip ahead of a UTXO-valid selected parent
- status: fixed in tree, uncommitted (2026-09-15)
- reported: 2026-09-15 by the user ("Pending tip 4e4ae019... does not overcome previous selected parent 2c558cb0... Processing entire unverified chain from pending tip"; that hash is not in any local log). Same warning on htnd5 at startup resolves 2026-08-31, 09-02, 09-03, 09-04 (after "Set block version to 9"), and in htnd2/htnd4/htnd6 logs; on 09-04 the lighter tip was then omitted from virtual parents for breaking the bounded merge set.
- mechanism: from block version 6 findNextPendingTip orders tips by DAGKnight OrderDAG (rank by k-colouring votes, tie-break by lexicographic hash), but ResolveVirtual needs a processing point that wins the previous virtual selected parent by blue work (isNewSelectedTip -> ChooseSelectedParent). A lighter pending tip can come first in DAGKnight order. When no block of its unverified chain wins, 3673b7125 (2026-08-18, replacing kaspad's error) processes the whole chain from the pending tip and makes it virtual's only parent, moving virtual off the heavier valid chain.
- fix (user's rule): at processingPointIndex == 0, if the previous virtual selected parent is StatusUTXOValid, keep it and return (nil, true, nil) like the existing early return; the lighter chain stays pending. A non-valid (e.g. disqualified) previous selected parent keeps the old fallback.
- test: TestResolveVirtualKeepsValidSelectedParentOverLighterPendingTip (resolve_lighter_pending_tip_test.go) - version 6, 5-block valid chain with a commitment-corrupted (disqualified) child, 3-block lighter side chain pending, retries coinbase data until OrderDAG puts the lighter tip first (attempt 3), chunks of 2. Pre-fix virtual selected parent moved to the lighter tip; post-fix stays. Package tests + vet OK.
- left alone / needs_human: DAGKnight tip order and blue-work selection still disagree; which one should pick virtual's chain from version 6 is a protocol question. The chain-shorter-than-a-chunk path (processingPoint = pendingTip without the overcome check) is untouched.
- commit message: scratchpad/lighter/commit_msg.txt
- COMMITTED: f1a75eb75 (committed 2026-09-15 ~19:35 on master at the user's choice without the full go test ./... gate - not enough free memory; targeted package tests + vet passed; full gate still owed)

## HTN-199
- title: A slow address-index RPC blocks the same client's GetBlockTemplate/SubmitBlock, stopping mining
- status: fixed, committed 320a23887 (2026-09-15 ~20:45) with app/rpc/... tests, -race and vet; full go test ./... gate still owed
- reported: 2026-09-15 by the user ("relaying headers is kind of stopping mining"). htnd5 accepted ~300-450 submitted blocks per 30 s until 20:16, then 1 per 30 s until 20:28. The header relay to 84.50.246.239 was coincidental.
- evidence: RPCSTATS 127.0.0.1 GetBlockTemplate 7523/min (20:16) -> 2/min (20:17..20:28), exactly 1 GetBalancesByAddresses per minute throughout. Goroutine 21804434 in HandleGetBalancesByAddresses -> GetVirtualUTXOEntries in dumps at 20:26:12, 20:29:15, 20:29:57 (one call running 3.5+ min). CPU profile 20:29: 46% in getBalanceByAddress -> FilterUTXOPairsAgainstVirtual.
- mechanism: app/rpc handleIncomingMessages handles one request at a time per client connection. Since 6073ca360 (2026-09-12) the address-index RPCs (GetBalanceByAddress, GetBalancesByAddresses, GetUTXOsByAddresses, GetPaginatedUTXOsByAddresses, GetUsableAddresses) check every coin against virtual's UTXO set in 1024-outpoint chunks, each taking the consensus lock with up to 2 s wait (375607fff). For a pool address with a very large number of coins that runs for minutes, and the pool's mining requests on the same connection wait behind it.
- fix plan: handle those address-index commands on a separate per-connection worker so other requests keep flowing; keep the virtual filter (6073ca360 intent). Test: TestAddressIndexRequestDoesNotHoldUpOtherRequestsFromTheSameClient.

## HTN-200
- title: The block builder derives the header and coinbase from virtual, so a node rejects blocks it built itself
- status: fixed, committed bbcd32eb9 (2026-09-18), full go test ./... gate passed with and without -tags=ci plus testing/integration
- reported: 2026-09-17/18 by the user, three coinbase mismatch dumps from mainnet nodes. 09-17 10:44 (creator 2.17.2-ace480940): 6 outputs, values differ on one merged block by +836000 miner / +44000 dev = 880000 sompi of fees. 09-17 same second: 2 outputs, same 880000. 09-18 04:12:59 block 00ba4275 (creator 2.17.2-6e598567d-dirty, i.e. this tree): "Output count differs: actual=2, expected=4", merge set {0186ae63, bf0adbab}, both with zero fees, the built coinbase crediting only 0186ae63.
- evidence: added logMergeSetFeeBreakdown (a8ddd9d17). On the 880000 case it printed one transaction, 88 inputs, 88 entries, recorded fee 880000, fee implied by the input UTXO entries 880000 - the two agree, so the validator's number was right and the creator credited zero. The user confirmed the shape: a compound transaction spending 88 UTXOs, ~10000 sompi per input (the mass minimum for 88 sigops at defaultMassPerSigOp=1000 and 1 sompi/gram is ~88000).
- mechanism: every commitment in a header describes the block it is the header of - the coinbase owes an output to each block in that block's merge set, the accepted-ID merkle root covers what that block accepts, the UTXO commitment is that block's past, the DAA score/blue score/blue work are that block's GHOSTDAG data. The validator derives all of it from the block (resolve_block_status.go:312 -> verifyUTXO). buildBlock derived all of it from model.VirtualBlockHash. The two diverge through ExpectedCoinbaseTransactionInternal's blockHash argument: calcMergedBlockReward pays a merge set block nothing unless it is in the DAA added blocks set of the block being built for, and a zero reward produces NO OUTPUT at all; the acceptance data separately decides which merge set block a fee is credited to.
- why the earlier checks pass first: verify_and_build_utxo.go:97-110 runs utxo-commitment then accepted-id-merkle-root then coinbase. Adding or moving a merge set block whose only transaction is a non-selected-parent coinbase changes neither the accepted transaction set nor the UTXO set, so both pass and only the coinbase disagrees. This is why the rejection always looked like "the fee arithmetic is wrong" when it never was.
- fix: buildBlock picks the parents first (still virtual's, via BuildParents, whose level 0 is the direct parents it is given), stages the prospective block under a temporary hash with its own relations + GHOSTDAG + DAA data, and derives the coinbase, accepted-ID merkle root, UTXO commitment, DAA score, blue score, blue work and required difficulty from that block. acceptanceData and multiset are passed into buildHeader rather than read back so the three cannot come from different replays. Same shape test_block_builder.go has always used.
- verified equivalence: instrumented buildBlock printed virtual vs prospective for daaScore, blueScore, blueWork and selectedParent across a whole test chain - identical every time. Within one node the parents are always virtual's, so the fix is a no-op in the normal case; its value is removing the class, including any staleness in virtual's stored GHOSTDAG/DAA data, which this node is prone to during chunked resolve.
- tests: TestBuiltCoinbasePaysTheFeesOfTheMergeSet and TestBuiltCoinbasePaysEveryMergedBlock (blockbuilder/coinbase_fees_test.go, committed 6e598567d and bbcd32eb9). The first funds a transaction, spends it with a fee in a block virtual merges, and requires this node's validator to accept the block this node built; the second uses a 2-sibling merge set and requires one reward + one dev fee output per merged block (asserted only from block version 2; version 1 buckets rewards). Both pass at HEAD; the first also passes at 320a23887, so it is a regression guard, not a reproduction.
- left alone: ruled out on the way - the template cache in miningmanager.GetBlockTemplate is entirely commented out so every template is freshly built and ModifyBlockTemplate is dead; checkCoinbaseBlueScore (block_body_in_isolation.go:93) already pins the coinbase payload's blue score to the header's, so a coinbase from another template cannot be the explanation; TransactionAcceptanceData.Fee survives Clone, the LRU cache and the proto round trip (dbobjects.proto:80).
- related: 4d31ea86d's "fall back to the recorded fee when the input entries are incomplete" is unreachable - validation always recomputes acceptance data, where Fee is set from transaction.LoadFee() right after ValidateTransactionInContextAndPopulateFee stored exactly totalIn-totalOut, and entries are only populated for accepted transactions. It is dead code and should be deleted or justified by a real path. 1d380ebe8 gates the integer dev-fee split on the merging block's version in ExpectedCoinbaseTransactionInternal but on the blue block's version in coinbaseOutputForBlueBlockV2 - a v10 block merging v9 blues rounds differently in the two functions; worth resolving before v10 activates.

## HTN-201
- title: A fake block hash staged for one DAG is served to another, because store LRU caches are filled on read
- status: mitigated in the block builder (bbcd32eb9); the general hazard is open
- reported: 2026-09-18, found while fixing HTN-200. The first version of that fix named the prospective block with a counter and TestCheckLockTimeVerifyConditionedByAbsoluteTime and ...WithWrongLockTime failed with ErrTimeTooOld, the block's timestamp 12 s below the past median time the validator computed for the same block.
- mechanism: blockwindowheapslicestore.Get calls bss.cache.Add(blockHash, windowSize, heapSlice) when it reads a value out of the staging shard - a plain read, before any commit. So anything computed for a hash inside a staging area that is later discarded still lives in the process-wide LRU keyed by that hash. blockBuilder's counter-derived temporary hashes (1, 2, 3, ...) collide with testBlockBuilder.nextTempBlockHash's counters over the same stores, so a window heap computed for one parent set was served to a block with different parents.
- mitigation: prospectiveBlockHash() hashes "prospective-block" plus the parent hashes, so the name means "these parents" and every cached entry under it stays true for any later build that reaches the same name.
- open: testBlockBuilder still uses bare counters, which is safe only because its own sequence is consistent within one consensus. Any future code that stages under a synthetic hash has the same trap. Worth deciding whether Get should populate the cache at all from staged-but-uncommitted data.

## HTN-202
- title: RepairBlockStatuses runs on every boot and the node does not listen for RPC until it finishes
- status: fixed, committed ce46aa667 (2026-09-18)
- reported: 2026-09-18 by the user - a node on 192.168.1.170 running the latest code "does not answer to htn-stratum-bridge".
- mechanism: e93758dde enabled c.RepairBlockStatuses() unconditionally inside factory.NewConsensus, which runs inside domain.New at component_manager.go:157, while the RPC manager is only built at component_manager.go:232. The walk clears the block status cache, reads every block's status from the database and commits each changed one in its own transaction while holding the consensus lock, so on a mature or archival node the node looks dead to any miner or bridge for as long as it runs. Log signature: "Starting block status repair..." then "Processed N blocks, repaired M so far..." every 1000 blocks.
- fix: hidden flag --repair-block-statuses (infrastructure/config/config.go -> consensus.Config.RepairBlockStatuses -> factory.go), default off, following --enable-utxo-debug-diagnostics. Its error is no longer discarded: a repair that fails halfway now fails startup instead of continuing quietly.
- left alone / needs_human: the repair itself is still a blunt instrument - it sets every block that is neither StatusInvalid nor StatusHeaderOnly to StatusUTXOValid, including StatusDisqualifiedFromChain and StatusUTXOPendingVerification, and writes only the status, never a UTXO diff. resolveBlockStatus short-circuits on StatusUTXOValid, so those blocks never get a diff and the diff-child walk can then hit the HTN-197 "blockHash is nil" family. e93758dde's own message calls it "not final version of the solution". Recovery from disqualify-all-blocks mode now needs the flag passed explicitly once.

## HTN-203
- title: maybeAcceptTransaction never assigns its accumulatedMassAfter return, so merge set mass accumulation resets on every accepted transaction
- status: open, low (inert rather than wrong)
- reported: 2026-09-18, found while reading calculate_past_utxo.go for HTN-200.
- mechanism: the named return accumulatedMassAfter (calculate_past_utxo.go:356) is never assigned in the function body. The early returns pass accumulatedMassBefore explicitly, but the accept path at line 464 returns the zero value, so applyMergeSetBlocks' accumulatedMass goes back to 0 after every accepted transaction.
- impact: nothing else reads accumulatedMass today - it is only threaded in and out - so no rule depends on it. It is not doing what it reads as doing, and any future per-merge-set mass limit built on it would be silently broken.
- fix_plan: either assign it (accumulatedMassAfter = accumulatedMassBefore + the transaction's mass) or delete the parameter and return, depending on whether a merge set mass limit is wanted. Deciding that is a consensus question, so do not guess.

## HTN-204
- title: A block whose selected parent was pruned never gets its trusted DAA window, so a freshly synced node mines at genesis difficulty
- status: REOPENED 2026-09-18 - the recorded fix is NOT safe and is not in the tree. Do not re-apply it as written.
- REFUTATION of the recorded fix (2026-09-18, this is why it is absent from master): re-applying it exactly as described below makes testing/integration TestIBDWithPruning fail reproducibly - 3 of 3 runs fail with it, 2 of 2 pass without it, on an otherwise identical tree. The failure is on the SERVING side: "Non-critical peer protocol error from HandlePruningPointAndItsAnticoneRequests: database entry not found ... DAA window <hash> does not exist in db", then "Timeout waiting for IBD to finish".
- why it breaks: calculateBlockWindowHeap has two callers with different needs. One is difficulty, which is what this issue is about. The other is handle_pruning_point_and_its_anticone_requests.go:83 -> Consensus().BlockDAAWindowHashes -> consensus.go:1590 -> dagTraversalManager.DAABlockWindow -> calculateBlockWindowHeap. With the fix, a serving node's window for the pruning point stops being empty and becomes the full trusted DAA window, so the serving loop then asks TrustedDataDataDAAHeader (consensus.go:1593) for each of those blocks. That call answers from ghostdagDataStores[0], or failing that from blocksWithTrustedDataDAAWindowStore keyed by (trustedBlockHash, daaBlockWindowIndex) - and for those blocks the serving node has neither, so it returns not-found and the peer's IBD dies. The old empty window was, accidentally, what kept the serving path consistent.
- what a real fix needs: separate the two uses. The difficulty path wants the trusted window; the trusted-data serving path must only enumerate blocks it can actually serve headers and GHOSTDAG data for. Changing calculateBlockWindowHeap for both at once cannot work. This is a protocol-visible decision (it changes what a node serves to syncing peers), so it is needs_human.
- status_of_evidence: the original mechanism below is still believed correct - a freshly synced node does compute genesis difficulty because the pruning point's window is empty. What is wrong is the prescribed fix, not the diagnosis.
- the attempted patch and its reproduction test are kept out of the tree at scratchpad/htn204_window.go.attempt and scratchpad/htn204_trusted_window_test.go.keep. The test (TestBlockWindowOfBlockWhoseSelectedParentWasPruned, plus a BlocksWithTrustedDataDAAWindowStore() accessor on testapi.TestConsensus) is a true reproduction: 0 blocks in the window before the change, 10 after.
- ORIGINAL RECORD FOLLOWS (diagnosis good, fix unsafe)
- status_original: fixed
- severity: high (a fresh node builds templates ~2000x too easy; with HTN-006 unfixed the network accepts those blocks)
- area: consensus/dagtraversalmanager, difficulty
- reported: 2026-09-18, user: "Find out why difficulty is not decreasing or dropping on a node that is mining, it's stuck on 65536.01"
- evidence (live node, HTND --appdir /mnt/data/.htnd5, RPC :42720, mining via hoominer/stratum 127.0.0.1:5555):
  - 65536.01 is not a computed difficulty, it is mainnet's genesis difficulty: GetDifficultyRatio(0x1E7FFFFF) = PowMax/(0x7FFFFF<<216) = 2^39/0x7FFFFF = 65536.0078 -> rounds to 65536.01. The powMax clamp would show 1.00, not 65536.01.
  - GetBlock on the selected tip: "bits": 511705087 = 0x1E7FFFFF = dm.genesisBits exactly. The only non-simnet path returning it is difficultymanager.go:157, `targetsWindow.len() < 2 || targetsWindow.len() < dm.windowSize(blockVersion)`.
  - GetBlockDagInfo: difficulty 65536.01, virtualDaaScore 227416484, pruningPointHash 0eec5f2eab2a17fe91394111d2d20930fa37d54f38c5323394348d3121cbb13b (DAA 227414422). Mainnet version 9 -> DifficultyAdjustmentWindowSize index 8 = 2640.
  - Window length measured through EstimateNetworkHashesPerSecond, whose answer stops changing once windowSize exceeds the window actually available: identical results from 2060 upwards, still changing at 2050. 227416484 - 227414422 = 2062. The window is exactly the blocks above the pruning point and nothing below it.
  - GetBlock on the pruning point: "selectedParentHash": "fefefefe...fe" = model.VirtualGenesisBlockHash, i.e. it was inserted through validateAndInsertBlockWithTrustedData and ghostdagDataWithoutPrunedBlocks replaced its pruned selected parent.
  - The trusted window itself is present: walking parents down from the pruning point, headers exist for 400+ consecutive blocks below it (down to DAA 227412837). Those headers are staged only by validateAndInsertBlockWithTrustedData alongside the DAA window entries.
  - Chain history: real network blocks stop at DAA 227414537 (2026-09-17 04:15:07), every block from DAA 227414538 (2026-09-18 07:28:56) on is locally mined and carries genesis bits.
- mechanism: calculateBlockWindowHeap walks down the selected chain until it finds a block carrying a trusted DAA window, and then takes that window. 8056a76ee (2026-08-15) added `break` on a selected parent equal to model.VirtualGenesisBlockHash, and fbbe4ebaa folded it into the same condition as the genesis check - both *before* the daaWindowStore lookup. The pruning point is precisely the block whose selected parent is the virtual genesis marker and whose window exists only as trusted data, so the walk breaks one statement before the data it came for. The pruning point's window is therefore empty, and because BlockWindowHeapSlice caches it and every child builds from its selected parent's cached slice, every block above the pruning point inherits the truncation: the window holds only the blocks mined since the IBD. Until 2640 of those accumulate, RequiredDifficulty returns genesisBits, so block templates (and virtual's Bits, hence GetBlockDagInfo's difficulty) sit at 65536.01 instead of the real ~1.3e8.
- fix: domain/consensus/processes/dagtraversalmanager/window.go - keep the nil and genesis breaks where they were, move only the VirtualGenesisBlockHash break to after the trusted-window branch, where it still guards the ghostdagDataStore.Get that would otherwise look up the marker. Upstream kaspad has no such break at all and reaches the trusted window; this keeps the guard and restores the ordering.
- tests: new dagtraversalmanager TestBlockWindowOfBlockWhoseSelectedParentWasPruned stages a block in the shape validateAndInsertBlockWithTrustedData leaves the pruning point in (GHOSTDAG selected parent = virtual genesis marker, trusted DAA window of 10 blocks) and asks for its window. Before the fix: "expected the trusted DAA window of 10 blocks, but the window has 0 blocks" on both nets. After: passes. Needed a test-only BlocksWithTrustedDataDAAWindowStore() accessor on testapi.TestConsensus.
- left alone: HTN-006 (header bits are never validated against RequiredDifficulty) is what turns this from a local mining nuisance into a network-level risk, and it still needs a coordinated activation - not touched here. The already-mined genesis-bits blocks on that node keep their bits; the fix only corrects what the node computes from now on, and those easy blocks stay in its own DAA window until they age out.
- tension: the window still has to be *recomputed* for the fix to take effect - windowHeapSliceStore is an in-memory LRU with no DB persistence, so a restart is enough, but a node that keeps running with the truncated windows cached will not heal.

## HTN-205
- title: The reachability reindex root never leaves virtual genesis after a pruning-proof IBD, so every block reindexes the entire reachability tree
- status: fixed, committed 20ba94e7c (2026-09-18). IS the cause of HTN-206 after all - confirmed on the live node 13:11, 0.8 -> 26.8 BPS. The mid-session refutation was wrong (it used tree size as the discriminator instead of interval-space exhaustion).
- severity: high (391 ms of the 701 ms each block spent in ValidateAndInsertBlock; removing it took the live node from 0.8 to 26.8 BPS)
- area: consensus/reachabilitymanager
- reported: 2026-09-18 by the user - "Investigate and fix why node is not accepting 5 BPS blocks. You can run go tool pprof http://localhost:6060/debug/pprof/profile to get profiler information, as reachabilityDataStore commit is taking way too long."
- evidence (live node, HTND 2.17.2-803929b57, --appdir /mnt/data/.htnd5, RPC :42720, mining via hoominer/stratum, pprof :6060):
  - log: "Processed 2 blocks and 0 headers in the last ~1.6s" continuously, one accepted SubmitBlock every ~0.8 s. That is the symptom: ~1.2 blocks/s, not 5.
  - 30 s CPU profile: SubmitBlock -> ValidateAndInsertBlock is 25.94 s of 47.19 s. Of that, CommitAllChanges 16.83 s, and reachabilityDataStagingShard.Commit alone 14.48 s (30.68% of the whole process) - 8.51 s in dbTx.Put (pebble indexed-batch skiplist: cmpbody 9.87%, batchskl.findSplice 10.70%) and 4.24 s in serializeReachabilityData. The other 9.02 s is validateBlock -> ValidateHeaderInContext -> reachabilityManager.AddBlock -> addChild -> reindexIntervals (countSubtrees 4.84 s, propagateInterval 4.11 s).
  - 30 s alloc profile: 10.66 GB allocated, 7.36 GB of it in the reachability staging shard commit. ReachablityDataToDBReachablityData allocated 4,442,879 objects and stageInterval/CloneMutable 4,440,335 - i.e. ~4.44M reachability rows rewritten in 30 s. 31 blocks were accepted in that window (37 in the CPU profile's window), so ~143,000 rows per block - the same order as the whole 168,396-node tree.
  - per-block CPU, over the 37 blocks accepted during the CPU profile: 701 ms in ValidateAndInsertBlock, of which 391 ms is the reachability commit and 243 ms is reindexIntervals. The residual once the reindex is gone is ~67 ms per block, against a 200 ms budget at 5 BPS, and the process was only using ~1.6 of 12 cores - so the headroom is there.
  - /proc/<pid>/io: 1.73 GB written to disk in 20 s (~70 MB per block); 182 GB since the 07:51 start. The datadir held ten 64 MB WALs written within the same minute - one memtable-sized write batch per block.
  - offline read of a hardlink snapshot of the datadir: the stored reachability reindex root is fefefe...fe = model.VirtualGenesisBlockHash, with a subtree of 168,396 nodes, i.e. the entire tree. It has never moved since reachabilityManager.Init staged it.
  - simulating the reindexIntervals walk-up from the current mining tip over that snapshot: every ancestor for 1,408 levels has intervalSize == subtreeSize-1, exactly one short, and the walk finally stops at 11e242bd with intervalSize 239,919 and a subtree of 146,118 nodes - which propagateInterval then rewrites in full. The whole 146K-node tree is packed into interval space [1, ~241K] with no slack anywhere.
  - replaying UpdateReindexRoot against the snapshot returns the root unchanged and stages nothing. FindNextAncestor(headersSelectedTip, virtualGenesis) returns c3003a4836...a783, and ghostdagDataStore.Get for it fails with "block-ghostdag-data/c3003a48... not found" - it has no level-0 GHOSTDAG data, while the other four children of virtual genesis do.
- mechanism: findNextReindexRoot walks from the current reindex root down the chain towards the selected tip, stopping reindexWindow (200) blue score short of it. 53e8662ca ("Fix IBD", 2026-06-21) added `if database.IsNotFoundError(err) { break }` on the chosen child's GHOSTDAG lookup, where upstream kaspad returns the error. After a pruning-proof IBD, virtual genesis's children include blocks that only ever existed above level 0 - all block levels share one reachability data store when the old per-level one is uninitialised (factory.go:236-241) - so the very first hop down from virtual genesis has no level-0 GHOSTDAG data and the loop breaks on its first iteration. newReindexRoot is returned equal to currentReindexRoot, updateReindexRoot reports "no update to root", and the root stays at virtual genesis forever. From then on it is an ancestor of everything, so reindexIntervals never reaches the cheap reindexIntervalsEarlierThanRoot path, and concentrateInterval - the only thing that ever hands interval slack down to the growing chain - never runs. intervalSplitWithExponentialBias then allocates every reindex exactly, so each block leaves every ancestor exactly full again and the next block repeats the full-tree reindex. This degrades from the first block after the IBD; it is not specific to this node's DAG.
- why it is invisible in the profile: updateReindexRoot has zero samples. It short-circuits even earlier on this node, because blockProcessor.updateReachabilityReindexRoot only calls it when the headers selected tip changed, and this node's headers selected tip has been frozen at bf0adbab (a real network block from 2026-09-17 12:17, blue score 215,650,884) since every block it mines loses ChooseSelectedParent against it - the HTN-204 aftermath. The root was already stuck before that happened.
- fix: domain/consensus/processes/reachabilitymanager/tree.go - on a not-found GHOSTDAG lookup for the chosen child, keep descending towards the selected tip (newReindexRoot = chosenChild; continue) instead of breaking, with a guard for chosenChild == selectedTip. This keeps 53e8662ca's intent - never fail block insertion on a proof-only block - while restoring the invariant that the reindex root tracks the selected tip.
- verified on the real data: replaying UpdateReindexRoot over the datadir snapshot with the fix moves the root from virtual genesis (subtree 168,396) to d11b5a7e... with a subtree of 201 nodes - exactly reindexWindow behind the tip - and concentrateInterval gives it an interval of 1.8e19. The one-off catch-up across ~1,400 chain blocks takes well under 2 s, so it fits inside a single block insertion.
- tests: new reachabilitymanager/reindex_root_test.go TestUpdateReindexRootPassesBlocksWithoutGHOSTDAGData, with a GHOSTDAGDataStore mock that returns database.ErrNotFound for blocks it was not given. It builds the post-IBD shape - reindex root = the tree root, first hop below it has no GHOSTDAG data, a 100 block chain below that - and requires the root to end up reindexWindow behind the selected tip. Fails before the fix ("the reindex root is still the tree root ... so it never passed the block without GHOSTDAG data"), passes after. Whole reachabilitymanager package green (48 s).
- left alone: (a) blockProcessor.updateReachabilityReindexRoot still short-circuits when the headers selected tip has not changed - that is kaspad's and is sound once the root is correct, but it means a node already in this state only heals when it next receives a better header. (b) factory.go giving every block level the same reachability data store when the old per-level store is uninitialised is NOT an HTND deviation - it is upstream kaspad 58d627e05 "Unite reachability stores" (#1963, 2022), and in that design every level's reachability manager is built with ghostdagDataStores[0], so reachabilityManager.AddBlock always reads level-0 GHOSTDAG data. That means c3003a48 must have HAD level-0 GHOSTDAG data when it entered the tree and lost it since, or entered through a path that bypasses AddBlock. Worth its own issue: how a block stays in the united reachability tree after its level-0 GHOSTDAG data is gone. Not investigated here - it does not change this fix, because either way the reindex root must not stop at such a block (and upstream's answer, returning the error, would just fail block insertion, which is what 53e8662ca was patching around). (c) HTN-204 (the trusted DAA window) is what froze this node's headers selected tip; unrelated code path.
- tension: the fix stops the degeneration but does not unpack an already-degenerate tree by itself. The tree only regains slack when concentrateInterval runs, which needs the reindex root to advance, which on htnd5 needs one headers-selected-tip change. A node that is stuck mining a losing branch will stay slow until it receives a better header.

## HTN-206
- title: A mining node accepts only ~1.08 blocks/s via SubmitBlock, where a node on 2.16.0 kept up with the network's 5 BPS
- status: RESOLVED on the live node 2026-09-18 13:11 by the HTN-205 fix (20ba94e7c). Not a commit - a state collapse caused by HTN-205.
- CONFIRMATION (live, htnd5, running 2.17.2-20ba94e7c): after the 12:47 restart the node resumed pulling
  real headers, which moved the headers selected tip and so called UpdateReindexRoot for the first time
  in days. With the fix the walk descended past c3003a48 instead of stopping, concentrateInterval ran,
  and the node went 48 blocks/min (0.8 BPS) at 13:10 -> 335 (5.6 BPS) at 13:11 -> 1610 (26.8 BPS) at 13:12.
  Read out of the datadir afterwards: the reindex root is now dc5e8360d0ed190613dc80e8871b8f0862dc3499f20926c1c9db9b5666fd0e4c
  with a subtree of 201 nodes, against fefefe...fe with 181,055 before - the exact hash and subtree size
  the offline replay predicted before the fix was committed. Each block now reindexes ~201 nodes instead
  of ~146,000. Part of the 26.8 BPS is backlog catch-up, so steady state is the number to judge.
- severity: high (this is the user's actual report)
- area: unknown; consensus/blockprocessor is where the time goes
- reported: 2026-09-18 by the user - "There has happened some regression after 2.17.2 tag.. But this ain't working at 5BPS avg and able to push to 100 BPS. like on 2.17.2 tag."
- measured:
  - htnd5 (mining, RPCS "Accepted block ... via submit"): a flat ~650 blocks per 10 min = 1.08 BPS from 07:20 to 12:00 on 2026-09-18, across builds 803929b57 and 9acde1d20-dirty. No decay, no step - flat the whole time.
  - htnd4 (/mnt/data/.htnd4, idle since 2026-09-07, datadir intact): PROT "Accepted block ... from node <peer>", ~2,900-3,000 per 10 min = 4.8 BPS sustained for hours on 2026-09-05, with a 3,394-blocks-in-one-minute burst = 56 BPS. Running Version 2.16.0-8a5a012b8 / 2.16.0-0f308b388. Zero blocks via submit.
  - so the reference is 2.16.0 and the relay path, not 2.17.2 and the submit path. 203 commits separate 0f308b388 from HEAD. There is no v2.17.2 git tag; the last tag is v2.17.0 and 2.17.2 is the version constant, so ~100 commits all report 2.17.2-<commit>.
- ruled out (HTN-205 is not the cause):
  - the CPU profile of htnd5 both before and after the restart puts 27.9-30.7% of the whole process in reachabilityDataStagingShard.Commit and ~17-19% in reindexIntervals, so the full-tree reindex is what htnd5 is spending its time on.
  - but htnd4, which sustained 4.8 BPS, has the SAME reindex root stuck at fefefe...fe (virtual genesis) and a subtree of 513,427 nodes against htnd5's 181,055. A stuck reindex root with a 3x larger tree did not stop it keeping up. So HTN-205 is a real defect and a real cost but it is not the discriminator.
  - interval slack is not the discriminator either: sampling both chains, htnd4's tip interval is size 2 and grows ~2.6 units per level, htnd5's tip interval is size 39,584 and grows ~1.06 per level. htnd4 is tighter, not looser.
- THE DECISIVE MEASUREMENT (found after the refutation above, and it reverses it): htnd5's own rotated log htnd.log.20.gz has the collapse on tape, per 10 min, via submit:
    2026-09-15 23:00  3097  5.2 BPS      2026-09-16 00:10  1937  3.2 BPS
    2026-09-15 23:30  3031  5.1 BPS      2026-09-16 00:20  1578  2.6 BPS
    2026-09-16 00:00  2904  4.8 BPS      2026-09-16 00:30   864  1.4 BPS
                                         2026-09-16 01:00   682  1.1 BPS
  and it has been ~1.0 BPS ever since, for three days. The binary did NOT change across that
  transition: Version 2.17.2-320a23887 started 2026-09-15 22:18:25 and was still the running
  build at 2026-09-16 07:09:04. No restart, no version change, no config change - a 5x throughput
  collapse in about 30 minutes on a fixed binary. So "regression after 2.17.2" is not a code
  regression at all, and bisecting commits would never have found it.
- corrected mechanism: HTN-205 is the cause after all, and the earlier refutation was wrong because
  it used tree SIZE as the discriminator. The real discriminator is whether the chain has exhausted
  the interval space it inherited. With the reindex root frozen at virtual genesis, concentrateInterval
  never runs, so no fresh interval space is ever handed down to the growing chain. The chain spends
  the finite space its ancestors already had - htnd5's is 181,055 nodes inside about 84,000 units of
  interval along the chain, i.e. oversubscribed - and once it is spent, every ancestor sits at
  intervalSize == subtreeSize-1 and each new block reindexes the whole subtree. That is a one-way
  transition: it is progressive as the walk climbs higher each block, which is why the collapse takes
  ~30 minutes rather than happening in one block, and why it never recovers.
- why htnd4 looked like a counterexample: its log ends 2026-09-05 06:29 still at 5.0 BPS, but the
  datadir I sampled was last written 2026-09-07. I compared a fast-period log against an end-of-life
  datadir. Its 513,427-node tree with a tip interval of size 2 is an early stage of the same collapse,
  not a refutation - a new child there still stops the walk at depth 0.
- open questions / next steps:
  - the two nodes are on different entry paths (RPC SubmitBlock vs P2P relay) and different code (2.16.0 vs HEAD). Neither has been held fixed while the other varies. That is the experiment that is missing.
  - the cheap version: run HEAD and 0f308b388 against the same copied datadir, feeding blocks the same way, and compare per-block time. htnd4's datadir is intact and idle, which makes that possible without touching the user's live node.
  - needs_human: whether "2.17.2" in the report means a specific build the user ran, and on which datadir - htnd4's logs say the 5 BPS reference was 2.16.0. Asked.

## HTN-207
- title: A large address-balance query scans virtual's UTXO set through the shared LRU and evicts everything block processing had warmed
- status: FIXED 2026-09-18, commit 499758822 (see fixed_commit below)
- severity: medium (52% of the node's CPU while it runs, and it runs under the consensus lock)
- area: rpc/rpccontext + consensus/consensusstatestore
- reported: 2026-09-18, found in the CPU profile taken right after HTN-205/HTN-206 were fixed - with the reachability reindex gone, this became the top cost on htnd5.
- evidence (live node, 25 s profile, 32.31 s of samples):
  - HandleGetBalancesByAddresses -> getBalanceByAddress -> FilterUTXOPairsAgainstVirtual -> GetVirtualUTXOEntries = 52.46% of the process.
  - consensusStateStore.utxoByOutpointFromStagedVirtualUTXODiff = 44.97%, and inside it the single line dbContext.Get(key) is 11.55 s while the virtualUTXOSetCache.Get hit path is 610 ms. So the cache is answering almost nothing on this path.
  - the cache is one shared utxolrucache of 50,000 entries, created once in factory.go:211 as consensusstatestore.New(prefixBucket, 50_000, preallocateCaches), and it is the same cache block validation reads virtual's UTXO set through.
- mechanism: FilterUTXOPairsAgainstVirtual asks consensus for every outpoint the UTXO index lists for an address, and consensus.virtualUTXOEntriesNoLock loops them one at a time through consensusStateStore.UTXOByOutpoint. Each miss does a pebble point lookup and then calls virtualUTXOSetCache.Add. For an address with far more coins than 50,000, that is a scan through a shared LRU: nearly every lookup misses, and each one evicts an entry that block processing put there. The query gets no benefit from the cache on its next run either, because it evicted its own early entries before it finished. Two costs, not one - the query is slow, and it leaves the cache cold for the block path, while holding the consensus lock (with the 2 s tryLockFor cap from 375607ff).
- fix_plan (not yet applied): give the bulk virtual-UTXO lookup a path that reads through the cache but does not populate it on a miss, and use it from virtualUTXOEntriesNoLock only. Block validation keeps populating as it does now. That is the smallest change that stops a scan from evicting the working set; it does not make the query itself faster.
- alternatives considered: a scan-resistant cache (2Q/SLRU) would fix the class rather than this one caller, but it is a much larger change to a structure block validation depends on. Raising the cache size does not help - the scan is unbounded in the address's coin count, not bounded by any size that would fit.
- left alone: the per-outpoint serial pebble Get itself. Sorting the outpoints into key order before the loop would turn random point lookups into a near-sequential scan and should help materially, but that is an optimisation I have not measured, and it changes the order entries are produced in, so it needs the index permutation carried through. Worth doing after the eviction fix, with a measurement.
- related: HTN-199 already stopped these queries blocking the same client's mining requests; this is about what they cost the node, not about head-of-line blocking.
- FIXED exactly as fix_plan proposed: domain/consensus/datastructures/consensusstatestore/utxo.go
  gained UTXOByOutpointWithoutPopulatingCache (delegates to the existing lookup helper with a new
  populateCacheOnMiss bool, now false on this path and true on the existing UTXOByOutpoint), added to
  the ConsensusStateStore interface. consensus.go's virtualUTXOEntriesNoLock (the only caller of
  GetVirtualUTXOEntries's per-outpoint loop, i.e. the bulk/RPC path) now calls the new method instead
  of UTXOByOutpoint. Block validation's own lookup (consensusstatemanager.virtualUTXOEntry, used by
  the offset-toleration arithmetic check) is untouched and keeps populating, as fix_plan specified.
- tests: new
  domain/consensus/datastructures/consensusstatestore/utxo_by_outpoint_without_populating_cache_test.go
  (TestUTXOByOutpointWithoutPopulatingCacheDoesNotEvictTheWorkingSet) - warms a 2-entry cache with two
  outpoints as block validation would, then runs four more misses through the new method and asserts
  the cache length is unchanged AND the two warmed entries are still individually present. Verified to
  fail (both warmed entries reported evicted) when temporarily made to populate on every miss, and to
  pass against the real fix. domain/consensus/... full suite green (plain and -tags=ci), staticcheck
  and gofmt clean repo-wide, full go test -tags=ci ./... 108 ok, plain ./... (minus htnwallet, cross-
  checked separately) 48+ ok, testing/integration full non-ci run (all long tests included) green.
- left alone: exactly what fix_plan called out - the per-outpoint serial pebble Get, and sorting
  outpoints into key order for a more sequential scan. Not measured or done here.
- fixed_commit: 499758822
- HTN-207 note 2026-09-18: this session moved onto 192.168.1.170 itself mid-fix (see AGENT_STATE.md);
  the live production node (htnd-public, in Docker on this host) was not touched by anything above -
  no build, restart, or datadir access. This is a pure code/test change, verified offline.

## HTN-208
- title: A block above an imported pruning point is disqualified for a UTXO commitment mismatch the toleration cannot recognise, and one such block strands the whole node
- status: FIXED 2026-09-18, commit f1abbcb16 (options 1+2 per user decision; see the entry near the
  end of this section for what shipped, tests, and what was left alone)
- severity: critical (it is the entry point to "all tips disqualified / virtual cannot leave VirtualGenesis" - a node that finishes a 140k-block IBD and is then unusable)
- area: consensus/consensusstatemanager (verify_and_build_utxo, import_pruning_utxo_set)
- reported: 2026-09-18 by the user. The pasted log is from 192.168.1.170:42520, which was syncing FROM
  the local node htnd5 and could not finish. htnd5 had hit the SAME block from the other direction
  hours earlier, while syncing FROM 192.168.1.170. Log:
    12:17:29.328 [INF] PROT: Start resolving virtual
    12:17:29.328 [INF] BDAG: Start of virtual DAAScore 227414423
    12:17:29.476 [WRN] BDAG: UTXO verification for block 19506f5f... failed: UTXO commitment is invalid -
      block header indicates 98b53db9..., but calculated value is 630c4d96...: ErrBadUTXOCommitment
- THE DECISIVE DATAPOINT - the same block is valid on a full chain and invalid above an imported
  pruning point, on the same node:
    2026-09-17 04:15:08 [INF] PROT: Accepted block 19506f5f... from node 178.121.114.34:52634
      with 1 tx (dynamic K: 1) Status Valid
    2026-09-18 06:26:46 [WRN] BDAG: UTXO verification for block 19506f5f... failed:
      ... UTXO commitment is invalid ... ErrBadUTXOCommitment
    2026-09-18 06:44:11 [WRN] BDAG: (same block, same failure, after the second import)
    2026-09-18 12:17:29 [WRN] BDAG: (same block, same failure, now on 192.168.1.170)
  htnd5 validated that block successfully from the live network on 09-17, and then rejected the very
  same block on 09-18 after importing pruning point 0eec5f2e... from 192.168.1.170. So the block is
  not bad. What changed is the UTXO baseline underneath it.
- and it is reciprocal, so it is not one bad peer: htnd5 imported from 192.168.1.170 (06:16, 06:31
  "Downloading the pruning point proof from ... 192.168.1.170:42421") and failed on this block; then
  192.168.1.170 imported from htnd5 and failed on the same block at 12:17. Both directions, same
  boundary block. Re-requesting the set from the other peer is therefore not a fix - each node hands
  the other a set that reproduces the fault.
- both imports reported success: "Imported pruning point 0eec5f2e... UTXO set matches its own header
  commitment c3fbf1a9..." at 06:20:56 and 06:35:57. The multiset hash at the pruning point is right and
  the set still produces the wrong answer one block later.
- what makes it critical, not cosmetic: 227414423 is the pruning point's DAA score plus one. The block
  that fails is the FIRST block above the imported pruning point. Disqualifying it disqualifies
  everything built on it, so every tip ends up disqualified, ResolveVirtual has nowhere to move virtual
  to, and the node is stranded at the virtual genesis marker having just spent a full IBD. That is the
  HTN-206/ErrVirtualHasNoUsableTip symptom, and this is where it starts.
- evidence that the toleration exists but does not fire here: the same ErrBadUTXOCommitment reaches two
  different outcomes in the same log.
    TOLERATED: "Block 3293a464...: utxo-commitment check failed and is being TOLERATED (... (inherited
      pruning-point offset) ...)" - 09-17 08:02, 10:40, 11:12, 09-18 06:30, 06:49
    NOT tolerated: "UTXO verification for block ... failed" - 09-17 06:05, 06:09, 06:13, 06:17, 06:18,
      07:58, 09:13, 10:35, 09-18 06:26, 06:44
  9 distinct blocks, 12 occurrences. In the same log, 10 x "ResolveVirtual finished with no
  UTXO-valid/pending tip".
- mechanism: verifyUTXO tolerates a commitment mismatch when blockInheritsKnownUTXOCommitmentOffset
  (verify_and_build_utxo.go:408) says the block merely carries an offset inherited from an incomplete
  imported pruning point UTXO set. That predicate has two signals:
    1. pruningPointBaselineIsOffset - the pruning point's own stored multiset disagrees with its own
       header commitment. This is the network-wide signal and it covers any block.
    2. failing that, the block's SELECTED PARENT's stored multiset disagrees with the parent's header.
  Neither can fire for the boundary block. Signal 2 cannot, because the boundary block's selected parent
  IS the pruning point, whose multiset was imported and checked against that same header at import time.
  Signal 1 cannot, because verifyAndRepairImportedPruningPointUTXOSet (import_pruning_utxo_set.go:222)
  only reports matchesHeader=true when the accumulated multiset equals the header commitment - so a
  pruning point that passed import is, by construction, not an "offset baseline".
- the proof that this is the state the node is in: GetInfo on htnd5 right now returns
  "IsUtxoSetVerified": true. The baseline is reported verified - signal 1 is false - while blocks above
  that same pruning point compute a different UTXO commitment than their headers carry. So the imported
  set hashes correctly AT the pruning point and produces a different answer ONE block later. The
  toleration was designed for a baseline that is visibly offset; this is a baseline that is invisibly
  offset, and it is precisely the case the predicate cannot see.
- why an offset can be invisible: the import path verifies the set by its multiset hash alone. A set
  that hashes to the right value at the pruning point can still be wrong for the child if the child's
  merge set spends or creates outpoints the imported snapshot got wrong in a way that only shows up
  when diffs are applied. The import check answers "does this set hash to what the header says", not
  "is this set the set the network had".
- fix options, in the order I would try them:
  1. TOLERATE THE BOUNDARY (small, matches what the user asked for): treat the first block whose
     selected parent is the pruning point as inheriting the offset by definition when its own
     commitment check fails - it has nothing else to inherit from. Cheap, self-scoping the same way the
     existing toleration is, and it converts "node stranded after a full IBD" into "node runs with a
     known-suspect baseline, loudly". Must reuse logToleratedIssue so it is visible once, and must NOT
     flip IsUtxoSetVerified to true - the whole point is that the operator can see it.
  2. MAKE THE BASELINE SIGNAL HONEST (the real fix): if the first block above the pruning point
     disagrees, then the baseline WAS offset regardless of what its own hash said. Record that - set
     the same offset state signal 1 reads - so every later block is covered by signal 1 and the node
     stops reporting IsUtxoSetVerified: true when it demonstrably is not. This fixes the reporting bug
     as well as the stranding.
  3. REFUSE THE PEER'S SET instead (strict): treat a boundary mismatch as ErrBadPruningPointUTXOSet and
     re-request from another peer. RULED OUT by the evidence above - the two nodes reproduce the fault
     on each other's sets, so there is no better peer to fall back to and this would just loop.
- what I would NOT do: widen the toleration to any commitment mismatch. The offset toleration is
  deliberately self-scoping so that a node on a clean pruning point still enforces strictly, and that
  property is worth keeping - it is the only thing separating "permissive after a bad import" from
  "never validates UTXO commitments at all".
- needs_human on which of 1/2/3 to take: all three change what a node will accept after an IBD, and 3
  changes peer behaviour. 1+2 together are my recommendation - tolerate so the node is usable, and stop
  claiming the UTXO set is verified when a block one above the pruning point says otherwise.
- next evidence to pull: the survey at /mnt/data/.htnd5/utxo-survey.jsonl (7.8 GB,
  --enable-utxo-debug-diagnostics is on) should have records for 19506f5f... naming WHICH outpoints
  differ. Given the datapoint above - block Valid on 09-17, invalid on 09-18 - the question is no longer
  "did a peer send a bad set" but "which outpoints does the imported snapshot get wrong, and are they
  ones the pruning point's own multiset is blind to". See docs/utxo-survey.md. Diffing the 09-17 and
  09-18 acceptance data for that block would answer it directly.
- related: HTN-206 (the stranding symptom, now recovered from by 07c95219d's RepairDisqualifiedTipChains
  - but that resets statuses, it does not fix the UTXO set, so a boundary block that genuinely mismatches
  will be re-disqualified on the retry). HTN-207 is unrelated. The supply figures in GetInfo
  (703.0e15 circulating vs 640.1e15 reference from 2026-08-01) are consistent with ~6.5 weeks of
  emission and are NOT evidence of corruption here.
- user decision 2026-09-18: implement fix options 1+2 together (recommended combination above).
- FIXED 2026-09-18, domain/consensus/processes/consensusstatemanager/verify_and_build_utxo.go +
  consensus_state_manager.go + domain/consensus/model/externalapi/utxo_set_health.go:
  1. blockInheritsKnownUTXOCommitmentOffset gained a third signal: blockHash's selected parent IS the
     current pruning point (new currentPruningPoint helper). Signal 2 reads the pruning point's own
     multiset against its own header - the exact check verifyAndRepairImportedPruningPointUTXOSet
     already ran and passed at import time - so it was structurally blind to an offset that only shows
     up one block later. The boundary block has nothing else to inherit an offset from, so a commitment
     mismatch there is now tolerated the same way. Self-scoping like the other two signals: it only
     matches the one block whose selected parent is the current pruning point.
  2. validateUTXOCommitment's tolerated-mismatch branch now calls confirmBaselineOffsetIfBoundaryBlock,
     which records (in a new boundaryOffsetConfirmedPruningPoint field, keyed by pruning point hash,
     process-memory only like the existing baselineOffsetPruningPoint/baselineOffset fields) that this
     pruning point's baseline is offset even though its own stored multiset hashes correctly against
     its own header. UTXOSetHealth now ORs that recorded state into `verified` instead of only
     re-hashing the pruning point's own multiset (which would keep saying "verified" forever), so
     GetInfo's IsUtxoSetVerified stops claiming a health the chain has demonstrably disproven, and
     pruningPointBaselineIsOffset (signal 1) covers every later block once this has fired once.
  Both changes are self-scoping the same way the pre-existing toleration is: they clear themselves
  the moment the pruning point advances to a clean one, and neither widens what gets tolerated beyond
  the specific block(s) named above.
- tests: new domain/consensus/processes/consensusstatemanager/boundary_offset_test.go, package-internal
  (constructs a consensusStateManager directly with fake PruningStore/GHOSTDAGDataStore/MultisetStore/
  BlockHeaderStore, following the dropped_input_error_test.go pattern) so it can stage the exact
  post-import shape without a real IBD: TestBlockInheritsKnownUTXOCommitmentOffsetCoversTheBoundaryBlock
  (signal 3 fires for the boundary block, not for an unrelated healthy block - self-scoping check),
  TestConfirmBaselineOffsetMakesUTXOSetHealthHonest (UTXOSetHealth/BaselineVerified flips to false after
  the boundary block fails, even though the pruning point's own stored multiset still equals its header
  commitment - the property option 2 exists for), TestConfirmBaselineOffsetIsScopedToTheBoundaryBlock
  (calling the recorder for a non-boundary block is a no-op). All three fail to compile against the
  pre-fix tree (confirmBaselineOffsetIfBoundaryBlock did not exist) and pass after. Full package suite
  green (go test and go test -tags=ci), staticcheck clean, gofmt clean.
- left alone: this does not touch import_pruning_utxo_set.go itself, and does not change what an
  imported set is accepted on - only what a demonstrated-offset baseline is allowed to tolerate one
  block later and how honestly that state is reported. Option 3 (refuse the peer's set) stays ruled out
  per the evidence above. The already-imported set on a currently-stranded node still needs the fix
  deployed and the boundary block reprocessed (e.g. via a resync or the existing
  RepairDisqualifiedTipChains retry path) to take effect - this does not retroactively repair a node
  that disqualified itself before the fix was running.
- next: get this onto a currently-stranded node (htnd5 or 192.168.1.170) and confirm virtual leaves
  VirtualGenesis and IsUtxoSetVerified reports false rather than the node silently claiming health it
  does not have.

## HTN-209
- title: TransactionsOrderedByFeeRate.GetByIndex has no bounds check, so mempool eviction can crash
  the node with an unrecovered index-out-of-range panic
- status: FIXED 2026-09-18, commit pending (see fixed_commit below)
- severity: high (unrecovered panic on the mempool hot path, reachable by ordinary RPC/relay traffic
  under mempool congestion - a DoS surface, no consensus-rule violation needed to trigger it)
- area: miningmanager/mempool
- reported: 2026-09-18, found by a background audit fork of previously-unswept mempool files
  (domain/miningmanager/mempool/transactions_pool.go, remove_transaction.go,
  revalidate_high_priority_transactions.go, validate_and_insert_transaction[_replacement].go,
  validate_transaction.go, mempool_utxo_set.go, mempool.go, fill_inputs_and_get_missing_parents.go,
  model/ordered_transactions_by_fee_rate.go, model/mempool_transaction.go). This session's earlier
  audits had already covered compound_tx_rate_limiter, wallet_freezing_manager,
  blocktemplatebuilder/txselection.go, the block template builder GC-diff commit, and RBF - all
  checked safe; this pass covered the files those hadn't.
- mechanism: model/ordered_transactions_by_fee_rate.go's TransactionsOrderedByFeeRate.slice is meant
  to mirror transactions_pool.go's allTransactions map 1:1, but the two structures are NOT kept
  strictly in sync in production:
    1. addMempoolTransaction (transactions_pool.go:54-77) writes to allTransactions,
       chainedTransactionsByParentID and mempoolUTXOSet BEFORE calling
       transactionsOrderedByFeeRate.Push, with no rollback if Push fails (findTransactionIndex
       refuses a transaction whose Fee or Mass reads as 0). A failed Push leaves the transaction
       permanently in allTransactions but absent from tobf.slice.
    2. removeTransaction (transactions_pool.go:110-120) already documents this happening: it deletes
       from allTransactions unconditionally, and when transactionsOrderedByFeeRate.Remove returns
       ErrTransactionNotFound it logs "This should never happen but sometimes does" and continues,
       rather than treating it as a bug.
  Once tobf.slice is shorter than allTransactions, limitTransactionCount's eviction loop
  (transactions_pool.go, called after every accepted transaction) walks currentIndex up while
  skipping high-priority entries, bounded only against len(tp.allTransactions) - never against
  len(tobf.slice) - and calls transactionsOrderedByFeeRate.GetByIndex(currentIndex), which had no
  bounds check at all (unlike its sibling RemoveAtIndex, which already had one). Once the mempool
  fills past MaximumTransactionCount with enough high-priority entries at low indices - plausible
  since raisePriorityIfCompound marks ordinary relayed compound transactions high-priority by default
  - currentIndex walks past len(tobf.slice)-1 while still under len(tp.allTransactions), and
  GetByIndex indexes out of range: an unrecovered panic, crashing the node.
  The audit did not nail the exact trigger for the FIRST desync (checked whether PopulateMass/fee
  population could leave LoadFee()==0 || LoadMass()==0 reachable at Push time; a prior session note
  already rules zero-fee unreachable via minrelaytxfee, and mass looked deterministic on a quick
  check) - not required for the fix, since the desync is independently conceded by removeTransaction's
  own tolerated-error comment regardless of how it first arises.
- fix: model/ordered_transactions_by_fee_rate.go's GetByIndex now bounds-checks and returns nil for
  an out-of-range index, matching RemoveAtIndex's existing contract. transactions_pool.go's
  limitTransactionCount checks for a nil result and logs a warning + returns instead of dereferencing
  it, treating "ran out of ordered entries" the same as the existing "ran out of allTransactions
  entries" fallback it already had.
- left alone: the root desync itself (unrolled-back Push failure in addMempoolTransaction, and
  removeTransaction's tolerated ErrTransactionNotFound) - fixing why the two structures fall out of
  sync is a larger change to mempool bookkeeping and wasn't the load-bearing fix; this stops the crash
  at the point of consumption, which is where RemoveAtIndex already drew the same line. Worth a
  follow-up if the desync itself turns out to be more than rare/benign.
- tests: new domain/miningmanager/mempool/model/ordered_transactions_by_fee_rate_test.go
  (TestGetByIndexOutOfBoundsReturnsNil) - covers an empty set, negative index, index == len (the
  exact value that used to panic), and index > len, plus confirms in-bounds indices still work.
  Verified to panic ("index out of range [0] with length 0") against the pre-fix GetByIndex and pass
  after. go vet + staticcheck clean, gofmt clean, domain/miningmanager/... full suite green, go test
  -tags=ci ./... 109 ok, rest of tree 49 ok, testing/integration full non-ci run green.
- fixed_commit: 59091739a

## HTN-210
- title: constants.SetBlockVersion is a check-then-act race, not an atomic compare-and-swap, so the
  process-global block-version ratchet can theoretically regress under concurrent calls
- status: FIXED 2026-09-18, commit pending (see fixed_commit below)
- severity: medium (a real correctness bug in code the whole codebase's most-repeated gotcha depends
  on - see CLAUDE.md's "Hoosat-specific gotcha: the block-version global" - but see "not reproduced"
  below: could not demonstrate it firing under stress, so treat the severity as "definitely wrong code
  in a load-bearing place" rather than "observed live failure")
- area: consensus/utils/constants
- reported: 2026-09-18, found by self (not a fork) while auditing app/protocol/flows/v8's
  handle_relay_invs.go, which calls constants.SetBlockVersion(version) once per relayed block, from
  each peer's own connection goroutine - i.e. concurrently across peers by construction.
- mechanism: the old implementation was
    current := atomic.LoadUint32(&blockVersion)
    if uint32(v) > current { ...; atomic.StoreUint32(&blockVersion, uint32(v)) }
  Each individual load and store is memory-safe (no torn reads/writes, so `go test -race` sees nothing
  wrong - this is not a Go memory-model data race), but the read-compare-write as a whole is not
  atomic: two goroutines can both load the same `current` before either stores. If the goroutine
  proposing the LOWER version's store lands after the one proposing the higher version's, the ratchet
  - which GetBlockVersion's every caller trusts to never decrease - visibly regresses until the next
  higher call catches it back up. HTN-001's finality/pruning depths read this "current" value directly
  (by user decision, chain-derived rather than per-block), so a regression in this exact window could
  make one node's own validation briefly use a lower version's parameters than it should, self-
  inconsistently with calls made microseconds apart on the same node.
- NOT REPRODUCED: tried to catch the old code regressing under a start-barrier stress test (up to 2000
  goroutines proposing random values 1-50 concurrently, 20 repeated attempts) and it passed every time
  on this hardware (80 cores) - the window between LoadUint32 and StoreUint32 is apparently too narrow
  to hit reliably even under heavy contention. This is a real, provable defect in the code as written
  (textbook non-atomic check-then-act on a monotonic-max update), not a demonstrated live failure -
  recorded honestly per this session's "prove it or say you couldn't" discipline. Fixed anyway because
  the fix is a strict, zero-cost improvement (same behavior outside the race window, provably correct
  inside it, no new dependency, same style as the atomic primitives already in use) to already-
  concurrency-sensitive code guarding the single most load-bearing global in this codebase - this is
  not "adding validation for a scenario that can't happen" (CLAUDE.md's caution), since concurrent
  calls to SetBlockVersion from different peers' goroutines demonstrably do happen; only the specific
  regression window could not be demonstrated firing.
- fix: domain/consensus/utils/constants/constants.go's SetBlockVersion now loops on
  atomic.CompareAndSwapUint32(&blockVersion, current, uint32(v)), retrying against the freshly-read
  current value if the CAS fails because another goroutine wrote first. A store only ever commits if
  blockVersion is still exactly what was just read, so a version that already advanced past v during
  the loop can never be overwritten by it.
- tests: new domain/consensus/utils/constants/constants_test.go -
  TestSetBlockVersionNeverDecreasesSequentially (the simple single-threaded contract) and
  TestSetBlockVersionIsMonotonicUnderConcurrency (200 goroutines x 200 iterations of random values,
  asserts the final value is the true max proposed) - documented honestly in its own comment as a
  regression guard pinning the invariant going forward, not a reproduction of the old bug (which this
  same test, run repeatedly against the pre-fix code, never caught failing). go vet + staticcheck
  clean, gofmt clean, go test -race ./domain/consensus/utils/constants/... x3 clean,
  domain/consensus/... full suite green, go test -tags=ci ./... 110 ok, rest of tree 49 ok,
  cmd/htnwallet ok, testing/integration full non-ci run green.
- left alone: nothing else reads or writes blockVersion outside GetBlockVersion/SetBlockVersion/
  ForceSetBlockVersion, all three already atomic-based; no other global in the codebase was found to
  share this check-then-act shape during this pass (not an exhaustive sweep for the pattern elsewhere).
- fixed_commit: b190d32aa

## HTN-211
- title: ResolveVirtual's DAGKnight-vs-blue-work overcome check only runs when chunking, so the common
  short-backlog case still swaps virtual onto a lighter pending tip unconditionally
- status: FIXED 2026-09-18, commit pending (see fixed_commit below)
- severity: high (this is very likely the dominant engine behind "reorgs happen so often" - see below)
- area: consensus/consensusstatemanager
- reported: 2026-09-18, found by self while analyzing a user question ("why do reorgs happen so often,
  and why do node disagreements happen on transactions") by tracing HTN-198 (fixed f1a75eb75) past its
  own fix to see whether the gap it left open ("left alone / needs_human: ... The chain-shorter-than-
  a-chunk path ... is untouched") was actually reachable in practice.
- mechanism: resolve.go has two different ways virtual's selected parent gets chosen. The normal live-
  block path (AddBlock -> updateVirtual -> pickVirtualParents -> selectVirtualSelectedParent) is pure
  blue-work GHOSTDAG selection off a DownHeap - fine, not affected. The IBD/catch-up path
  (ResolveVirtual, called at the end of every IBD round from app/protocol/flows/v8/blockrelay/ibd.go's
  syncMissingBlockBodies, i.e. essentially every time a node falls even slightly behind and catches
  back up, not only cold starts) picks its starting "pending tip" via findNextPendingTip, which orders
  tips by DAGKnight's OrderDAG (k-colouring votes, hash tie-break) from block version 6 - a different
  algorithm from blue-work, and one HTN-198 already showed disagrees with it on real mainnet data.
  HTN-198's fix added an "overcome check" (if the DAGKnight-ordered tip never out-blue-works the
  previous UTXO-valid virtual selected parent, keep the previous one instead of swapping) - but that
  check lives entirely inside the `if maxBlocksToResolve != 0 && len(unverifiedBlocks) >
  maxBlocksToResolve` chunking branch (resolve.go, originally lines 294-338). When the unverified
  backlog is short enough to resolve in one pass - the ordinary case once a node is nearly caught up,
  since every routine IBD round ends here regardless of how far behind the node was - `processingPoint`
  was set to the DAGKnight-ordered pending tip and used UNCONDITIONALLY, with zero check against the
  previous selected parent's blue work. Virtual's selected parent would swap to whatever DAGKnight
  currently ranks first on essentially every ordinary catch-up round, independent of accumulated work -
  not a real proof-of-work reorg, an algorithmic disagreement between two different tip-orderings
  flipping the canonical chain back and forth.
- connection to transaction-level disagreement: HTN-004 (open, needs_human) already documents that
  "the diff-child tree itself depends on reorg history and arrival order" and that its UTXO-diff
  tolerances (isTolerableConflict, addEntry's incoming-score-wins restamp) exist because of exactly
  this kind of repeated restamping. Every spurious selected-parent flip this bug caused is a reorg from
  the diff-child bookkeeping's point of view, so this is very plausibly the engine that kept re-
  triggering HTN-004's and HTN-005's already-documented UTXO/transaction drift mechanisms - fixing the
  frequency of spurious reorgs does not fix HTN-004/005 (those tolerances and the drift mechanism are
  untouched), but it removes what was very likely the largest source of exposure to them.
- why this did not need a fresh consensus decision: unlike HTN-002/004/005/006 (open protocol questions
  about which baseline or ordering should govern), the POLICY here was already decided by the user in
  the same commit (f1a75eb75, HTN-198): keep the UTXO-valid previous virtual selected parent unless the
  new tip actually overcomes it by blue work. This fix applies that identical, already-approved rule to
  the code path where the guard was accidentally scoped to chunking only - it is not a new policy
  question, confirmed with the user before implementing.
- fix: domain/consensus/processes/consensusstatemanager/resolve.go - added the same
  isNewSelectedTip(pendingTip, previousVirtualSelectedParent) check unconditionally, right after
  computing unverifiedBlocks and before the chunking branch, mirroring the existing chunked-branch
  logic exactly (same store read, same log line, same early return keeping the previous valid selected
  parent). The existing chunked-branch code is untouched - still runs its own backward search for
  chains long enough to need chunking, which the new up-front check does not replace, only complements.
- tests: domain/consensus/processes/consensusstatemanager/resolve_lighter_pending_tip_test.go -
  parameterized the existing lighterPendingTipScenario helper with maxBlocksToResolve (previously
  hardcoded to 2, forcing the chunked path) and added
  TestResolveVirtualKeepsValidSelectedParentOverLighterPendingTipShortChain, identical scenario but
  with maxBlocksToResolve=0 (unlimited - the short/non-chunked path). Verified to fail against the
  pre-fix resolve.go (virtual's selected parent visibly moved from the heavier valid tip to the lighter
  DAGKnight-preferred one) and pass after. The pre-existing chunked-path test still passes unchanged.
  go vet + staticcheck clean, gofmt clean, domain/consensus/... full suite green (53 ok), go test
  -tags=ci ./... 110 ok, rest of tree 49 ok, cmd/htnwallet ok, testing/integration full non-ci run
  green.
- left alone: the chunked branch's own backward-search logic (untouched, still correct); HTN-198's
  still-open broader question of whether DAGKnight order or blue-work should fundamentally govern
  virtual's chain from version 6 - that remains needs_human, this fix only makes the two paths
  consistent about NOT moving virtual backward off a valid heavier chain, not about resolving which
  ordering is "right" in general; HTN-004/005's own drift mechanisms, unaffected by this fix.
- fixed_commit: 22529337f

## HTN-212
- title: rpcclient's outgoing route and request timeout are read from every request method without
  synchronization, racing Reconnect/connect and SetTimeout
- status: FIXED 2026-09-18, commit pending (see fixed_commit below)
- severity: medium (real, unsynchronized concurrent field access in a client library used by htnctl,
  htnwallet, stability-tests and anything else built on infrastructure/network/rpcclient - a Go data
  race, `go test -race` catches it directly; not consensus/node-process code)
- area: infrastructure/network/rpcclient
- reported: 2026-09-18, found by self while sweeping for other instances of HTN-210's check-then-act/
  unsynchronized-shared-state class. AGENT_STATE.md already carried an old, never-followed-up note
  ("rpcclient: reconnect leaks old grpc.ClientConn (never Close), timeout/GRPCClient fields raced") -
  the leak half was already fixed in a previous session (releaseClient/activeClient, see the doc
  comment on releaseClient); the "fields raced" half was not.
- mechanism: every rpc_*.go request method (41 files) read `c.rpcRouter.outgoingRoute()` and
  `c.timeout` directly. `c.rpcRouter` is a plain `*rpcRouter` field, and `c.timeout` a plain
  `time.Duration` field; both are written by connect() (on every initial connection and every
  Reconnect) and `c.timeout` additionally by the exported `SetTimeout`, entirely outside
  `rpcRouterMutex` for `timeout` and, for `rpcRouter`, under the mutex only on the WRITE side - the 41
  read sites never took it. The package's own `route()` helper already reads `c.rpcRouter` correctly
  (RLock before dereferencing), for exactly this reason - the request methods just never used it for
  the outgoing route, and there was no equivalent guard for `timeout` at all. Any caller issuing RPC
  requests concurrently with a Reconnect (very ordinary usage - the client reconnects automatically
  from its own disconnect/error handlers on a background goroutine while user code keeps calling RPC
  methods) races these two fields.
- fix: rpcclient.go gained `outgoingRoute()` (RLock, mirrors `route()`) and every `c.rpcRouter.
  outgoingRoute()` call site now goes through it; `timeout time.Duration` became `timeoutNanos
  atomic.Int64` with a `getTimeout()` accessor, and `connect()`/`SetTimeout()` write it with
  `atomic.Int64.Store`. All 41 call sites updated mechanically (sed on the exact call pattern -
  caught and fixed one self-inflicted bug from that during development: the sed also rewrote the new
  `outgoingRoute()` helper's own body into a call to itself, an infinite-recursion stack overflow
  caught immediately by the first `go test -race` run, not shipped).
- tests: new infrastructure/network/rpcclient/concurrent_request_during_settimeout_test.go
  (TestConcurrentRequestsDuringSetTimeoutDoNotRace) - concurrent GetInfo and SetTimeout under
  `go test -race`, clean with the fix. Does NOT also hammer Reconnect concurrently with active sends
  in a tight loop to exercise the c.rpcRouter half: doing so surfaced a SECOND, separate, pre-existing
  race inside grpcclient.GRPCClient (below, HTN-213) unrelated to this fix, and kept tripping the test
  for a different reason than what it was testing. The pre-existing
  TestReconnectReleasesPreviousConnection and TestCloseWhileReconnectingDoesNotExitTheProcess tests
  already exercise Reconnect concurrently with this fix in place and pass clean under -race, which is
  the coverage this change relies on for the c.rpcRouter half. go vet + staticcheck clean, gofmt
  clean, full repo build clean, go test -race ./infrastructure/network/rpcclient/... clean (all 7
  tests), go test -tags=ci ./... 110 ok, rest of tree 49 ok, cmd/htnwallet ok, testing/integration
  full non-ci run green.
- left alone: HTN-213 (below) - found in the course of writing this fix's test, not fixed here to
  keep this change scoped to the field-access race it targets.
- fixed_commit: e5f73cbdf

## HTN-213
- title: grpcclient.GRPCClient.Disconnect races an in-flight send on the same gRPC stream
- status: open, not fixed - found incidentally while testing HTN-212, not investigated further
- severity: unknown (real Go data race, `go test -race` catches it reliably under concurrent
  Reconnect + active request traffic; whether it causes anything worse than a race-detector report -
  a panic, a corrupted stream, a hang - was not determined)
- area: infrastructure/network/rpcclient/grpcclient
- reported: 2026-09-18, surfaced by a stress test written for HTN-212 (a goroutine issuing GetInfo
  calls in a tight loop while another goroutine calls Reconnect in a tight loop, under -race) - not a
  deliberate investigation of this code.
- evidence (go test -race, infrastructure/network/rpcclient package): WARNING: DATA RACE - write at
  grpcclient/grpcclient.go:88 (GRPCClient.Disconnect -> the underlying grpc clientStream's CloseSend)
  by the goroutine running Reconnect -> disconnect -> GRPCClient.Disconnect, racing a read at
  grpcclient/grpcclient.go:139 (GRPCClient.send -> the same clientStream's SendMsg) by the spawned
  AttachRouter receive/send-forwarding goroutine - both touch the same underlying grpc-go
  *clientStream object with no synchronization between Disconnect and an in-flight send.
- repro: the test written for HTN-212 before it was scoped down (concurrent tight-loop GetInfo +
  Reconnect) reproduced this reliably; kept as scratch, not committed, since it was testing for a
  different bug and this one needs its own dedicated investigation and reproduction.
- fix_plan: not analyzed. Candidates to look at first: whether GRPCClient already has or needs a
  lock around the clientStream that Disconnect and send both go through, or whether Disconnect should
  wait for in-flight sends to complete first (matching how releaseClient already detaches
  activeClient before closing, so callbacks from a closed connection are ignored rather than raced).
- needs: nothing conceptually - this is a local-correctness/concurrency bug, not a protocol question -
  just needs someone to actually read grpcclient.go's Disconnect/send/receive lifecycle end to end and
  design the fix, which this session did not get to.
- FIXED 2026-09-19, commit 87090d5ad: renamed closeSendMutex to sendMutex and now take it around all
  three call sites that reach into the underlying stream's Send-family methods - send() (AttachRouter's
  send loop), Post (post.go, previously entirely unguarded), and Disconnect's CloseSend (already
  guarded, kept as-is). RecvMsg is intentionally left unguarded: grpc-go's ClientStream doc says
  concurrent SendMsg+RecvMsg is safe, only concurrent SendMsg-family calls (Send/Send,
  CloseSend/Send) are not.
- tests: new TestPostAndDisconnectNeverOverlapOnTheUnderlyingStream (grpcclient package) - a fake
  RPC_MessageStreamClient (overlapDetectingStream) flags any overlap it observes between its own
  Send-family calls, then the test runs 300 concurrent Post/Disconnect iterations against it under
  -race. Verified the repro property directly: reverted the Post lock, ran the test 3/3 times and it
  failed every time with the overlap detected; restored the lock, 3/3 passes. Full
  infrastructure/network/rpcclient/... suite green under -race, gofmt/vet/staticcheck (build_and_test.sh's
  exact check list) clean, full repo build (app/cmd/domain/infrastructure/stability-tests/testing)
  clean.
- left alone: receive()/RecvMsg is not locked (safe concurrently with Send per grpc-go's own
  contract); no change to Disconnect's or send()'s error handling or call sites in rpcclient.go -
  purely adds synchronization, no behavior/API change.
- fixed_commit: 87090d5ad

## HTN-214
- title: domain/utxoindex.utxoIndexStore.UTXOs scanned its cursor twice for every unlimited query -
  once just to count, once to fill - instead of reading the count this store already maintains
- status: FIXED 2026-09-18, commit pending (see fixed_commit below)
- severity: high (this is the concrete, measured explanation for the user's "why is nearly-synced IBD
  so damn slow" question - not the only contributor, see HTN-215 below, but the cleanest, safest, and
  highest-confidence one found)
- area: domain/utxoindex
- reported: 2026-09-18, user pasted a live log showing "nearly synced" block processing running at
  ~2-3 blocks/s with a persistent ~7.5 minute lag between wall-clock and the DAA timestamp embedded
  in each processed block, and asked why. Investigated with a live 20s CPU profile pulled from the
  production node's pprof endpoint (HTND_PROFILER=1, already enabled; read-only, node not touched or
  restarted) at http://127.0.0.1:6060/debug/pprof/profile - matches the profiling approach used in
  this session's earlier HTN-205/206 investigation.
- evidence: the profile showed runtime.gcBgMarkWorker/gcDrain (GC background marking) consuming
  52.88% of ALL CPU sampled over the 20s window (341% average core utilization, so the node was busy,
  just spending more than half of that on garbage collection). Two concurrent, independent, allocation-
  heavy call paths were driving it: (1) block processing's per-block virtual-parent selection (see
  HTN-215) at ~18% of total CPU, and (2) domain/utxoindex.(*UTXOIndex).UTXOs / utxoIndexStore.UTXOs at
  15.25% of total CPU cumulative, reached from app/rpc/rpchandlers.HandleGetBalancesByAddresses/
  HandleGetUsableAddresses (8.39%/8.03% respectively) - both running concurrently with IBD on this
  node, competing for the same CPU and GC budget as block processing.
- mechanism: utxoIndexStore.UTXOs(scriptPublicKey, limit, buffer), when limit==0 ("give me
  everything," which HandleGetBalancesByAddresses and HandleGetUsableAddresses both use), ran a first
  full cursor scan over the script's entire bucket just to count entries (so the result buffer could
  be pre-sized exactly), then ran a SECOND full cursor scan, from the start again, to actually
  deserialize and collect them - go tool pprof -list showed the counting pass alone cost 2.31s of the
  profile's 20s window, separate from the fill pass's own ~7.5s (cursor iteration, key/outpoint
  conversion, UTXO entry deserialization). Meanwhile domain/utxoindex/store.go already maintains an
  exact, incrementally-updated per-script UTXO count (utxoCountKeyForScriptPublicKey /
  applyUTXOCountDeltas, committed atomically in the same transaction as the entries themselves,
  specifically so a caller needing the count doesn't have to scan for it) - already used by HasUTXOs,
  just not by UTXOs.
- fix: UTXOs now reads the maintained count with a single point Get (falling back to 0 on not-found)
  instead of a full cursor scan, for the limit==0 case; the limit>0 case was already O(1) (count =
  int(limit)) and is untouched. The single remaining cursor pass is the same fill loop as before,
  unchanged.
- tests: new domain/utxoindex/utxos_count_sizing_test.go
  (TestUTXOsSizesFromTheMaintainedCountNotAScan) - two scripts, several commits including an
  add-then-remove of the same outpoint within one commit (nets to zero) and a partial removal from an
  earlier commit, checks UTXOs(sp, 0, ...) returns exactly the surviving set (not the net-zero
  outpoint, not affected by the other script's churn) and that limit>0 still truncates correctly.
  This is a performance fix, not a bug fix - the pre-fix double-scan was already correct, just slow -
  so there is no "fails before, passes after" reproduction to point to; correctness is established by
  this test and the pre-existing TestHasUTXOsUsesTrackedCounts/TestUTXOsReturnsReallocatedBufferFor
  CallerCleanup (both pass unchanged), and the performance claim by code reading (one cursor pass
  removed) plus the live profile that motivated it. go vet + staticcheck clean, gofmt clean, full repo
  build clean, domain/utxoindex/... full suite green (11 tests), go test -tags=ci ./... 110 ok, rest
  of tree 49 ok, cmd/htnwallet ok, testing/integration full non-ci run green.
- left alone: HTN-215 (below) - the other major cost center from the same profile, deliberately not
  touched in this commit because it's consensus-visible (virtual parent selection) rather than a pure
  serving-path optimization like this one.
- fixed_commit: 267c7f8e1

## HTN-215
- title: pickVirtualParents' per-candidate merge-set-size check re-walks reachability from scratch for
  every DAG tip considered, with no memoization across candidates in the same virtual update
- status: open, needs_human (consensus-visible: this is block-parent-selection code, a memoization
  fix that changes iteration/call patterns around reachability queries touches exactly the kind of
  code this session has treated as measure-first-then-ask territory all along - not fixed without
  the user's go-ahead, unlike HTN-214 which was a pure non-consensus serving-path optimization)
- severity: high (this is the OTHER major contributor to the "nearly synced IBD is slow" symptom,
  and unlike HTN-214 it sits directly on the block-processing critical path, not a competing RPC
  path - see HTN-214's evidence section for the same profile this was found in)
- area: consensus/consensusstatemanager (pick_virtual_parents.go)
- reported: 2026-09-18, same live-node CPU profile as HTN-214 (see that entry for how it was taken -
  http://127.0.0.1:6060/debug/pprof/profile, 20s, read-only, node not touched).
- evidence: consensus.ValidateAndInsertBlock -> ...AddBlock -> consensusStateManager.updateVirtual ->
  pickVirtualParents -> mergeSetIncrease accounted for 19.22%/19.25%/18.28%/17.80% of the whole
  profile's CPU respectively (nearly identical, i.e. essentially all of updateVirtual's cost is this
  one call chain). go tool pprof -list mergeSetIncrease showed 9.63s of its 12.15s total inside a
  single line: `csm.dagTopologyManager.IsAncestorOfAny(stagingArea, current, selectedVirtualParents)`,
  called once per BFS-visited ancestor of each candidate tip. Downstream, IsAncestorOfAny ->
  reachabilityManager.IsDAGAncestorOf -> interval/futureCoveringSetHasAncestorOf/ReachabilityData
  together accounted for another ~9-14% of total CPU each (overlapping - these are on the same call
  chain). This IS block-processing work (not RPC contention like HTN-214), and it runs on literally
  every block while the node is "nearly synced" (updateVirtual, the internal function, is only
  called when AddBlock's updateVirtual PARAMETER is true - i.e. exactly the nearly-synced live path
  the user asked about; the bulk/far-behind IBD path defers this entirely via ResolveVirtual, which
  is why bulk IBD is fast and nearly-synced IBD is comparatively so slow - this is architectural, not
  a bug in itself, but the per-call cost inside it plausibly is more expensive than necessary).
- mechanism: pickVirtualParents (pick_virtual_parents.go:14) iterates candidate tips (up to
  maxBlockParents*3, sorted by blue work) and calls mergeSetIncrease(candidate, selectedVirtualParents,
  mergeSetSize) once per candidate, in a loop. Each call runs its OWN independent BFS from the
  candidate's parents, calling IsAncestorOfAny(current, selectedVirtualParents) for every visited
  node, with NO memoization of ancestry results across separate candidates' BFS runs within the same
  pickVirtualParents call - even though selectedVirtualParents only grows (never shrinks) across
  those calls, and the DAG being walked is identical between them. On a DAG with many tips (a busy,
  high-BPS network - exactly what this session's HTN-205/206/198/211 work has been about), this
  multiplies: candidates x BFS-size x reachability-check-cost, with a large constant-factor of
  redundant work between candidates whose BFS visits overlapping ancestor sets.
- fix_plan: not designed - this needs someone with authority over the consensus code path to decide
  the shape of a fix (e.g. a memoization cache scoped to one pickVirtualParents call, keyed by
  (blockHash, selectedVirtualParents-generation) or similar) that provably produces IDENTICAL output
  (same selectedVirtualParents, same order) to the current O(candidates x BFS) algorithm - a pure
  speed optimization with zero behavior change, not an algorithm change - before writing it. Measure
  first: how many candidates/BFS nodes does this walk on the live node in the steady state, and how
  much does memoization actually save, before deciding whether it's worth the added complexity/review
  risk in a hot consensus path.
- left alone: entirely, this session - HTN-214 was implemented (non-consensus, unambiguously safe);
  this one was not, and needs the user's decision on whether/how to proceed given it's on the
  virtual-parent-selection path.
- user decision 2026-09-18: proceed.
- FIXED 2026-09-18, commit pending (see fixed_commit below): pick_virtual_parents.go's
  pickVirtualParents loop now creates one hashset.HashSet (knownPastOfSelectedVirtualParents) before
  the candidate loop and threads it into every mergeSetIncrease call for that invocation.
  mergeSetIncrease checks it before calling IsAncestorOfAny for a visited node, and adds the node to
  it only when IsAncestorOfAny returns true. Correctness argument (why caching only "true" is safe):
  selectedVirtualParents only ever grows within one pickVirtualParents call (candidates are appended
  to it, never removed, and the set is never reused across separate pickVirtualParents calls - it's a
  local variable created fresh each time). IsAncestorOfAny(x, S) asks "is x an ancestor of ANY member
  of S" - once true for some S, it stays true for any S' ⊇ S, because the member that made it true
  is still in S'. A "false" answer has no such guarantee (a later-added member could make it true), so
  false is never cached - the function falls through to a fresh IsAncestorOfAny call every time,
  identical to the pre-fix behavior for that case. No other control flow changed: the BFS structure,
  visited-set semantics, merge-set-size accounting and every return value are untouched.
- tests: no new test added - the existing TestConsensusStateManager_pickVirtualParents (both mainnet
  and testnet configs) already builds 3*maxParents chains from shared ancestors specifically to
  exercise pickVirtualParents with many overlapping candidates (maxCandidates is exactly
  maxBlockParents*3), and cross-checks its output against BuildBlock's independently-derived parent
  selection - this is already the strongest available regression guard for exactly the scenario this
  fix targets, and it passed unchanged. This is a pure memoization (identical output by construction,
  argued above), the same class as HTN-214, so there is no fails-before/passes-after reproduction to
  add; go vet + staticcheck clean, gofmt clean, full repo build clean, domain/consensus/... full suite
  green (53 ok, including the overlapping-candidates test), go test -tags=ci ./... 110 ok, rest of
  tree 49 ok, cmd/htnwallet ok, testing/integration full non-ci run green.
- left alone: this reduces redundant work within one pickVirtualParents call; it does not change the
  BFS itself, the candidate ordering, or the merge-set-limit algorithm - none of that was touched.
  Live confirmation that this measurably reduces CPU on the production node needs a rebuild and
  redeploy, which is the user's call, not done from here (the node was only ever profiled read-only,
  never touched).
- fixed_commit: 7e9b86230

## HTN-216
- title: calcMergedBlockReward denies a fully accepted merge set block its subsidy+fees whenever it
  falls outside the difficulty-adjustment window's sample, silently underpaying nearly every coinbase
- status: FIXED (dormant until a coordinated hard fork activation - see fixed section)
- severity: CRITICAL (tokenomics-affecting: measured 99.99% of coinbases on the live mainnet node
  underpaying, some miners not paid at all for a fully accepted block)
- area: consensus/coinbasemanager (coinbasemanager.go)
- reported: 2026-09-19, via `docker logs htnd-public` (read-only, per user request: "read logs of
  htnd-public and fix the issue with coinbase.")
- evidence: the existing diagnostic log line "Coinbase being built for %s pays nothing to %d of its %d
  merge set blocks: %s ... (no reward; in the DAA added blocks set: false)" fired on 27,982 of 27,985
  coinbase builds logged over a 12-hour window (99.99%) - one instance dropped 140 of its 163 merge
  set blocks. Every dropped block's miner received zero reward for a block GHOSTDAG and acceptance
  processing had already fully accepted into the merge set.
- mechanism: calcMergedBlockReward (coinbasemanager.go) gated a merge set block's reward on
  `mergingBlockDAAAddedBlocksSet.Contains(blockHash)`. That set (daaAddedBlocksSet, sourced from
  DAABlocksStore.DAAAddedBlocks) is the subset of a block's merge set that the difficulty-adjustment
  window (a size-bounded, blue-work-ranked sampling heap - see calculateBlockWindowHeap/
  tryPushMergeSet/BlockWindowHeapSlice) happened to sample when computing that block's DAA score. That
  window exists to pick a representative, bounded-cost sample for retargeting difficulty; it was never
  designed or intended to answer "was this merge set block legitimately merged" - a fully valid,
  GHOSTDAG-accepted merge set block (blue or red) can and routinely does lose the sampling cutoff for
  having lower blue work than whatever already filled the window, especially on a busy DAG with many
  parallel blocks (exactly HTN-205/206/211/215's territory). calcMergedBlockReward's `if
  !mergingBlockDAAAddedBlocksSet.Contains(blockHash) { return 0, nil }` reused that sampling artifact
  as a reward-eligibility filter, so being outside the sample - not being invalid, not being
  unaccepted, just not sampled - meant the block earned nothing. On this network's actual DAG width
  this is the common case, not the exception, hence the 99.99% figure.
- fix: calcMergedBlockReward gained a `payRegardlessOfDAAWindow bool` parameter; when true, the
  DAA-added-blocks check is skipped entirely and every merge set block with valid acceptance data
  earns its subsidy+fees. This is a consensus/tokenomics change (it changes the computed coinbase
  transaction network-wide) so it can't apply unconditionally without an uncoordinated fork. It's
  gated behind a new `mergeSetRewardIgnoresDAAWindowVersion = 10` constant, activated per merge-set
  block's OWN version (mirroring how the existing `blockVersion >= 10` dev-fee formula check in the
  same loop is keyed - not the building block's version). Version 10 is the same not-yet-activated
  hard fork bucket that dev-fee change already claims in this file: mainnet's POWScores
  (domain/dagconfig/params.go) currently tops out at version 9, with no entry raising it to 10, so
  this fix is completely inert on the live network until a maintainer adds a coordinated activation
  DAA score - not chosen here, per this session's standing consensus-activation rule. The live call
  site (ExpectedCoinbaseTransactionInternal's v2 merge-set loop) now computes the merge set block's
  own version once (reusing it for both this gate and the existing dev-fee/coinbase-data-extraction
  calls that already needed it, removing two now-redundant c.blockVersion lookups in the process) and
  passes `mergeSetBlockVersion >= mergeSetRewardIgnoresDAAWindowVersion`. The other four call sites -
  coinbaseOutputForBlueBlockV2 (dead code, no other caller), coinbaseOutputForBlueBlockV1 and
  coinbaseOutputForRewardFromRedBlocksV1 (only reachable for ownBlockVersion == 1, i.e. genesis-era
  blocks that will never reach version 10), and coinbaseOutputForRewardFromRedBlocksV2 (dead code) -
  all pass `false`, preserving their exact pre-fix behavior; none of them needed to change.
- tests: TestCalcMergedBlockRewardPaysRegardlessOfDAAWindowFromActivation (coinbasemanager_test.go),
  a white-box test in-package: builds a coinbaseManager with a minimal fake BlockStore serving one
  block whose coinbase payload is built via the real serializeCoinbasePayload, then calls
  calcMergedBlockReward directly with an EMPTY daaAddedBlocksSet (the block is guaranteed outside the
  window) and asserts payRegardlessOfDAAWindow=false returns 0 (pins the exact pre-fix/historical
  behavior for already-mined blocks) while payRegardlessOfDAAWindow=true returns the block's own
  subsidy (proves the fix). gofmt clean, go vet clean, staticcheck (build_and_test.sh's exact check
  list) clean on the package, full package suite green, full domain/consensus/... suite green (all
  packages ok, including consensusstatemanager/blockbuilder/blockprocessor/blockvalidator which sit
  next to or call into coinbase logic), `go build -tags=ci ./...` clean across the whole tree.
- left alone: not touching domain/dagconfig/params.go's POWScores (choosing an activation DAA score is
  explicitly the user's decision per this session's standing rule, not made here); not changing the
  meaning or contents of daaAddedBlocksSet itself, or anything about how the DAA window is sampled -
  that mechanism is correct and unchanged for its actual purpose (difficulty adjustment); not touching
  the dev-fee formula or entropy logic already keyed off version 10/8/9 in the same function, even
  though they share the activation bucket - each is an independently-scoped change to the same
  not-yet-reached version number, not one combined change.
- fixed_commit: 4b4829d9c
- ACTIVATED 2026-09-19, commit 252e0adc5: user chose the activation DAA score directly ("Make the
  block v10 activate on 227679830 daa score") after being walked through why an unconditional
  (ungated) apply was not viable - it would have made this node fail re-validation of ~99.99% of its
  own already-mined chain (ExpectedCoinbaseTransactionInternal is re-derived for historical blocks
  during IBD/ResolveVirtual, not only for newly-built ones, so removing the version gate would compare
  a "pays everyone" expected coinbase against every pre-activation block's real, already-mined,
  already-accepted "pays almost nobody" coinbase). Added 227679830 as mainnet POWScores' 9th entry.
  Also extended every other per-version array in MainnetParams (K, TargetTimePerBlock,
  FinalityDuration, DifficultyAdjustmentWindowSize, PruningMultiplier, MaxBlockMass, MaxBlockParents,
  MergeDepth) from 9 to 10 entries per the user's explicit follow-up ("increase indexes of other daa
  parameters also... so it won't crash") - each new 10th entry repeats its array's version-9 value
  unchanged, so nothing about version 10's behavior changes in any of those dimensions, only the
  coinbase-manager checks explicitly gated on mergeSetRewardIgnoresDAAWindowVersion take effect from
  this DAA score. See HTN-217 for a related latent crash this surfaced and fixed on the way.
- LIVE OUTAGE 2026-09-19: within ~2 hours of htnd-public being rebuilt and redeployed on the
  activation commit, the user reported "Stratum has miners, but they can't find blocks" after the v10
  activation. Root-caused via `docker logs htnd-public` and live RPC (read-only): htnd-public was the
  ONLY node on mainnet running the version-10 code. The instant its own virtual tip reached DAA score
  227679830, it set its ambient block version to 10 and began rejecting every block relayed by
  peers - "Cannot process <hash>, Wrong block version 9, it should be 10" - because the rest of the
  network's nodes/miners are still on the old binary, whose POWScores table stops at version 9, so
  they correctly keep building/relaying version-9 blocks past that DAA score.
- REVERTED then RE-APPLIED, same day: first reverted (commit 6bf71257f) on the reasoning that an
  uncoordinated activation had forked this node off the network alone. The user explicitly overrode
  this ("Don't revert the hard fork... Fix the issue! Don't revert.") - the peer-relay rejection of
  unupgraded peers is an accepted, known consequence of activating without a rollout window, not
  itself what needed fixing; the actual complaint was that mining had stopped working on this node's
  own v10 chain, which is a different, fixable problem. Re-applied 227679830 as POWScores' 9th entry
  (commit 0f4441be9) once that distinction was clear.
- ACTUAL ROOT CAUSE FOUND AND FIXED (commit 0f4441be9): confirmed via `GetBlockDagInfo`/`GetInfo`
  (read-only) that virtualDaaScore was stuck exactly at 227679830 for 3+ hours with isSynced=false,
  and via `docker logs htnd-public` that RPC clients were being disconnected every ~2 minutes with
  "Multiset <hash> does not exist in db" - traced to `calculateMultiset` (multisets.go) needing
  virtual's selected parent's stored multiset to build any new candidate block, including a mining
  template (`MiningManager.GetBlockTemplate` -> `HandleGetBlockTemplate` -> the same failing error
  propagated straight back to the RPC caller, explaining why the stratum bridge's clients kept
  disconnecting/reconnecting with 0 H/s). The missing multiset traces to `RepairBlockStatuses`
  (`--repair-block-statuses true`, baked permanently into this node's docker-compose launch command
  rather than used once as its own doc comment says - "ask for it when recovering", not something to
  run on every boot): it blindly re-marks every non-invalid, non-header-only block StatusUTXOValid
  without ever computing a UTXO diff or multiset for it - a hazard already documented in
  RepairDisqualifiedTipChains's own comment ("which is how a repaired node ends up with UTXO-valid
  blocks that have no diff"), just not yet fixed for the UTXOValid (as opposed to disqualified) case.
  Independent of and unrelated to the v10 activation itself - it would have bitten this node on any
  restart with that flag set, at any block version.
- fix: new `RepairMissingMultisets()` on `*consensus` (consensus.go), mirroring
  `RepairDisqualifiedTipChains`'s already-reviewed approach exactly rather than inventing new
  multiset-reconstruction math under time pressure: walks each virtual tip's selected-parent chain,
  and for every StatusUTXOValid block with no stored multiset, marks it StatusUTXOPendingVerification
  so the normal resolve path (`getUnverifiedChainBlocks`/`ResolveVirtual`) re-derives a real diff and
  multiset for it - the same safe mechanism already proven for disqualified chains. Stops each
  branch's walk at the first ancestor that already has a multiset (a block only ever becomes
  UTXOValid through the normal path after its own multiset is staged, so anything below an
  already-good block was resolved correctly and needs no checking). Wired as a new hidden, opt-in CLI
  flag `--repair-missing-multisets` (infrastructure/config/config.go, consensus.Config, factory.go),
  matching `RepairBlockStatuses`'s own convention - default off, run once to recover, not left on
  every boot (the exact rule the OTHER flag's own doc comment already states but the launch command
  ignores).
- tests: TestRepairMissingMultisetsResetsOnlyTheBlocksMissingOne (deletes the multisets of the two
  blocks nearest a 4-block chain's tip, requires the repair to reset exactly those two and stop at the
  boundary block that still has one) and
  TestRepairMissingMultisetsResetsNothingWhenEveryMultisetExists (healthy DAG comes back untouched) -
  both mirroring RepairDisqualifiedTipChains's own test structure. gofmt/vet/staticcheck clean, full
  domain/consensus package suite green, full repo build clean.
- left alone: NOT changing the docker-compose launch command myself (operational, the user's own
  infrastructure); NOT removing or fixing `RepairBlockStatuses` itself (still exists as-is for its own
  documented one-time-recovery purpose); NOT deploying this fix - my sandbox's permission classifier
  blocks `docker compose build`/`up`/`exec` and even a live read-only `GetBlockTemplate`/`GetBlock`
  RPC call against the production stack, so the user must rebuild and redeploy the htnd service
  themselves and run once with `--repair-missing-multisets` (then remove it again, and remove the
  permanent `--repair-block-statuses true` too, to stop this recurring) to actually end the outage.
- fixed_commit: 0f4441be9

## HTN-217
- title: Two call sites indexed a per-version Params table directly with
  constants.GetBlockVersion()-1, with no bounds clamp - would panic once the ambient version outran
  the table
- status: FIXED
- severity: high (a guaranteed panic/crash on every node, at the exact moment any future hard fork's
  block version becomes reachable, if the corresponding per-version table isn't extended first)
- area: consensus/factory, miningmanager/mempool
- reported: 2026-09-19, found while extending mainnet's per-version tables for HTN-216's version 10
  activation (see that entry) - checking whether any other code indexes those same tables the same way
  turned up two direct, unclamped indexes.
- mechanism: every other per-version lookup in this codebase goes through
  blockVersionIndexForSlice/currentBlockVersionIndexForSlice, which clamps the index to the last
  element if constants.GetBlockVersion() (or a given blockVersion) exceeds the table's length -
  FinalityDepthForBlockVersion, PruningDepthForBlockVersion and TargetTimePerBlockForCurrentVersion
  all use it. Two call sites did not: domain/consensus/factory.go's dagStores
  (`config.DifficultyAdjustmentWindowSize[constants.GetBlockVersion()-1]`) and
  domain/miningmanager/mempool/config.go's DefaultConfig
  (`dagParams.MaxBlockMass[constants.GetBlockVersion()-1]`). Both run once, at node/mempool
  construction time, using whatever constants.GetBlockVersion() is at that moment - which can be any
  value once IBD has raised it. Before this session extended MainnetParams' tables to 10 entries
  (HTN-216's activation commit), DifficultyAdjustmentWindowSize and MaxBlockMass both had exactly 9
  entries (indices 0-8, versions 1-9); the moment a node's ambient version reached 10, both of these
  would index position 9 of a 9-element slice and panic with an index-out-of-range, crashing the node
  (or failing mempool construction) outright - not a wrong value, a hard crash.
- fix: added DifficultyAdjustmentWindowSizeForCurrentVersion and MaxBlockMassForCurrentVersion to
  Params (domain/dagconfig/params.go), matching the existing TargetTimePerBlockForCurrentVersion
  pattern exactly (delegates to currentBlockVersionIndexForSlice). Switched both call sites to use
  them instead of indexing the raw slice. Purely a safety net: for every currently valid block version
  the clamped and unclamped forms return byte-identical results; they only diverge once the version
  outruns the table, where the clamped form now reuses the last entry instead of panicking - the same
  fallback every other per-version lookup in this file already uses.
- tests: no new test - this is the same well-established clamping pattern already covered by
  blockVersionIndexForSlice's own use in FinalityDepthForBlockVersion/PruningDepthForBlockVersion,
  which is exercised throughout domain/consensus's existing suite; gofmt/vet/staticcheck clean, full
  domain/... suite green, full repo build clean with and without -tags=ci.
- left alone: did not audit every OTHER direct slice index in the codebase for the same pattern beyond
  what surfaced from this specific investigation (K, MaxBlockParents and MergeDepth are already
  clamped at their actual call sites per earlier reading - see HTN-216's activation entry); a broader
  sweep for other unclamped per-version indexing was not performed this session.
- fixed_commit: 220cb55ad

## HTN-218
- title: A candidate tip losing the local tip race to an already-valid selected parent was logged at
  Warn, making routine ResolveVirtual behavior look like a problem
- status: FIXED
- severity: low (cosmetic - log level only, no behavior change)
- area: consensus/consensusstatemanager
- reported: 2026-09-19, user pasted a live log line: "Pending tip <hash> does not overcome previous
  selected parent <hash>, which is UTXO-valid. Keeping it as virtual's selected parent." and initially
  asked to "fix that so it won't stop node" - investigated first and confirmed via live logs the node
  had NOT stopped (it kept accepting blocks continuously through and after the warning). The user then
  pointed out the real issue: "Does not matter at all if pending tip is older than selected parent" -
  i.e. a block losing the tip race to a heavier, already-valid chain is ordinary GHOSTDAG/DAGKnight
  behavior on any DAG with more than one tip, not something exceptional, so it should not be logged as
  a warning.
- mechanism: resolve.go's ResolveVirtual has two occurrences of this exact log line (mirroring each
  other - see HTN-211), both gated on `previousVirtualSelectedParentStatus == StatusUTXOValid`, i.e.
  exactly the "this is fine, correctly keeping the current tip" case HTN-211 added. A third, distinct
  occurrence (previous selected parent NOT UTXO-valid - "could happen in nearly synced scenarios where
  GHOSTDAG data isn't fully consistent") is a genuinely more unusual state and was left as a warning.
- fix: downgraded the two "previous selected parent is UTXO-valid" occurrences from log.Warnf to
  log.Debugf. Zero control-flow or decision change - verified against the existing HTN-211 tests
  (TestResolveVirtualKeepsValidSelectedParentOverLighterPendingTip and its ShortChain variant), which
  passed unchanged.
- tests: no new test - this is a pure log-level change with no observable behavior difference; the
  existing HTN-211 regression tests already cover the actual decision logic and continue to pass.
  gofmt/vet/staticcheck clean, full domain/consensus/processes/consensusstatemanager suite green,
  whole-tree build clean with and without -tags=ci.
- commit: 175064f21

## HTN-219
- title: A disconnected RPC client's notification listener could stay registered forever, logging
  "Couldn't send message to closed route" on every subsequent notification broadcast
- status: FIXED
- severity: medium (unbounded per-connection resource leak under client churn, plus continuous log
  spam - not consensus-affecting, but a real operational problem for a node serving many RPC clients,
  e.g. a mining pool's stratum bridge)
- area: rpc/netadapter
- reported: 2026-09-19, user pasted a live log window: "Couldn't send message to closed route 'on RPC
  connected - outgoing'" firing once per accepted block, continuously. Measured: 445 occurrences over
  15 minutes, 227 of those in the single most recent minute - not tapering off, i.e. not a transient
  race resolving itself, a permanent leak.
- mechanism: app/rpc/rpccontext/notificationmanager.go broadcasts every notification (block added,
  UTXOs changed, virtual DAA score changed, etc.) to every router registered in
  NotificationManager.listeners, via router.OutgoingRoute().MaybeEnqueue(notification) -
  MaybeEnqueue is deliberately designed to swallow a closed-route error without propagating it
  (logging only "Couldn't send message to closed route", app/rpc.go:118), so a stale entry never
  surfaces as anything worse than this log line - it just never stops firing.
  RemoveListener (unregistering a router from that map) is only called via a defer inside the
  goroutine running handleIncomingMessages (app/rpc/rpc.go), which fires when that loop's next call to
  incomingRoute.Dequeue() notices the route is closed. But handleIncomingMessages can be blocked well
  past that point: address-index commands (GetUsableAddressesRequest and similar, comment at
  app/rpc/rpc.go:90 - "an address with many coins, such as a mining pool's, takes minutes") are pushed
  into a separate, capacity-100 addressIndexRequests channel and processed by a dedicated worker
  goroutine, one at a time. GetUsableAddressesRequest was observed as 85-98% of all RPC traffic on
  this node throughout the day (RPCSTATS), and this session's own live profiling (HTN-214) and the
  day's "Consensus has held its lock for longer than 2s" warnings already established that consensus
  lock contention on this node is real and ongoing. If that worker is stuck on a slow request when a
  client disconnects, and the client had already queued enough address-index requests to fill the
  channel, handleIncomingMessages' main loop stays blocked trying to enqueue another one - it never
  gets back to Dequeue() to notice the connection died, so RemoveListener never fires, and the listener
  stays registered for as long as that block persists (observed: at least 15+ minutes, continuously).
  Meanwhile the transport layer already knows the connection is dead immediately -
  NetConnection's internal disconnect callback (netconnection.go) synchronously calls
  netConnection.router.Close() the moment the underlying connection drops - but nothing in app/rpc used
  that signal; onRPCConnectedHandler (netadapter.go) unconditionally set the connection's
  onDisconnectedHandler to a no-op right after the RouterInitializer ran, discarding whatever the
  RouterInitializer might have registered.
- fix: added an exported NetConnection.SetOnDisconnectedHandler (netconnection.go) so
  app/rpc/rpc.go's routerInitializer, which runs in a different package, can register real cleanup.
  routerInitializer now registers a handler that calls NotificationManager.RemoveListener(rtr)
  immediately. Fixed onRPCConnectedHandler to only install the no-op fallback when the
  RouterInitializer didn't already set a handler, instead of unconditionally overwriting it. The
  existing deferred RemoveListener inside handleIncomingMessages' goroutine is left in place as a
  backstop (RemoveListener on an already-removed listener is a no-op, so both paths can safely fire).
- tests: TestRPCConnectedHandlerDoesNotClobberRouterInitializersDisconnectedHandler
  (infrastructure/network/netadapter package) - a fake RouterInitializer registers a real disconnect
  handler via the new exported setter, and the test requires it to actually fire on disconnect rather
  than being silently replaced by onRPCConnectedHandler's no-op. Verified it fails on the old
  unconditional-overwrite code (reverted, ran red) and passes with the fix restored.
  TestRPCConnectedHandlerFallsBackToANoOpWhenNoneIsSet pins the other half - start() panics if
  onDisconnectedHandler is nil, so the no-op fallback must still apply when nothing else was
  registered. gofmt/vet/staticcheck clean, full infrastructure/network/netadapter and app/rpc suites
  green, whole-tree build clean with and without -tags=ci.
- left alone: did not change handleIncomingMessages' own address-index queueing/serialization
  behavior, which is itself a separate, deliberate design (comment at app/rpc/rpc.go:90) to stop one
  slow address-balance query from blocking a mining pool's GetBlockTemplate/SubmitBlock traffic on the
  same connection - this fix only ensures a dead connection's listener is removed promptly regardless
  of whether that queue is backed up, not the queueing design itself.
- commit: cd2b11676
