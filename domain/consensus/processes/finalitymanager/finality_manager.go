package finalitymanager

import (
	"errors"

	"github.com/HoosatNetwork/HTND/domain/consensus/database"
	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/blockversion"
)

type finalityManager struct {
	databaseContext         model.DBReader
	dagTopologyManager      model.DAGTopologyManager
	finalityStore           model.FinalityStore
	ghostdagDataStore       model.GHOSTDAGDataStore
	pruningStore            model.PruningStore
	headersSelectedTipStore model.HeaderSelectedTipStore
	daaBlocksStore          model.DAABlocksStore
	genesisHash             *externalapi.DomainHash
	powScores               []uint64
	// finalityDepthForBlockVersion is evaluated with the chain's current block version on every use, never cached.
	finalityDepthForBlockVersion func(blockVersion uint16) uint64
}

// New instantiates a new FinalityManager
func New(databaseContext model.DBReader,
	dagTopologyManager model.DAGTopologyManager,
	finalityStore model.FinalityStore,
	ghostdagDataStore model.GHOSTDAGDataStore,
	pruningStore model.PruningStore,
	headersSelectedTipStore model.HeaderSelectedTipStore,
	daaBlocksStore model.DAABlocksStore,
	genesisHash *externalapi.DomainHash,
	powScores []uint64,
	finalityDepthForBlockVersion func(blockVersion uint16) uint64,
) model.FinalityManager {
	return &finalityManager{
		databaseContext:              databaseContext,
		genesisHash:                  genesisHash,
		dagTopologyManager:           dagTopologyManager,
		finalityStore:                finalityStore,
		ghostdagDataStore:            ghostdagDataStore,
		pruningStore:                 pruningStore,
		headersSelectedTipStore:      headersSelectedTipStore,
		daaBlocksStore:               daaBlocksStore,
		powScores:                    powScores,
		finalityDepthForBlockVersion: finalityDepthForBlockVersion,
	}
}

// finalityDepth returns the finality depth for the chain's current block version.
func (fm *finalityManager) finalityDepth(stagingArea *model.StagingArea) (uint64, error) {
	blockVersion, err := blockversion.Current(fm.databaseContext, stagingArea, fm.ghostdagDataStore,
		fm.headersSelectedTipStore, fm.daaBlocksStore, fm.powScores)
	if err != nil {
		return 0, err
	}
	return fm.finalityDepthForBlockVersion(blockVersion), nil
}

func (fm *finalityManager) VirtualFinalityPoint(stagingArea *model.StagingArea) (*externalapi.DomainHash, error) {
	log.Tracef("virtualFinalityPoint start")
	defer log.Tracef("virtualFinalityPoint end")

	virtualFinalityPoint, err := fm.calculateFinalityPoint(stagingArea, model.VirtualBlockHash, false)
	if err != nil {
		return nil, err
	}
	log.Debugf("The current virtual finality block is: %s", virtualFinalityPoint)

	return virtualFinalityPoint, nil
}

func (fm *finalityManager) FinalityPoint(stagingArea *model.StagingArea, blockHash *externalapi.DomainHash, isBlockWithTrustedData bool) (*externalapi.DomainHash, error) {
	log.Tracef("FinalityPoint start")
	defer log.Tracef("FinalityPoint end")
	if blockHash.Equal(model.VirtualBlockHash) {
		return fm.VirtualFinalityPoint(stagingArea)
	}
	finalityPoint, err := fm.finalityStore.FinalityPoint(fm.databaseContext, stagingArea, blockHash)
	if err != nil {
		log.Debugf("%s finality point not found in store - calculating", blockHash)
		if errors.Is(err, database.ErrNotFound) {
			return fm.calculateAndStageFinalityPoint(stagingArea, blockHash, isBlockWithTrustedData)
		}
		return nil, err
	}
	return finalityPoint, nil
}

