package mergedepthmanager

import (
	"github.com/Hoosat-Oy/HTND/domain/consensus/model"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/domain/consensus/ruleerrors"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/constants"
	"github.com/Hoosat-Oy/HTND/infrastructure/db/database"
	"github.com/pkg/errors"
)

func (mdm *mergeDepthManager) mergeDepthForCurrentBlockVersion() (uint64, error) {
	blockVersion := int(constants.GetBlockVersion())
	if blockVersion <= 0 {
		return 0, errors.Errorf("invalid block version %d", blockVersion)
	}
	if len(mdm.mergeDepth) == 0 {
		return 0, errors.New("merge depth configuration is empty")
	}
	index := blockVersion - 1
	if index >= len(mdm.mergeDepth) {
		log.Warnf("merge depth config has %d entries but current block version is %d; falling back to last entry", len(mdm.mergeDepth), blockVersion)
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
			return errors.Wrapf(ruleerrors.ErrViolatingBoundedMergeDepth, "block is violating bounded merge depth")
		}
	}

	return nil
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

	mergeDepth, err := mdm.mergeDepthForCurrentBlockVersion()
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
