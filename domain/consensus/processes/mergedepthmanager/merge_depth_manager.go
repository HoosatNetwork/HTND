package mergedepthmanager

import (
	"fmt"

	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/ruleerrors"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/infrastructure/db/database"
	"github.com/pkg/errors"
)

// blockVersionForBlock returns the block version that governs blockHash, derived from a DAA score
// this node computed itself.
//
// Not from the header: checkDAAScore is currently disabled, so a peer could otherwise choose its own
// merge depth by lying about its header. Not from constants.GetBlockVersion() either, except as a
// last-resort bootstrap fallback - see mergeDepthForBlock.
func (mdm *mergeDepthManager) blockVersionForBlock(stagingArea *model.StagingArea,
	blockHash *externalapi.DomainHash, ghostdagData *externalapi.BlockGHOSTDAGData,
) (uint16, error) {
	daaScore, err := mdm.daaBlocksStore.DAAScore(mdm.databaseContext, stagingArea, blockHash)
	if err == nil {
		return constants.BlockVersionForDAAScore(mdm.powScores, daaScore), nil
	}
	if !database.IsNotFoundError(err) {
		return 0, err
	}

	// Virtual gets colored and has its merge depth root taken inside boundedMergeBreakingParents,
	// which runs during pickVirtualParents - before updateVirtualWithParents stages virtual's DAA
	// data - so virtual's own DAA score is routinely not yet available here. Its selected parent is a
	// real block whose DAA score is staged, and version boundaries (POWScores) sit millions of DAA
	// scores apart, so the selected parent's version is the version that governs virtual.
	if ghostdagData != nil && ghostdagData.SelectedParent() != nil {
		daaScore, err = mdm.daaBlocksStore.DAAScore(mdm.databaseContext, stagingArea, ghostdagData.SelectedParent())
		if err == nil {
			return constants.BlockVersionForDAAScore(mdm.powScores, daaScore), nil
		}
		if !database.IsNotFoundError(err) {
			return 0, err
		}
	}

	// Bootstrap only: genesis, virtual genesis, and the pruning-point import, where neither the block
	// nor its selected parent has a DAA score yet. Every one of those paths returns genesis or virtual
	// genesis as the merge depth root regardless of the depth, so the ambient ratchet cannot do harm
	// here the way it does on a live chain.
	log.Debugf("no DAA score for %s or its selected parent - falling back to the ambient block version",
		blockHash)
	return constants.GetBlockVersion(), nil
}

// mergeDepthForBlock returns the merge depth that governs blockHash, derived from that block's own
// version rather than from the ambient process state.
//
// It must NOT be indexed by constants.GetBlockVersion(). That is a process-global one-way ratchet
// which starts at 1 on every restart and only rises as blocks arrive, so for the first seconds or
// minutes of a node's life it reports version 1 and indexes the tables at 0 - mainnet's
// MergeDepth[0] is 360 where MergeDepth[8] is 3600, a tenfold difference in a consensus bound. Two
// nodes on the same binary and the same DAG therefore disagreed about the merge depth purely
// according to how long each had been running, and calculateAndStageMergeDepthRoot PERSISTS the root
// it computes while its walk only ever moves forward up the selected chain - so a root staged during
// that startup window is a root that is ten times too shallow, is never revisited, and turns
// ordinary reds into ErrViolatingBoundedMergeDepth for as long as it stays in the store.
func (mdm *mergeDepthManager) mergeDepthForBlock(stagingArea *model.StagingArea,
	blockHash *externalapi.DomainHash, ghostdagData *externalapi.BlockGHOSTDAGData,
) (uint64, error) {
	if len(mdm.mergeDepth) == 0 {
		return 0, errors.New("merge depth configuration is empty")
	}

	blockVersion, err := mdm.blockVersionForBlock(stagingArea, blockHash, ghostdagData)
	if err != nil {
		return 0, err
	}
	if blockVersion == 0 {
		return 0, errors.Errorf("invalid block version %d for block %s", blockVersion, blockHash)
	}

	index := int(blockVersion) - 1
	if index >= len(mdm.mergeDepth) {
		log.Warnf("merge depth config has %d entries but block %s is version %d; falling back to last entry",
			len(mdm.mergeDepth), blockHash, blockVersion)
		return mdm.mergeDepth[len(mdm.mergeDepth)-1], nil
	}
	return mdm.mergeDepth[index], nil
}