func (fm *finalityManager) calculateAndStageFinalityPoint(
	stagingArea *model.StagingArea, blockHash *externalapi.DomainHash, isBlockWithTrustedData bool,
) (*externalapi.DomainHash, error) {
	finalityPoint, err := fm.calculateFinalityPoint(stagingArea, blockHash, isBlockWithTrustedData)
	if err != nil {
		return nil, err
	}
	fm.finalityStore.StageFinalityPoint(stagingArea, blockHash, finalityPoint)
	return finalityPoint, nil
}

func (fm *finalityManager) calculateFinalityPoint(stagingArea *model.StagingArea, blockHash *externalapi.DomainHash, isBlockWithTrustedData bool) (
	*externalapi.DomainHash, error,
) {
	log.Tracef("calculateFinalityPoint start")
	defer log.Tracef("calculateFinalityPoint end")

	if isBlockWithTrustedData {
		return model.VirtualGenesisBlockHash, nil
	}

	ghostdagData, err := fm.ghostdagDataStore.Get(fm.databaseContext, stagingArea, blockHash, false)
	// if database.IsNotFoundError(err) {
	// log.Infof("calculateFinalityPoint failed to retrieve with %s\n", blockHash)
	// 	return nil, err
	// }
	if err != nil {
		return nil, err
	}

	finalityDepth, err := fm.finalityDepth(stagingArea)
	if err != nil {
		return nil, err
	}
	if ghostdagData.BlueScore() < finalityDepth {
		log.Debugf("%s blue score lower then finality depth - returning genesis as finality point", blockHash)
		return fm.genesisHash, nil
	}

	pruningPoint, err := fm.pruningStore.PruningPoint(fm.databaseContext, stagingArea)
	if err != nil {
		return nil, err
	}
	pruningPointGhostdagData, err := fm.ghostdagDataStore.Get(fm.databaseContext, stagingArea, pruningPoint, false)
	if database.IsNotFoundError(err) {
		log.Infof("calculateFinalityPoint failed to retrieve with %s\n", pruningPoint)
		return nil, err
	}
	if err != nil {
		return nil, err
	}
	if ghostdagData.BlueScore() < pruningPointGhostdagData.BlueScore()+finalityDepth {
		log.Debugf("%s blue score less than finality distance over pruning point - returning virtual genesis as finality point", blockHash)
		return model.VirtualGenesisBlockHash, nil
	}
	isPruningPointOnChain, err := fm.dagTopologyManager.IsInSelectedParentChainOf(stagingArea, pruningPoint, blockHash)
	if err != nil {
		return nil, err
	}
	if !isPruningPointOnChain {
		log.Debugf("pruning point not in selected chain of %s - returning virtual genesis as finality point", blockHash)
		return model.VirtualGenesisBlockHash, nil
	}

	selectedParent := ghostdagData.SelectedParent()
	if selectedParent.Equal(fm.genesisHash) {
		return fm.genesisHash, nil
	}

	current, err := fm.finalityStore.FinalityPoint(fm.databaseContext, stagingArea, ghostdagData.SelectedParent())
	if err != nil {
		return nil, err
	}
	// In this case we expect the pruning point or a block above it to be the finality point.
	// Note that above we already verified the chain and distance conditions for this
	if current.Equal(model.VirtualGenesisBlockHash) {
		current = pruningPoint
	}

	requiredBlueScore := ghostdagData.BlueScore() - finalityDepth
	log.Debugf("%s's finality point is the one having the highest blue score lower then %d", blockHash, requiredBlueScore)

	var next *externalapi.DomainHash
	for {
		next, err = fm.dagTopologyManager.ChildInSelectedParentChainOf(stagingArea, current, blockHash)
		if err != nil {
			return nil, err
		}
		nextGHOSTDAGData, err := fm.ghostdagDataStore.Get(fm.databaseContext, stagingArea, next, false)
		if err != nil {
			return nil, err
		}
		if nextGHOSTDAGData.BlueScore() >= requiredBlueScore {
			log.Debugf("%s's finality point is %s", blockHash, current)
			return current, nil
		}

		current = next
	}
}