type mergeDepthManager struct {
	databaseContext     model.DBReader
	dagTopologyManager  model.DAGTopologyManager
	dagTraversalManager model.DAGTraversalManager
	finalityManager     model.FinalityManager

	genesisHash *externalapi.DomainHash
	mergeDepth  []uint64
	// powScores is the per-version DAA score activation table, used to derive a block's own version
	// from its DAA score - see mergeDepthForBlock.
	powScores []uint64

	ghostdagDataStore   model.GHOSTDAGDataStore
	mergeDepthRootStore model.MergeDepthRootStore
	daaBlocksStore      model.DAABlocksStore
	pruningStore        model.PruningStore
	finalityStore       model.FinalityStore
}

// New instantiates a new MergeDepthManager
func New(
	databaseContext model.DBReader,
	dagTopologyManager model.DAGTopologyManager,
	dagTraversalManager model.DAGTraversalManager,
	finalityManager model.FinalityManager,

	genesisHash *externalapi.DomainHash,
	mergeDepth []uint64,
	powScores []uint64,

	ghostdagDataStore model.GHOSTDAGDataStore,
	mergeDepthRootStore model.MergeDepthRootStore,
	daaBlocksStore model.DAABlocksStore,
	pruningStore model.PruningStore,
	finalityStore model.FinalityStore,
) model.MergeDepthManager {
	return &mergeDepthManager{
		databaseContext:     databaseContext,
		dagTopologyManager:  dagTopologyManager,
		dagTraversalManager: dagTraversalManager,
		finalityManager:     finalityManager,
		genesisHash:         genesisHash,
		mergeDepth:          mergeDepth,
		powScores:           powScores,
		ghostdagDataStore:   ghostdagDataStore,
		mergeDepthRootStore: mergeDepthRootStore,
		daaBlocksStore:      daaBlocksStore,
		pruningStore:        pruningStore,
		finalityStore:       finalityStore,
	}
}

// CheckBoundedMergeDepth is used for validation, so must follow the HF1 DAA score for determining the correct depth to verify
func (mdm *mergeDepthManager) CheckBoundedMergeDepth(stagingArea *model.StagingArea, blockHash *externalapi.DomainHash, ghostdagData *externalapi.BlockGHOSTDAGData, header externalapi.BlockHeader, isBlockWithTrustedData bool) error {
	// Return nil on genesis
	if ghostdagData.SelectedParent() == nil {
		return nil
	}

	mergeDepthRoot, err := mdm.MergeDepthRoot(stagingArea, blockHash, isBlockWithTrustedData)
	if err != nil {
		return err
	}

	// We call FinalityPoint in order to save it to storage.
	_, err = mdm.finalityManager.FinalityPoint(stagingArea, blockHash, isBlockWithTrustedData)
	if err != nil {
		return err
	}

	nonBoundedMergeDepthViolatingBlues, err := mdm.NonBoundedMergeDepthViolatingBlues(stagingArea, blockHash, mergeDepthRoot)
	if err != nil {
		return err
	}

	for _, red := range ghostdagData.MergeSetReds() {
		doesRedHaveMergeRootInPast, err := mdm.dagTopologyManager.IsAncestorOf(stagingArea, mergeDepthRoot, red)
		if err != nil {
			return err
		}

		if doesRedHaveMergeRootInPast {
			continue
		}

		isRedInPastOfAnyNonMergeDepthViolatingBlue, err := mdm.dagTopologyManager.IsAncestorOfAny(stagingArea, red, nonBoundedMergeDepthViolatingBlues)
		if err != nil {
			return err
		}
		if !isRedInPastOfAnyNonMergeDepthViolatingBlue && header.DAAScore() >= 43334184+1000000 {
			mdm.logBoundedMergeDepthViolation(stagingArea, blockHash, ghostdagData, header, mergeDepthRoot, red,
				nonBoundedMergeDepthViolatingBlues)
			return errors.Wrapf(ruleerrors.ErrViolatingBoundedMergeDepth, "block is violating bounded merge depth")
		}
	}

	return nil
}

// logBoundedMergeDepthViolation dumps every input the bounded merge depth verdict was reached from.
//
// The rule rejects with a single sentence and no operands, which makes a stuck chain - every mined
// block rejected, no way to tell which of the several independent preconditions actually failed -
// essentially undiagnosable from the logs. This runs only on the rejection path, at most once per
// rejected block, so it costs nothing in the normal case.
func (mdm *mergeDepthManager) logBoundedMergeDepthViolation(stagingArea *model.StagingArea,
	blockHash *externalapi.DomainHash, ghostdagData *externalapi.BlockGHOSTDAGData, header externalapi.BlockHeader,
	mergeDepthRoot, offendingRed *externalapi.DomainHash, kosherizingBlues []*externalapi.DomainHash,
) {
	// Every lookup here is best-effort: this is a diagnostic on an already-failing path and must never
	// turn a rule error into something else.
	blueScoreOf := func(hash *externalapi.DomainHash) string {
		data, err := mdm.ghostdagDataStore.Get(mdm.databaseContext, stagingArea, hash, false)
		if err != nil {
			return "<no ghostdag data: " + err.Error() + ">"
		}
		return fmt.Sprintf("%d", data.BlueScore())
	}
	daaScoreOf := func(hash *externalapi.DomainHash) string {
		daaScore, err := mdm.daaBlocksStore.DAAScore(mdm.databaseContext, stagingArea, hash)
		if err != nil {
			return "<no DAA score>"
		}
		return fmt.Sprintf("%d", daaScore)
	}
	describe := func(hash *externalapi.DomainHash) string {
		switch {
		case hash == nil:
			return "<nil>"
		case hash.Equal(model.VirtualGenesisBlockHash):
			return "VIRTUAL-GENESIS"
		case hash.Equal(mdm.genesisHash):
			return "GENESIS"
		}
		return fmt.Sprintf("%s (blueScore=%s daaScore=%s)", hash, blueScoreOf(hash), daaScoreOf(hash))
	}

	selectedParent := ghostdagData.SelectedParent()
	mergeDepth, mergeDepthErr := mdm.mergeDepthForBlock(stagingArea, blockHash, ghostdagData)
	blockVersion, versionErr := mdm.blockVersionForBlock(stagingArea, blockHash, ghostdagData)

	log.Warnf("[MERGE-DEPTH] block %s REJECTED with ErrViolatingBoundedMergeDepth", blockHash)
	log.Warnf("[MERGE-DEPTH]   block:          headerVersion=%d headerDAAScore=%d blueScore=%d dynamicK=%d",
		header.Version(), header.DAAScore(), ghostdagData.BlueScore(), ghostdagData.DynamicK())
	log.Warnf("[MERGE-DEPTH]   mergeSet:       %d blues, %d reds; directParents=%d",
		len(ghostdagData.MergeSetBlues()), len(ghostdagData.MergeSetReds()), len(header.DirectParents()))
	log.Warnf("[MERGE-DEPTH]   selectedParent: %s", describe(selectedParent))
	log.Warnf("[MERGE-DEPTH]   derivedVersion=%v (err=%v) -> mergeDepth=%v (err=%v)",
		blockVersion, versionErr, mergeDepth, mergeDepthErr)
	if mergeDepthErr == nil && ghostdagData.BlueScore() >= mergeDepth {
		log.Warnf("[MERGE-DEPTH]   requiredBlueScore (blueScore-mergeDepth) = %d",
			ghostdagData.BlueScore()-mergeDepth)
	}
	log.Warnf("[MERGE-DEPTH]   mergeDepthRoot: %s", describe(mergeDepthRoot))

	// Where the root came from. calculateMergeDepthRoot starts at the selected parent's STORED root
	// and only ever walks forward, so a wrong stored value here is never corrected by the walk.
	if selectedParent != nil {
		storedRoot, err := mdm.mergeDepthRootStore.MergeDepthRoot(mdm.databaseContext, stagingArea, selectedParent)
		if err != nil {
			log.Warnf("[MERGE-DEPTH]   selectedParent's stored root: <none: %s> - the walk fell back to its "+
				"finality point instead", err)
			finalityPoint, finalityErr := mdm.finalityStore.FinalityPoint(mdm.databaseContext, stagingArea, selectedParent)
			if finalityErr != nil {
				log.Warnf("[MERGE-DEPTH]   selectedParent's finality point: <none: %s>", finalityErr)
			} else {
				log.Warnf("[MERGE-DEPTH]   selectedParent's finality point: %s", describe(finalityPoint))
			}
		} else {
			log.Warnf("[MERGE-DEPTH]   selectedParent's stored root: %s", describe(storedRoot))
		}
	}

	pruningPoint, err := mdm.pruningStore.PruningPoint(mdm.databaseContext, stagingArea)
	if err != nil {
		log.Warnf("[MERGE-DEPTH]   pruningPoint:   <unavailable: %s>", err)
	} else {
		onChain, chainErr := mdm.dagTopologyManager.IsInSelectedParentChainOf(stagingArea, pruningPoint, blockHash)
		log.Warnf("[MERGE-DEPTH]   pruningPoint:   %s; isInSelectedParentChainOf(block)=%v (err=%v)",
			describe(pruningPoint), onChain, chainErr)
	}

	// Why nothing kosherized the red. A kosherizing block must be a merge set BLUE that has the merge
	// depth root in its SELECTED PARENT CHAIN; an empty set here means the rule cannot be satisfied by
	// any red at all, whatever the reds happen to be.
	log.Warnf("[MERGE-DEPTH]   kosherizingBlues: %d of %d merge set blues have the root in their "+
		"selected parent chain", len(kosherizingBlues), len(ghostdagData.MergeSetBlues()))
	if len(kosherizingBlues) == 0 {
		log.Warnf("[MERGE-DEPTH]   -> NO kosherizing blue exists, so EVERY red whose past lacks the merge " +
			"depth root rejects the block. Check the root and the blues above, not the reds.")
	}
	for i, blue := range ghostdagData.MergeSetBlues() {
		if i >= 8 {
			log.Warnf("[MERGE-DEPTH]     ... and %d more blues", len(ghostdagData.MergeSetBlues())-i)
			break
		}
		inChain, chainErr := mdm.dagTopologyManager.IsInSelectedParentChainOf(stagingArea, mergeDepthRoot, blue)
		log.Warnf("[MERGE-DEPTH]     blue %s: rootInSelectedParentChain=%v (err=%v)", describe(blue), inChain, chainErr)
	}

	log.Warnf("[MERGE-DEPTH]   offending red:  %s", describe(offendingRed))
	rootInRedPast, rootErr := mdm.dagTopologyManager.IsAncestorOf(stagingArea, mergeDepthRoot, offendingRed)
	log.Warnf("[MERGE-DEPTH]     isAncestorOf(mergeDepthRoot, red)=%v (err=%v)", rootInRedPast, rootErr)

	// How widespread the failure is: one stray deep red is a different problem from every red failing.
	withRoot, withoutRoot := 0, 0
	for _, red := range ghostdagData.MergeSetReds() {
		hasRoot, err := mdm.dagTopologyManager.IsAncestorOf(stagingArea, mergeDepthRoot, red)
		if err != nil {
			continue
		}
		if hasRoot {
			withRoot++
		} else {
			withoutRoot++
		}
	}
	log.Warnf("[MERGE-DEPTH]   reds: %d have the merge depth root in their past, %d do NOT",
		withRoot, withoutRoot)
}

func (mdm *mergeDepthManager) NonBoundedMergeDepthViolatingBlues(
	stagingArea *model.StagingArea, blockHash, mergeDepthRoot *externalapi.DomainHash,
) ([]*externalapi.DomainHash, error) {
	ghostdagData, err := mdm.ghostdagDataStore.Get(mdm.databaseContext, stagingArea, blockHash, false)
	if database.IsNotFoundError(err) {
		log.Infof("NonBoundedMergeDepthViolatingBlues failed to retrieve with %s\n", blockHash)
		return nil, err
	}
	if err != nil {
		return nil, err
	}

	nonBoundedMergeDepthViolatingBlues := make([]*externalapi.DomainHash, 0, len(ghostdagData.MergeSetBlues()))
	for _, blue := range ghostdagData.MergeSetBlues() {
		isMergeDepthRootInSelectedChainOfBlue, err := mdm.dagTopologyManager.IsInSelectedParentChainOf(stagingArea, mergeDepthRoot, blue)
		if err != nil {
			return nil, err
		}

		if isMergeDepthRootInSelectedChainOfBlue {
			nonBoundedMergeDepthViolatingBlues = append(nonBoundedMergeDepthViolatingBlues, blue)
		}
	}

	return nonBoundedMergeDepthViolatingBlues, nil
}

func (mdm *mergeDepthManager) VirtualMergeDepthRoot(stagingArea *model.StagingArea) (*externalapi.DomainHash, error) {
	log.Tracef("VirtualMergeDepthRoot start")
	defer log.Tracef("VirtualMergeDepthRoot end")

	virtualMergeDepthRoot, err := mdm.calculateMergeDepthRoot(stagingArea, model.VirtualBlockHash, false)
	if err != nil {
		return nil, err
	}
	log.Debugf("The current virtual merge depth root is: %s", virtualMergeDepthRoot)

	return virtualMergeDepthRoot, nil
}

func (mdm *mergeDepthManager) MergeDepthRoot(stagingArea *model.StagingArea, blockHash *externalapi.DomainHash, isBlockWithTrustedData bool) (*externalapi.DomainHash, error) {
	log.Tracef("MergeDepthRoot start")
	defer log.Tracef("MergeDepthRoot end")
	if blockHash.Equal(model.VirtualBlockHash) {
		return mdm.VirtualMergeDepthRoot(stagingArea)
	}
	root, err := mdm.mergeDepthRootStore.MergeDepthRoot(mdm.databaseContext, stagingArea, blockHash)
	if err != nil {
		log.Debugf("%s merge depth root not found in store - calculating", blockHash)
		if errors.Is(err, database.ErrNotFound) {
			return mdm.calculateAndStageMergeDepthRoot(stagingArea, blockHash, isBlockWithTrustedData)
		}
		return nil, err
	}
	return root, nil
}

func (mdm *mergeDepthManager) calculateAndStageMergeDepthRoot(
	stagingArea *model.StagingArea, blockHash *externalapi.DomainHash, isBlockWithTrustedData bool,
) (*externalapi.DomainHash, error) {
	root, err := mdm.calculateMergeDepthRoot(stagingArea, blockHash, isBlockWithTrustedData)
	if err != nil {
		return nil, err
	}
	mdm.mergeDepthRootStore.StageMergeDepthRoot(stagingArea, blockHash, root)
	return root, nil
}

func (mdm *mergeDepthManager) calculateMergeDepthRoot(stagingArea *model.StagingArea, blockHash *externalapi.DomainHash, isBlockWithTrustedData bool) (
	*externalapi.DomainHash, error,
) {
	log.Tracef("calculateMergeDepthRoot start")
	defer log.Tracef("calculateMergeDepthRoot end")

	if isBlockWithTrustedData {
		return model.VirtualGenesisBlockHash, nil
	}

	ghostdagData, err := mdm.ghostdagDataStore.Get(mdm.databaseContext, stagingArea, blockHash, false)
	if database.IsNotFoundError(err) {
		log.Infof("calculateMergeDepthRoot failed to retrieve with %s\n", blockHash)
		return nil, err
	}
	if err != nil {
		return nil, err
	}

	mergeDepth, err := mdm.mergeDepthForBlock(stagingArea, blockHash, ghostdagData)
	if err != nil {
		return nil, err
	}

	if ghostdagData.BlueScore() < mergeDepth {
		log.Debugf("%s blue score lower then merge depth - returning genesis as merge depth root", blockHash)
		return mdm.genesisHash, nil
	}

	pruningPoint, err := mdm.pruningStore.PruningPoint(mdm.databaseContext, stagingArea)
	if err != nil {
		return nil, err
	}
	pruningPointGhostdagData, err := mdm.ghostdagDataStore.Get(mdm.databaseContext, stagingArea, pruningPoint, false)
	if err != nil {
		return nil, err
	}
	if ghostdagData.BlueScore() < pruningPointGhostdagData.BlueScore()+mergeDepth {
		log.Debugf("%s blue score less than merge depth over pruning point - returning virtual genesis as merge depth root", blockHash)
		return model.VirtualGenesisBlockHash, nil
	}
	isPruningPointOnChain, err := mdm.dagTopologyManager.IsInSelectedParentChainOf(stagingArea, pruningPoint, blockHash)
	if err != nil {
		return nil, err
	}
	if !isPruningPointOnChain {
		log.Debugf("pruning point not in selected chain of %s - returning virtual genesis as merge depth root", blockHash)
		return model.VirtualGenesisBlockHash, nil
	}

	selectedParent := ghostdagData.SelectedParent()
	if selectedParent.Equal(mdm.genesisHash) {
		return mdm.genesisHash, nil
	}

	current, err := mdm.mergeDepthRootStore.MergeDepthRoot(mdm.databaseContext, stagingArea, ghostdagData.SelectedParent())
	if database.IsNotFoundError(err) {
		// This should only occur for a few blocks following the upgrade
		log.Debugf("merge point root not in store for %s, falling back to finality point", ghostdagData.SelectedParent())
		current, err = mdm.finalityStore.FinalityPoint(mdm.databaseContext, stagingArea, ghostdagData.SelectedParent())
		if err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	// In this case we expect the pruning point or a block above it to be the merge depth root.
	// Note that above we already verified the chain and distance conditions for this
	if current.Equal(model.VirtualGenesisBlockHash) {
		current = pruningPoint
	}

	requiredBlueScore := ghostdagData.BlueScore() - mergeDepth

	// The walk below only ever moves FORWARD, up the selected parent chain, on the assumption that the
	// starting point sits at or below requiredBlueScore. A stored root that is already above it makes
	// the loop return on its first iteration with a root that is too RECENT - and a too-recent merge
	// depth root is exactly what turns ordinary reds into ErrViolatingBoundedMergeDepth, since fewer
	// of them have it in their past.
	//
	// That is not hypothetical: roots staged while mergeDepthForBlock still read the process-global
	// block version were computed against MergeDepth[0] (360 on mainnet) instead of the block's real
	// MergeDepth[8] (3600), so they sit roughly ten times too shallow, and nothing ever revisits them.
	// Restart from the pruning point when that is detected, which both repairs such an entry and costs
	// nothing in the normal case. The distance and chain conditions checked above guarantee the
	// pruning point itself is at or below requiredBlueScore, so the walk is well-founded from there.
	currentGHOSTDAGData, err := mdm.ghostdagDataStore.Get(mdm.databaseContext, stagingArea, current, false)
	if err != nil {
		return nil, err
	}
	if currentGHOSTDAGData.BlueScore() >= requiredBlueScore {
		log.Debugf("stored merge depth root %s for %s has blue score %d above the required %d - "+
			"recomputing from the pruning point", current, blockHash, currentGHOSTDAGData.BlueScore(),
			requiredBlueScore)
		current = pruningPoint
	}
	log.Debugf("%s's merge depth root is the one having the highest blue score lower then %d", blockHash, requiredBlueScore)

	var next *externalapi.DomainHash
	for {
		next, err = mdm.dagTopologyManager.ChildInSelectedParentChainOf(stagingArea, current, blockHash)
		if err != nil {
			return nil, err
		}
		nextGHOSTDAGData, err := mdm.ghostdagDataStore.Get(mdm.databaseContext, stagingArea, next, false)
		if err != nil {
			return nil, err
		}
		if nextGHOSTDAGData.BlueScore() >= requiredBlueScore {
			log.Debugf("%s's merge depth root is %s", blockHash, current)
			return current, nil
		}

		current = next
	}
}
