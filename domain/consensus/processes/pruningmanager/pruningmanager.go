package pruningmanager

import (
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/multiset"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/utxo"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/virtual"
	"github.com/HoosatNetwork/HTND/infrastructure/db/database"
	"github.com/HoosatNetwork/HTND/infrastructure/logger"
	"github.com/HoosatNetwork/HTND/util/staging"
	"github.com/pkg/errors"
)

// pruningManager resolves and manages the current pruning point
type pruningManager struct {
	databaseContext model.DBManager

	dagTraversalManager   model.DAGTraversalManager
	dagTopologyManager    model.DAGTopologyManager
	consensusStateManager model.ConsensusStateManager
	finalityManager       model.FinalityManager

	consensusStateStore                 model.ConsensusStateStore
	ghostdagDataStore                   model.GHOSTDAGDataStore
	pruningStore                        model.PruningStore
	blockStatusStore                    model.BlockStatusStore
	headerSelectedTipStore              model.HeaderSelectedTipStore
	blocksWithTrustedDataDAAWindowStore model.BlocksWithTrustedDataDAAWindowStore
	multiSetStore                       model.MultisetStore
	acceptanceDataStore                 model.AcceptanceDataStore
	blocksStore                         model.BlockStore
	blockHeaderStore                    model.BlockHeaderStore
	utxoDiffStore                       model.UTXODiffStore
	daaBlocksStore                      model.DAABlocksStore
	reachabilityDataStore               model.ReachabilityDataStore

	isArchivalNode                  bool
	genesisHash                     *externalapi.DomainHash
	finalityInterval                uint64
	pruningDepth                    uint64
	deletionDepth                   uint64
	dataRetentionDuration           time.Duration
	pruningInterval                 time.Duration
	shouldSanityCheckPruningUTXOSet bool
	k                               []externalapi.KType
	difficultyAdjustmentWindowSize  []int

	targetTimePerBlock []time.Duration

	lastPruningTime            time.Time
	cachedPruningPoint         *externalapi.DomainHash
	cachedPruningPointAnticone []*externalapi.DomainHash
}

// New instantiates a new PruningManager
func New(
	databaseContext model.DBManager,

	dagTraversalManager model.DAGTraversalManager,
	dagTopologyManager model.DAGTopologyManager,
	consensusStateManager model.ConsensusStateManager,
	finalityManager model.FinalityManager,

	consensusStateStore model.ConsensusStateStore,
	ghostdagDataStore model.GHOSTDAGDataStore,
	pruningStore model.PruningStore,
	blockStatusStore model.BlockStatusStore,
	headerSelectedTipStore model.HeaderSelectedTipStore,
	multiSetStore model.MultisetStore,
	acceptanceDataStore model.AcceptanceDataStore,
	blocksStore model.BlockStore,
	blockHeaderStore model.BlockHeaderStore,
	utxoDiffStore model.UTXODiffStore,
	daaBlocksStore model.DAABlocksStore,
	reachabilityDataStore model.ReachabilityDataStore,
	blocksWithTrustedDataDAAWindowStore model.BlocksWithTrustedDataDAAWindowStore,

	isArchivalNode bool,
	genesisHash *externalapi.DomainHash,
	finalityInterval uint64,
	pruningDepth uint64,
	deletionDepth uint64,
	dataRetentionDuration time.Duration,
	pruningInterval time.Duration,
	shouldSanityCheckPruningUTXOSet bool,
	k []externalapi.KType,
	difficultyAdjustmentWindowSize []int,
	targetTimePerBlock []time.Duration,
) model.PruningManager {
	pm := &pruningManager{
		databaseContext:       databaseContext,
		dagTraversalManager:   dagTraversalManager,
		dagTopologyManager:    dagTopologyManager,
		consensusStateManager: consensusStateManager,
		finalityManager:       finalityManager,

		consensusStateStore:                 consensusStateStore,
		ghostdagDataStore:                   ghostdagDataStore,
		pruningStore:                        pruningStore,
		blockStatusStore:                    blockStatusStore,
		multiSetStore:                       multiSetStore,
		acceptanceDataStore:                 acceptanceDataStore,
		blocksStore:                         blocksStore,
		blockHeaderStore:                    blockHeaderStore,
		utxoDiffStore:                       utxoDiffStore,
		headerSelectedTipStore:              headerSelectedTipStore,
		daaBlocksStore:                      daaBlocksStore,
		reachabilityDataStore:               reachabilityDataStore,
		blocksWithTrustedDataDAAWindowStore: blocksWithTrustedDataDAAWindowStore,

		isArchivalNode:                  isArchivalNode,
		genesisHash:                     genesisHash,
		pruningDepth:                    pruningDepth,
		deletionDepth:                   deletionDepth,
		dataRetentionDuration:           dataRetentionDuration,
		pruningInterval:                 pruningInterval,
		finalityInterval:                finalityInterval,
		shouldSanityCheckPruningUTXOSet: shouldSanityCheckPruningUTXOSet,
		k:                               k,
		difficultyAdjustmentWindowSize:  difficultyAdjustmentWindowSize,
		targetTimePerBlock:              targetTimePerBlock,
	}
	// Reload the durable timestamp at startup
	lastTime, err := pruningStore.LastPruningTime(databaseContext)
	if err == nil {
		pm.lastPruningTime = lastTime
	} else if !database.IsNotFoundError(err) {
		log.Errorf("Failed to load last pruning time: %s", err)
	}

	return pm
}

func (pm *pruningManager) UpdatePruningPointByVirtual(stagingArea *model.StagingArea) error {
	onEnd := logger.LogAndMeasureExecutionTime(log, "pruningManager.UpdatePruningPointByVirtual")
	defer onEnd()
	hasPruningPoint, err := pm.pruningStore.HasPruningPoint(pm.databaseContext, stagingArea)
	if err != nil {
		return err
	}

	if !hasPruningPoint {
		hasGenesis, err := pm.blocksStore.HasBlock(pm.databaseContext, stagingArea, pm.genesisHash)
		if err != nil {
			return err
		}

		if hasGenesis {
			err = pm.savePruningPoint(stagingArea, pm.genesisHash)
			if err != nil {
				return err
			}
		}

		// Pruning point should initially set manually on a pruned-headers node.
		return nil
	}

	virtualGHOSTDAGData, err := pm.ghostdagDataStore.Get(pm.databaseContext, stagingArea, model.VirtualBlockHash, false)
	if database.IsNotFoundError(err) {
		// Virtual GHOSTDAG data may not exist yet (e.g., after an aborted IBD stage or
		// in a staging consensus that hasn't initialized virtual). In such cases there
		// is nothing to update yet.
		log.Infof("UpdatePruningPointByVirtual skipped: virtual GHOSTDAG data not found (%s)", model.VirtualBlockHash)
		return nil
	}
	if err != nil {
		return err
	}

	selectedParent := virtualGHOSTDAGData.SelectedParent()
	if selectedParent == nil {
		log.Infof("UpdatePruningPointByVirtual skipped: virtual selected parent is nil")
		return nil
	}
	if selectedParent.Equal(pm.genesisHash) {
		return nil
	}
	if selectedParent.Equal(model.VirtualGenesisBlockHash) {
		return nil
	}

	status, err := pm.blockStatusStore.Get(pm.databaseContext, stagingArea, selectedParent)
	if err != nil {
		return err
	}
	if status != externalapi.StatusUTXOValid {
		return nil
	}

	newPruningPoint, newCandidate, err := pm.nextPruningPointAndCandidateByBlockHash(stagingArea, virtualGHOSTDAGData.SelectedParent(), nil)
	if err != nil {
		return err
	}

	currentCandidate, err := pm.pruningPointCandidate(stagingArea)
	if err != nil {
		return err
	}

	if !newCandidate.Equal(currentCandidate) {
		log.Debugf("Staged a new pruning candidate, old: %s, new: %s", currentCandidate, newCandidate)
		pm.pruningStore.StagePruningPointCandidate(stagingArea, newCandidate)
	}

	currentPruningPoint, err := pm.pruningStore.PruningPoint(pm.databaseContext, stagingArea)
	if err != nil {
		return err
	}

	if !newPruningPoint.Equal(currentPruningPoint) {
		if constants.GetBlockVersion() < 5 {
			currentPruningPointGHOSTDAGData, err := pm.ghostdagDataStore.Get(pm.databaseContext, stagingArea, currentPruningPoint, false)
			if err != nil {
				return err
			}

			newPruningPointGHOSTDAGData, err := pm.ghostdagDataStore.Get(pm.databaseContext, stagingArea, newPruningPoint, false)
			if err != nil {
				return err
			}
			if pm.finalityScore(newPruningPointGHOSTDAGData.BlueScore()) > pm.finalityScore(currentPruningPointGHOSTDAGData.BlueScore())+1 {
				return errors.Errorf("cannot advance pruning point by more than one finality interval at once")
			}
		}

		log.Infof("Moving pruning point from %s to %s", currentPruningPoint, newPruningPoint)
		err = pm.savePruningPoint(stagingArea, newPruningPoint)
		if err != nil {
			return err
		}
		log.Infof("Moving pruning point finished.")
	}

	return nil
}

type blockIteratorFromOneBlock struct {
	done, isClosed bool
	hash           *externalapi.DomainHash
}

func (b *blockIteratorFromOneBlock) First() bool {
	if b.isClosed {
		panic("Tried using a closed blockIteratorFromOneBlock")
	}

	b.done = false
	return true
}

func (b *blockIteratorFromOneBlock) Next() bool {
	if b.isClosed {
		panic("Tried using a closed blockIteratorFromOneBlock")
	}

	b.done = true
	return false
}

func (b *blockIteratorFromOneBlock) Get() (*externalapi.DomainHash, error) {
	if b.isClosed {
		panic("Tried using a closed blockIteratorFromOneBlock")
	}

	return b.hash, nil
}

func (b *blockIteratorFromOneBlock) Close() error {
	if b.isClosed {
		panic("Tried using a closed blockIteratorFromOneBlock")
	}

	b.isClosed = true
	return nil
}

func (pm *pruningManager) nextPruningPointAndCandidateByBlockHash(stagingArea *model.StagingArea,
	blockHash, suggestedLowHash *externalapi.DomainHash,
) (*externalapi.DomainHash, *externalapi.DomainHash, error) {
	onEnd := logger.LogAndMeasureExecutionTime(log, "pruningManager.nextPruningPointAndCandidateByBlockHash")
	defer onEnd()

	currentCandidate, err := pm.pruningPointCandidate(stagingArea)
	if err != nil {
		return nil, nil, err
	}

	lowHash := currentCandidate
	if suggestedLowHash != nil {
		isSuggestedLowHashInSelectedParentChainOfCurrentCandidate, err := pm.dagTopologyManager.IsInSelectedParentChainOf(stagingArea, suggestedLowHash, currentCandidate)
		if err != nil {
			return nil, nil, err
		}

		if !isSuggestedLowHashInSelectedParentChainOfCurrentCandidate {
			isCurrentCandidateInSelectedParentChainOfSuggestedLowHash, err := pm.dagTopologyManager.IsInSelectedParentChainOf(stagingArea, currentCandidate, suggestedLowHash)
			if err != nil {
				return nil, nil, err
			}

			if !isCurrentCandidateInSelectedParentChainOfSuggestedLowHash {
				panic(errors.Errorf("suggested low hash %s is not on the same selected chain as the pruning candidate %s", suggestedLowHash, currentCandidate))
			}
			lowHash = suggestedLowHash
		}
	}

	currentPruningPoint, err := pm.pruningStore.PruningPoint(pm.databaseContext, stagingArea)
	if err != nil {
		return nil, nil, err
	}

	ghostdagData, err := pm.ghostdagDataStore.Get(pm.databaseContext, stagingArea, blockHash, false)
	if database.IsNotFoundError(err) {
		log.Infof("nextPruningPointAndCandidateByBlockHash failed to retrieve with %s\n", blockHash)
		return nil, nil, err
	}
	if err != nil {
		return nil, nil, err
	}

	currentPruningPointGHOSTDAGData, err := pm.ghostdagDataStore.Get(pm.databaseContext, stagingArea, currentPruningPoint, false)
	if database.IsNotFoundError(err) {
		log.Infof("nextPruningPointAndCandidateByBlockHash failed to retrieve with %s\n", currentPruningPoint)
		return nil, nil, err
	}
	if err != nil {
		return nil, nil, err
	}

	// We iterate until the selected parent of the given block, in order to allow a situation where the given block hash
	// belongs to the virtual. This shouldn't change anything since the max blue score difference between a block and its
	// selected parent is K, and K << pm.pruningDepth.
	var iterator model.BlockIterator
	if blockHash.Equal(lowHash) {
		iterator = &blockIteratorFromOneBlock{hash: lowHash}
	} else {
		iterator, err = pm.dagTraversalManager.SelectedChildIterator(stagingArea, ghostdagData.SelectedParent(), lowHash, true)
		if err != nil {
			// Instead of erroring if SelectedChildIterator decides to crash because
			// low hash is not in the selected parent hash of the highhash- So we
			// use highhash as block iterator from one block, so that we don't
			// advance further and gracefully handle error.
			iterator = &blockIteratorFromOneBlock{hash: ghostdagData.SelectedParent()}
			// return nil, nil, err
		}
	}
	defer iterator.Close()

	// Finding the next pruning point candidate: look for the latest
	// selected child of the current candidate that is in depth of at
	// least pm.pruningDepth blocks from the virtual selected parent.
	//
	// Note: Sometimes the current candidate is less than pm.pruningDepth
	// from the virtual. This can happen only if the virtual blue score
	// got smaller, because virtual blue score is not guaranteed to always
	// increase (because sometimes a block with higher blue work can have
	// lower blue score).
	// In such cases we still keep the same candidate because it's guaranteed
	// that a block that was once in depth of pm.pruningDepth cannot be
	// reorged without causing a finality conflict first.
	newCandidate := currentCandidate

	newPruningPoint := currentPruningPoint
	newPruningPointGHOSTDAGData := currentPruningPointGHOSTDAGData
	for ok := iterator.First(); ok; ok = iterator.Next() {
		selectedChild, err := iterator.Get()
		if err != nil {
			return nil, nil, err
		}
		selectedChildGHOSTDAGData, err := pm.ghostdagDataStore.Get(pm.databaseContext, stagingArea, selectedChild, false)
		if err != nil {
			return nil, nil, err
		}
		// log.Infof("ghostdagData.BlueScore()-selectedChildGHOSTDAGData.BlueScore() %d < pm.pruningDepth %d", ghostdagData.BlueScore()-selectedChildGHOSTDAGData.BlueScore(), pm.pruningDepth)
		if ghostdagData.BlueScore()-selectedChildGHOSTDAGData.BlueScore() < pm.pruningDepth {
			break
		}

		newCandidate = selectedChild
		newCandidateGHOSTDAGData := selectedChildGHOSTDAGData

		// We move the pruning point every time the candidate's finality score is
		// bigger than the current pruning point finality score.
		// log.Infof("pm.finalityScore(newCandidateGHOSTDAGData.BlueScore()) %d > pm.finalityScore(newPruningPointGHOSTDAGData.BlueScore()) %d", pm.finalityScore(newCandidateGHOSTDAGData.BlueScore()), pm.finalityScore(newPruningPointGHOSTDAGData.BlueScore()))
		if pm.finalityScore(newCandidateGHOSTDAGData.BlueScore()) > pm.finalityScore(newPruningPointGHOSTDAGData.BlueScore()) {
			newPruningPoint = newCandidate
			newPruningPointGHOSTDAGData = newCandidateGHOSTDAGData
		}
	}

	return newPruningPoint, newCandidate, nil
}

func (pm *pruningManager) isInPruningFutureOrInVirtualPast(stagingArea *model.StagingArea, block *externalapi.DomainHash,
	pruningPoint *externalapi.DomainHash, virtualParents []*externalapi.DomainHash,
) (bool, error) {
	hasPruningPointInPast, err := pm.dagTopologyManager.IsAncestorOf(stagingArea, pruningPoint, block)
	if err != nil {
		return false, err
	}
	if hasPruningPointInPast {
		return true, nil
	}
	// Because virtual doesn't have reachability data, we need to check reachability
	// using it parents.
	isInVirtualPast, err := pm.dagTopologyManager.IsAncestorOfAny(stagingArea, block, virtualParents)
	if err != nil {
		return false, err
	}
	if isInVirtualPast {
		return true, nil
	}

	return false, nil
}

func (pm *pruningManager) deletePastBlocks(stagingArea *model.StagingArea, pruningPoint *externalapi.DomainHash) error {
	onEnd := logger.LogAndMeasureExecutionTime(log, "pruningManager.deletePastBlocks")
	defer onEnd()

	// Go over all pruningPoint.Past and pruningPoint.Anticone that's not in virtual.Past
	queue := pm.dagTraversalManager.NewDownHeap(stagingArea)
	virtualParents, err := pm.dagTopologyManager.Parents(stagingArea, model.VirtualBlockHash)
	if err != nil {
		return err
	}

	// Start queue with all tips that are below the pruning point (and on the way remove them from list of tips)
	prunedTips, err := pm.pruneTips(stagingArea, pruningPoint, virtualParents)
	if err != nil {
		return err
	}
	err = queue.PushSlice(prunedTips)
	if err != nil {
		return err
	}

	// Add pruningPoint.Parents to queue
	parents, err := pm.dagTopologyManager.Parents(stagingArea, pruningPoint)
	if err != nil {
		return err
	}

	if !virtual.ContainsOnlyVirtualGenesis(parents) {
		err = queue.PushSlice(parents)
		if err != nil {
			return err
		}
	}

	blocksToKeep, err := pm.calculateBlocksToKeep(stagingArea, pruningPoint)
	if err != nil {
		return err
	}
	err = pm.deleteBlocksDownward(stagingArea, queue, blocksToKeep)
	if err != nil {
		return err
	}

	return nil
}

func (pm *pruningManager) calculateBlocksToKeep(stagingArea *model.StagingArea,
	pruningPoint *externalapi.DomainHash,
) (map[externalapi.DomainHash]struct{}, error) {
	pruningPointAnticone, err := pm.dagTraversalManager.AnticoneFromVirtualPOV(stagingArea, pruningPoint)
	if err != nil {
		return nil, err
	}
	pruningPointAndItsAnticone := append([]*externalapi.DomainHash{}, pruningPointAnticone...)
	pruningPointAndItsAnticone = append(pruningPointAndItsAnticone, pruningPoint)
	blocksToKeep := make(map[externalapi.DomainHash]struct{})
	for _, blockHash := range pruningPointAndItsAnticone {
		blocksToKeep[*blockHash] = struct{}{}
		blockWindow, err := pm.dagTraversalManager.BlockWindow(stagingArea, blockHash, pm.difficultyAdjustmentWindowSize[constants.GetBlockVersion()-1])
		if err != nil {
			return nil, err
		}
		for _, windowBlockHash := range blockWindow {
			blocksToKeep[*windowBlockHash] = struct{}{}
		}
	}
	return blocksToKeep, nil
}

func (pm *pruningManager) deleteBlocksDownward(stagingArea *model.StagingArea,
	queue model.BlockHeap, blocksToKeep map[externalapi.DomainHash]struct{},
) error {
	visited := map[externalapi.DomainHash]struct{}{}
	// Prune everything in the queue including its past, unless it's in `blocksToKeep`
	for queue.Len() > 0 {
		current := queue.Pop()
		if _, ok := visited[*current]; ok {
			continue
		}
		visited[*current] = struct{}{}

		shouldAddParents := true
		if _, ok := blocksToKeep[*current]; !ok {
			alreadyPruned, err := pm.deleteBlock(stagingArea, current)
			if err != nil {
				return err
			}
			shouldAddParents = !alreadyPruned
		}

		if shouldAddParents {
			parents, err := pm.dagTopologyManager.Parents(stagingArea, current)
			if err != nil {
				return err
			}

			if !virtual.ContainsOnlyVirtualGenesis(parents) {
				err = queue.PushSlice(parents)
				if err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (pm *pruningManager) pruneTips(stagingArea *model.StagingArea, pruningPoint *externalapi.DomainHash,
	virtualParents []*externalapi.DomainHash,
) (prunedTips []*externalapi.DomainHash, err error) {
	dagTips, err := pm.consensusStateStore.Tips(stagingArea, pm.databaseContext)
	if err != nil {
		return nil, err
	}
	newTips := make([]*externalapi.DomainHash, 0, len(dagTips))
	for _, tip := range dagTips {
		isInPruningFutureOrInVirtualPast, err := pm.isInPruningFutureOrInVirtualPast(stagingArea, tip, pruningPoint, virtualParents)
		if err != nil {
			return nil, err
		}
		if !isInPruningFutureOrInVirtualPast {
			prunedTips = append(prunedTips, tip)
		} else {
			newTips = append(newTips, tip)
		}
	}

	// Never leave the node with zero tips.
	if len(newTips) == 0 {
		log.Warnf("pruneTips would leave zero tips; keeping pruning point %s as tip", pruningPoint)
		newTips = []*externalapi.DomainHash{pruningPoint}
		prunedTips = nil // do not delete the only remaining tip
	}

	pm.consensusStateStore.StageTips(stagingArea, newTips)
	return prunedTips, nil
}

func (pm *pruningManager) savePruningPoint(stagingArea *model.StagingArea, pruningPointHash *externalapi.DomainHash) error {
	onEnd := logger.LogAndMeasureExecutionTime(log, "pruningManager.savePruningPoint")
	defer onEnd()
	err := pm.pruningStore.StagePruningPoint(pm.databaseContext, stagingArea, pruningPointHash)
	if err != nil {
		return err
	}
	pm.pruningStore.StageStartUpdatingPruningPointUTXOSet(stagingArea)

	// Call UpdatePruningPointIfRequired immediately after setting the flag
	// Rather than after every single validate_and_insert_block
	err = pm.UpdatePruningPointIfRequired()
	if err != nil {
		return err
	}

	return nil
}

func (pm *pruningManager) deleteBlock(stagingArea *model.StagingArea, blockHash *externalapi.DomainHash) (
	alreadyPruned bool, err error,
) {
	status, err := pm.blockStatusStore.Get(pm.databaseContext, stagingArea, blockHash)
	if database.IsNotFoundError(err) {
		log.Infof("deleteBlock failed to retrieve with %s\n", blockHash)
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if status == externalapi.StatusHeaderOnly {
		return true, nil
	}

	pm.blockStatusStore.Stage(stagingArea, blockHash, externalapi.StatusHeaderOnly)
	if pm.isArchivalNode {
		return false, nil
	}

	pm.multiSetStore.Delete(stagingArea, blockHash)
	pm.acceptanceDataStore.Delete(stagingArea, blockHash)
	pm.blocksStore.Delete(stagingArea, blockHash)
	pm.utxoDiffStore.Delete(stagingArea, blockHash)
	pm.daaBlocksStore.Delete(stagingArea, blockHash)

	return false, nil
}

func (pm *pruningManager) IsValidPruningPoint(stagingArea *model.StagingArea, blockHash *externalapi.DomainHash) (bool, error) {
	if *pm.genesisHash == *blockHash {
		return true, nil
	}

	headersSelectedTip, err := pm.headerSelectedTipStore.HeadersSelectedTip(pm.databaseContext, stagingArea)
	if err != nil {
		return false, err
	}

	// A pruning point has to be in the selected chain of the headers selected tip.
	headersSelectedTipGHOSTDAGData, err := pm.ghostdagDataStore.Get(pm.databaseContext, stagingArea, headersSelectedTip, false)
	if database.IsNotFoundError(err) {
		log.Infof("IsValidPruningPoint failed to retrieve with %s\n", headersSelectedTip)
		return false, err
	}
	if err != nil {
		return false, err
	}

	isInSelectedParentChainOfHeadersSelectedTip, err := pm.dagTopologyManager.IsInSelectedParentChainOf(stagingArea, blockHash, headersSelectedTip)
	if err != nil {
		return false, err
	}

	if !isInSelectedParentChainOfHeadersSelectedTip {
		return false, nil
	}

	ghostdagData, err := pm.ghostdagDataStore.Get(pm.databaseContext, stagingArea, blockHash, false)
	if err != nil {
		return false, err
	}

	// A pruning point has to be at depth of at least pm.pruningDepth
	// For imported pruning points, we allow the depth to be at least pruningDepth - 1
	// to account for slight differences in chain structure during IBD
	if headersSelectedTipGHOSTDAGData.BlueScore()-ghostdagData.BlueScore() < pm.pruningDepth-1 {
		return false, nil
	}

	return true, nil
}

func (pm *pruningManager) ArePruningPointsViolatingFinality(stagingArea *model.StagingArea,
	pruningPoints []externalapi.BlockHeader,
) (bool, error) {
	virtualFinalityPoint, err := pm.finalityManager.VirtualFinalityPoint(stagingArea)
	if err != nil {
		return false, err
	}

	virtualFinalityPointFinalityPoint, err := pm.finalityManager.FinalityPoint(stagingArea, virtualFinalityPoint, false)
	if err != nil {
		return false, err
	}

	// We need to check if virtualFinalityPointFinalityPoint is in the selected chain of
	// the most recent known pruning point, so we iterate the pruning points from the most
	// recent one until we find a known pruning point.
	for _, pruningPoint := range slices.Backward(pruningPoints) {
		blockHash := consensushashing.HeaderHash(pruningPoint)
		exists, err := pm.blockStatusStore.Exists(pm.databaseContext, stagingArea, blockHash)
		if err != nil {
			return false, err
		}

		if !exists {
			continue
		}

		isInSelectedParentChainOfVirtualFinalityPointFinalityPoint, err := pm.dagTopologyManager.
			IsInSelectedParentChainOf(stagingArea, virtualFinalityPointFinalityPoint, blockHash)
		if err != nil {
			return false, err
		}

		return !isInSelectedParentChainOfVirtualFinalityPointFinalityPoint, nil
	}

	// If no pruning point is known, there's definitely a finality violation
	return true, nil
}

func (pm *pruningManager) ArePruningPointsInValidChain(stagingArea *model.StagingArea) (bool, error) {
	// Check that a pruning point exists (we may not use lastPruningPoint directly, but this validates the store)
	_, err := pm.pruningStore.PruningPoint(pm.databaseContext, stagingArea)
	if err != nil {
		log.Errorf("pm.pruningStore.PruningPoint(pm.databaseContext, stagingArea): %s", err)
		return false, err
	}

	expectedPruningPoints := make([]*externalapi.DomainHash, 0)
	headersSelectedTip, err := pm.headerSelectedTipStore.HeadersSelectedTip(pm.databaseContext, stagingArea)
	if err != nil {
		log.Errorf("pm.headerSelectedTipStore.HeadersSelectedTip(pm.databaseContext, stagingArea): %s", err)
		return false, err
	}

	// Build the list of expected pruning points from selected tip back through the chain
	// We need to collect all distinct pruning points in the correct order to match against the stored list
	// The expected list should be in the same order as the stored list (from oldest to newest)
	// but we're walking backwards, so we'll reverse at the end
	current := headersSelectedTip
	reachedGenesis := false
	for {
		// Skip virtual blocks as they don't have headers
		if current.Equal(model.VirtualBlockHash) {
			break
		}
		header, err := pm.blockHeaderStore.BlockHeader(pm.databaseContext, stagingArea, current)
		if err != nil {
			log.Errorf("pm.blockHeaderStore.BlockHeader(pm.databaseContext, stagingArea, current): %s", err)
			return false, err
		}

		// Collect pruning points - we'll reverse the list later
		if len(expectedPruningPoints) == 0 || !expectedPruningPoints[len(expectedPruningPoints)-1].Equal(header.PruningPoint()) {
			expectedPruningPoints = append(expectedPruningPoints, header.PruningPoint())
		}

		if current.Equal(pm.genesisHash) {
			reachedGenesis = true
			break
		}

		currentGHOSTDAGData, err := pm.ghostdagDataStore.Get(pm.databaseContext, stagingArea, current, false)
		if database.IsNotFoundError(err) {
			log.Infof("ArePruningPointsInValidChain failed to retrieve with %s\n", current)
			return false, err
		}
		if err != nil {
			log.Errorf("pm.ghostdagDataStore.Get(pm.databaseContext, stagingArea, current): %s", err)
			return false, err
		}

		current = currentGHOSTDAGData.SelectedParent()
	}

	// If we reached genesis, ensure it's in the expected list
	if reachedGenesis && (len(expectedPruningPoints) == 0 || !expectedPruningPoints[len(expectedPruningPoints)-1].Equal(pm.genesisHash)) {
		expectedPruningPoints = append(expectedPruningPoints, pm.genesisHash)
	} else if !reachedGenesis && len(expectedPruningPoints) == 0 {
		// If we didn't reach genesis and have no expected pruning points,
		// this is likely a pruned node - we can't validate the full chain
		log.Warn("ArePruningPointsInValidChain: chain does not reach genesis, cannot fully validate")
		return false, nil
	}

	// Reverse the expected list so it's in order from genesis to current
	// (same order as stored pruning points)
	for i, j := 0, len(expectedPruningPoints)-1; i < j; i, j = i+1, j-1 {
		expectedPruningPoints[i], expectedPruningPoints[j] = expectedPruningPoints[j], expectedPruningPoints[i]
	}

	if len(expectedPruningPoints) == 0 {
		log.Errorf("Expected pruning points list is empty, can't match against stored pruning points")
		return false, errors.New("Expected pruning points list is empty, can't match against stored pruning points")
	}

	// Validate stored pruning points against expected pruning points
	lastPruningPointIndex, err := pm.pruningStore.CurrentPruningPointIndex(pm.databaseContext, stagingArea)
	if err != nil {
		log.Errorf("pm.pruningStore.CurrentPruningPointIndex(pm.databaseContext, stagingArea): %s", err)
		return false, err
	}

	// Now compare stored pruning points with expected pruning points
	// Both lists should be in order from genesis (index 0) to current (index lastPruningPointIndex)
	// Validate min of the two lengths to handle pruned nodes
	numToValidate := int(lastPruningPointIndex) + 1
	if len(expectedPruningPoints) < numToValidate {
		numToValidate = len(expectedPruningPoints)
		log.Warnf("ArePruningPointsInValidChain: chain only has %d pruning points but store has %d, validating %d", len(expectedPruningPoints), lastPruningPointIndex+1, numToValidate)
	}

	for i := uint64(0); i < uint64(numToValidate); i++ {
		pruningPoint, err := pm.pruningStore.PruningPointByIndex(pm.databaseContext, stagingArea, i)
		if err != nil {
			log.Errorf("pm.pruningStore.PruningPointByIndex(pm.databaseContext, stagingArea, %d): %s", i, err)
			return false, err
		}

		if int(i) >= len(expectedPruningPoints) {
			log.Warnf("ArePruningPointsInValidChain: no more expected pruning points at index %d", i)
			break
		}

		expectedPruningPoint := expectedPruningPoints[i]
		if !pruningPoint.Equal(expectedPruningPoint) {
			log.Errorf("Pruning point %s is not expected pruning point %s at index %d", pruningPoint.String(), expectedPruningPoint.String(), i)
			return false, errors.New("Pruning point is not expected pruning point at index")
		}
	}

	return true, nil
}

func (pm *pruningManager) pruningPointCandidate(stagingArea *model.StagingArea) (*externalapi.DomainHash, error) {
	hasPruningPointCandidate, err := pm.pruningStore.HasPruningPointCandidate(pm.databaseContext, stagingArea)
	if err != nil {
		return nil, err
	}

	if !hasPruningPointCandidate {
		return pm.genesisHash, nil
	}

	return pm.pruningStore.PruningPointCandidate(pm.databaseContext, stagingArea)
}

// validateUTXOSetFitsCommitment makes sure that the calculated UTXOSet of the new pruning point fits the commitment.
// This is a sanity test, to make sure that htnd doesn't store, and subsequently sends syncing peers the wrong UTXOSet.
func (pm *pruningManager) validateUTXOSetFitsCommitment(stagingArea *model.StagingArea, pruningPointHash *externalapi.DomainHash) error {
	onEnd := logger.LogAndMeasureExecutionTime(log, "pruningManager.validateUTXOSetFitsCommitment")
	defer onEnd()

	utxoSetIterator, err := pm.pruningStore.PruningPointUTXOIterator(pm.databaseContext)
	if err != nil {
		return err
	}
	defer utxoSetIterator.Close()

	utxoSetMultiset := multiset.New()
	for ok := utxoSetIterator.First(); ok; ok = utxoSetIterator.Next() {
		outpoint, entry, err := utxoSetIterator.Get()
		if err != nil {
			return err
		}
		serializedUTXO, err := utxo.SerializeUTXO(entry, outpoint)
		if err != nil {
			return err
		}
		utxoSetMultiset.Add(serializedUTXO)
	}
	utxoSetHash := utxoSetMultiset.Hash()

	header, err := pm.blockHeaderStore.BlockHeader(pm.databaseContext, stagingArea, pruningPointHash)
	if err != nil {
		return err
	}
	expectedUTXOCommitment := header.UTXOCommitment()

	if !expectedUTXOCommitment.Equal(utxoSetHash) {
		return errors.Errorf("Calculated UTXOSet for next pruning point %s doesn't match it's UTXO commitment\n"+
			"Calculated UTXOSet hash: %s. Commitment: %s",
			pruningPointHash, utxoSetHash, expectedUTXOCommitment)
	}

	log.Debugf("Validated the pruning point %s UTXO commitment: %s", pruningPointHash, utxoSetHash)

	return nil
}

// This function takes 2 points (currentPruningHash, previousPruningHash) and traverses the UTXO diff children DAG
// until it finds a common descendant, at the worse case this descendant will be the current SelectedTip.
// it then creates 2 diffs, one from that descendant to previousPruningHash and another from that descendant to currentPruningHash
// then using `DiffFrom` it converts these 2 diffs to a single diff from previousPruningHash to currentPruningHash.
// this way should be the fastest way to get the difference between the 2 points, and should perform much better than restoring the full UTXO set.
func (pm *pruningManager) calculateDiffBetweenPreviousAndCurrentPruningPoints(stagingArea *model.StagingArea, currentPruningHash *externalapi.DomainHash) (externalapi.UTXODiff, error) {
	onEnd := logger.LogAndMeasureExecutionTime(log, "pruningManager.calculateDiffBetweenPreviousAndCurrentPruningPoints")
	defer onEnd()
	if currentPruningHash.Equal(pm.genesisHash) {
		log.Infof("Current pruning point hash is equal to genesis")
		iter, err := pm.consensusStateManager.RestorePastUTXOSetIterator(stagingArea, currentPruningHash)
		if err != nil {
			return nil, err
		}
		set := make(map[externalapi.DomainOutpoint]externalapi.UTXOEntry)
		for ok := iter.First(); ok; ok = iter.Next() {
			outpoint, entry, err := iter.Get()
			if database.IsNotFoundError(err) {
				log.Infof("calculateDiffBetweenPreviousAndCurrentPruningPoints failed to retrieve\n")
				return nil, err
			}
			if err != nil {
				return nil, err
			}
			set[*outpoint] = entry
		}
		return utxo.NewUTXODiffFromCollections(utxo.NewUTXOCollection(set), utxo.NewUTXOCollection(make(map[externalapi.DomainOutpoint]externalapi.UTXOEntry)))
	}

	pruningPointIndex, err := pm.pruningStore.CurrentPruningPointIndex(pm.databaseContext, stagingArea)
	if err != nil {
		return nil, err
	}

	if pruningPointIndex == 0 {
		return nil, errors.Errorf("previous pruning point doesn't exist")
	}

	previousPruningHash, err := pm.pruningStore.PruningPointByIndex(pm.databaseContext, stagingArea, pruningPointIndex-1)
	if err != nil {
		return nil, err
	}
	currentPruningGhostDAG, err := pm.ghostdagDataStore.Get(pm.databaseContext, stagingArea, currentPruningHash, false)
	if err != nil {
		return nil, err
	}
	previousPruningGhostDAG, err := pm.ghostdagDataStore.Get(pm.databaseContext, stagingArea, previousPruningHash, false)
	if err != nil {
		return nil, err
	}

	currentPruningCurrentDiffChild := currentPruningHash
	previousPruningCurrentDiffChild := previousPruningHash
	// We need to use BlueWork because it's the only thing that's monotonic in the whole DAG
	// We use the BlueWork to know which point is currently lower on the DAG so we can keep climbing its children,
	// that way we keep climbing on the lowest point until they both reach the exact same descendant
	currentPruningCurrentDiffChildBlueWork := currentPruningGhostDAG.BlueWork()
	previousPruningCurrentDiffChildBlueWork := previousPruningGhostDAG.BlueWork()

	var diffHashesFromPrevious []*externalapi.DomainHash
	var diffHashesFromCurrent []*externalapi.DomainHash

diffTraversalLoop:
	for {
		// if currentPruningCurrentDiffChildBlueWork > previousPruningCurrentDiffChildBlueWork
		switch {
		case currentPruningCurrentDiffChildBlueWork.Cmp(previousPruningCurrentDiffChildBlueWork) == 1:
			diffHashesFromPrevious = append(diffHashesFromPrevious, previousPruningCurrentDiffChild)
			previousPruningCurrentDiffChild, err = pm.utxoDiffStore.UTXODiffChild(pm.databaseContext, stagingArea, previousPruningCurrentDiffChild)
			if err != nil {
				return nil, err
			}
			diffChildGhostDag, err := pm.ghostdagDataStore.Get(pm.databaseContext, stagingArea, previousPruningCurrentDiffChild, false)
			if err != nil {
				return nil, err
			}
			previousPruningCurrentDiffChildBlueWork = diffChildGhostDag.BlueWork()
		case currentPruningCurrentDiffChild.Equal(previousPruningCurrentDiffChild):
			break diffTraversalLoop
		default:
			diffHashesFromCurrent = append(diffHashesFromCurrent, currentPruningCurrentDiffChild)
			currentPruningCurrentDiffChild, err = pm.utxoDiffStore.UTXODiffChild(pm.databaseContext, stagingArea, currentPruningCurrentDiffChild)
			if err != nil {
				return nil, err
			}
			diffChildGhostDag, err := pm.ghostdagDataStore.Get(pm.databaseContext, stagingArea, currentPruningCurrentDiffChild, false)
			if err != nil {
				return nil, err
			}
			currentPruningCurrentDiffChildBlueWork = diffChildGhostDag.BlueWork()
		}
	}
	// The order in which we apply the diffs should be from top to bottom, but we traversed from bottom to top
	// so we apply the diffs in reverse order.

	oldDiff := utxo.NewMutableUTXODiff()
	log.Infof("Diff hashes from previous %d", len(diffHashesFromPrevious))
	for _, diffHashesFromPreviou := range slices.Backward(diffHashesFromPrevious) {
		utxoDiff, err := pm.utxoDiffStore.UTXODiff(pm.databaseContext, stagingArea, diffHashesFromPreviou)
		if err != nil {
			return nil, err
		}
		err = oldDiff.WithDiffInPlace(utxoDiff)
		if err != nil {
			return nil, err
		}
	}
	newDiff := utxo.NewMutableUTXODiff()
	log.Infof("Diff hashes from current %d", len(diffHashesFromCurrent))
	for _, d := range slices.Backward(diffHashesFromCurrent) {
		utxoDiff, err := pm.utxoDiffStore.UTXODiff(pm.databaseContext, stagingArea, d)
		if err != nil {
			return nil, err
		}
		err = newDiff.WithDiffInPlace(utxoDiff)
		if err != nil {
			return nil, err
		}
	}
	result, err := oldDiff.DiffFrom(newDiff.ToImmutable())
	if err != nil {
		panic(fmt.Sprintf("DiffFrom error for pruning points (previous: %s, current: %s): %s", previousPruningHash, currentPruningHash, err))
	}
	return result, nil
}

// This function takes 2 chain blocks (currentPruningHash, previousPruningHash) and finds
// the UTXO diff between them by iterating over acceptance data of the chain blocks in between.
func (pm *pruningManager) calculateDiffBetweenPreviousAndCurrentPruningPointsUsingAcceptanceData(stagingArea *model.StagingArea, currentPruningHash *externalapi.DomainHash) (externalapi.UTXODiff, error) {
	onEnd := logger.LogAndMeasureExecutionTime(log, "pruningManager.calculateDiffBetweenPreviousAndCurrentPruningPoints__UsingAcceptanceData")
	defer onEnd()
	if currentPruningHash.Equal(pm.genesisHash) {
		iter, err := pm.consensusStateManager.RestorePastUTXOSetIterator(stagingArea, currentPruningHash)
		if err != nil {
			return nil, err
		}
		set := make(map[externalapi.DomainOutpoint]externalapi.UTXOEntry)
		for ok := iter.First(); ok; ok = iter.Next() {
			outpoint, entry, err := iter.Get()
			if err != nil {
				return nil, err
			}
			set[*outpoint] = entry
		}
		return utxo.NewUTXODiffFromCollections(utxo.NewUTXOCollection(set), utxo.NewUTXOCollection(make(map[externalapi.DomainOutpoint]externalapi.UTXOEntry)))
	}

	pruningPointIndex, err := pm.pruningStore.CurrentPruningPointIndex(pm.databaseContext, stagingArea)
	if err != nil {
		return nil, err
	}

	if pruningPointIndex == 0 {
		return nil, errors.Errorf("previous pruning point doesn't exist")
	}

	previousPruningHash, err := pm.pruningStore.PruningPointByIndex(pm.databaseContext, stagingArea, pruningPointIndex-1)
	if err != nil {
		return nil, err
	}

	utxoDiff := utxo.NewMutableUTXODiff()

	iterator, err := pm.dagTraversalManager.SelectedChildIterator(stagingArea, currentPruningHash, previousPruningHash, false)
	if err != nil {
		return nil, err
	}
	defer iterator.Close()

	for ok := iterator.First(); ok; ok = iterator.Next() {
		child, err := iterator.Get()
		if err != nil {
			return nil, err
		}
		chainBlockAcceptanceData, err := pm.acceptanceDataStore.Get(pm.databaseContext, stagingArea, child)
		if database.IsNotFoundError(err) {
			log.Infof("calculateDiffBetweenPreviousAndCurrentPruningPointsUsingAcceptanceData failed to retrieve with %s\n", child)
			return nil, err
		}
		if err != nil {
			return nil, err
		}
		// chainBlockAcceptanceData holds one BlockAcceptanceData entry per merge-set block that `child`
		// accepted (see applyMergeSetBlocks), each carrying its OWN BlockHash - not just child's. Every
		// transaction in a given entry must be stamped with that entry's own creating block's DAA score,
		// not child's (chainBlockHeader.DAAScore() was being applied to every merge-set block's
		// transactions uniformly - the same wrong-DAA-score-stamping bug fixed in 96efc0d3d for
		// calculate_past_utxo.go's applyMergeSetBlocks/maybeAcceptTransaction, and in calculateMultiset/
		// addTransactionToMultiset in multisets.go, independently present here too). This is what was
		// producing the "pruning point diff verification (acceptance-data) FAILED" errors: the
		// acceptance-data-replay diff didn't reproduce the header's UTXO commitment because entries in
		// it carried the wrong BlockDAAScore.
		for _, blockAcceptanceData := range chainBlockAcceptanceData {
			creatingBlockHeader, err := pm.blockHeaderStore.BlockHeader(pm.databaseContext, stagingArea, blockAcceptanceData.BlockHash)
			var creatingBlockDAAScore uint64
			if err != nil {
				creatingBlockDAAScore, err = pm.daaBlocksStore.DAAScore(pm.databaseContext, stagingArea, blockAcceptanceData.BlockHash)
				if err != nil {
					return nil, err
				}
			} else {
				creatingBlockDAAScore = creatingBlockHeader.DAAScore()
			}
			for _, transactionAcceptanceData := range blockAcceptanceData.TransactionAcceptanceData {
				if transactionAcceptanceData.IsAccepted {
					err = utxoDiff.AddTransaction(transactionAcceptanceData.Transaction, creatingBlockDAAScore)
					if err != nil {
						return nil, err
					}
				}
			}
		}
	}

	return utxoDiff.ToImmutable(), err
}

// finalityScore is the number of finality intervals passed since
// the given block.
func (pm *pruningManager) finalityScore(blueScore uint64) uint64 {
	if pm.finalityInterval == 0 {
		return 0
	}
	return blueScore / pm.finalityInterval
}

// FindAndReproduceRootDisqualification walks back from a current DAG tip via SelectedParent looking
// for the root disqualification - the first block whose own status is StatusDisqualifiedFromChain
// while its selected parent's status is not. Once cascading disqualification has happened and been
// persisted to blockStatusStore, every block resolved on top of it (including in a completely fresh
// IBD run) goes through ResolveBlockStatus's cascade branch, which never re-runs verifyUTXO or fires
// its diagnostics - so the original failure can go completely silent in every subsequent run unless
// this root is found and re-resolved directly. Non-fatal: only logs, never blocks startup.
func (pm *pruningManager) FindAndReproduceRootDisqualification(stagingArea *model.StagingArea) {
	tips, err := pm.consensusStateStore.Tips(stagingArea, pm.databaseContext)
	if err != nil {
		log.Debugf("[UTXO-DEBUG] FindAndReproduceRootDisqualification: could not fetch tips: %s", err)
		return
	}
	if len(tips) == 0 {
		log.Debugf("[UTXO-DEBUG] FindAndReproduceRootDisqualification: no tips found")
		return
	}

	current := tips[0]
	const maxWalk = 1_000_000
	for i := 0; i < maxWalk; i++ {
		status, err := pm.blockStatusStore.Get(pm.databaseContext, stagingArea, current)
		if err != nil {
			log.Debugf("[UTXO-DEBUG] FindAndReproduceRootDisqualification: could not fetch status for %s: %s", current, err)
			return
		}
		if status != externalapi.StatusDisqualifiedFromChain {
			log.Debugf("[UTXO-DEBUG] FindAndReproduceRootDisqualification: walked back %d blocks from tip %s "+
				"without finding any disqualified block (reached %s with status %s) - nothing to reproduce.",
				i, tips[0], current, status)
			return
		}

		ghostdagData, err := pm.ghostdagDataStore.Get(pm.databaseContext, stagingArea, current, false)
		if err != nil {
			log.Debugf("[UTXO-DEBUG] FindAndReproduceRootDisqualification: could not fetch GHOSTDAG data for %s: %s", current, err)
			return
		}
		parent := ghostdagData.SelectedParent()
		if parent == nil {
			log.Debugf("[UTXO-DEBUG] FindAndReproduceRootDisqualification: reached the end of the chain at %s "+
				"(still disqualified) without finding a healthy parent.", current)
			return
		}
		parentStatus, err := pm.blockStatusStore.Get(pm.databaseContext, stagingArea, parent)
		if err != nil {
			log.Debugf("[UTXO-DEBUG] FindAndReproduceRootDisqualification: could not fetch status for %s: %s", parent, err)
			return
		}
		if parentStatus != externalapi.StatusDisqualifiedFromChain {
			// The walk itself is cheap (status lookups only) and always runs to confirm the root
			// hasn't moved, but ReproduceDisqualification (restorePastUTXO + full resolution) is
			// expensive - skip it if this is the same root already reproduced on a previous boot.
			if lastRoot, lastRootErr := pm.pruningStore.LastUTXODebugReproducedRootHash(pm.databaseContext); lastRootErr == nil && lastRoot.Equal(current) {
				log.Debugf("[UTXO-DEBUG] FindAndReproduceRootDisqualification: root disqualified block %s was "+
					"already reproduced on a previous boot and hasn't changed - skipping.", current)
				return
			}
			log.Debugf("[UTXO-DEBUG] FindAndReproduceRootDisqualification: found root disqualified block %s "+
				"(selected parent %s has status %s) after walking back %d blocks from tip %s. Reproducing...",
				current, parent, parentStatus, i, tips[0])
			if err := pm.consensusStateManager.ReproduceDisqualification(current, parent); err != nil {
				log.Errorf("[UTXO-DEBUG] FindAndReproduceRootDisqualification: ReproduceDisqualification failed: %s", err)
				return
			}
			if setErr := pm.pruningStore.SetLastUTXODebugReproducedRootHash(pm.databaseContext, current); setErr != nil {
				log.Debugf("[UTXO-DEBUG] FindAndReproduceRootDisqualification: could not persist last-reproduced marker for %s: %s",
					current, setErr)
			}
			return
		}
		current = parent
	}
	log.Debugf("[UTXO-DEBUG] FindAndReproduceRootDisqualification: walked %d blocks without finding the root "+
		"- giving up.", maxWalk)
}

// VerifyCurrentPruningPointUTXOSet is a diagnostic, non-fatal check: it iterates this node's
// ENTIRE current pruningPointUTXOSetBucket from scratch and compares the resulting multiset
// against the current pruning point's own header UTXOCommitment. Unlike
// shouldSanityCheckPruningUTXOSet (which only ever runs as a side effect of the pruning point
// advancing AGAIN, and only when that hidden flag is enabled), this checks the CURRENTLY
// already-computed and already-served bucket immediately, on demand - e.g. once at startup - so a
// node's existing state can be confirmed or ruled out without waiting for its next pruning point
// movement.
//
// It also pulls in multiSetStore.Get(pruningPoint) - the PER-BLOCK multiset, built via the
// completely separate calculateMultiset/resolveSingleBlockStatus code path used for every normal
// block, not the pruning-point-bucket machinery - so a bucket mismatch can be localized: if the
// per-block multiset matches the header but the bucket doesn't, the bug is specifically in how the
// bucket gets derived/updated (updatePruningPoint), not in general per-block resolution.
//
// Always logs and never returns an error, so it can never block startup or any other caller.
//
// Persists the checked pruning point (LastUTXODebugCheckedPruningPoint) and skips re-running
// entirely if it's unchanged since the last boot - this scan is expensive (a full bucket iteration
// plus, on failure, further multi-minute passes), and produces the same result on every boot until
// the pruning point itself actually advances.
func (pm *pruningManager) VerifyCurrentPruningPointUTXOSet() {
	stagingArea := model.NewStagingArea()
	pruningPoint, err := pm.pruningStore.PruningPoint(pm.databaseContext, stagingArea)
	if err != nil {
		log.Debugf("[UTXO-DEBUG] VerifyCurrentPruningPointUTXOSet: could not fetch current pruning point: %s", err)
		return
	}
	if pruningPoint.Equal(pm.genesisHash) {
		log.Debugf("[UTXO-DEBUG] VerifyCurrentPruningPointUTXOSet: current pruning point is genesis, skipping")
		return
	}
	if lastChecked, lastCheckedErr := pm.pruningStore.LastUTXODebugCheckedPruningPoint(pm.databaseContext); lastCheckedErr == nil && lastChecked.Equal(pruningPoint) {
		log.Debugf("[UTXO-DEBUG] VerifyCurrentPruningPointUTXOSet: %s was already checked on a previous "+
			"boot and hasn't changed - skipping.", pruningPoint)
		return
	}
	// Only persisted once a real verdict is actually reached (reachedVerdict set just before the
	// switch below) - an early technical failure (couldn't fetch header/iterator/etc.) should be
	// retried next boot, not remembered as "checked".
	reachedVerdict := false
	defer func() {
		if !reachedVerdict {
			return
		}
		if setErr := pm.pruningStore.SetLastUTXODebugCheckedPruningPoint(pm.databaseContext, pruningPoint); setErr != nil {
			log.Debugf("[UTXO-DEBUG] VerifyCurrentPruningPointUTXOSet: could not persist last-checked marker for %s: %s",
				pruningPoint, setErr)
		}
	}()

	header, err := pm.blockHeaderStore.BlockHeader(pm.databaseContext, stagingArea, pruningPoint)
	if err != nil {
		log.Debugf("[UTXO-DEBUG] VerifyCurrentPruningPointUTXOSet: could not fetch header for %s: %s", pruningPoint, err)
		return
	}
	expectedCommitment := header.UTXOCommitment()

	utxoSetIterator, err := pm.pruningStore.PruningPointUTXOIterator(pm.databaseContext)
	if err != nil {
		log.Debugf("[UTXO-DEBUG] VerifyCurrentPruningPointUTXOSet: could not get bucket iterator: %s", err)
		return
	}
	defer utxoSetIterator.Close()
	bucketMultiset := multiset.New()
	// Collected alongside the multiset so a repair (if needed) doesn't have to walk the bucket a
	// second time just to know what's currently in it.
	oldBucketEntries := make(map[externalapi.DomainOutpoint]externalapi.UTXOEntry)
	entryCount := 0
	for ok := utxoSetIterator.First(); ok; ok = utxoSetIterator.Next() {
		outpoint, entry, err := utxoSetIterator.Get()
		if err != nil {
			log.Debugf("[UTXO-DEBUG] VerifyCurrentPruningPointUTXOSet: bucket iterator.Get failed: %s", err)
			return
		}
		serialized, err := utxo.SerializeUTXO(entry, outpoint)
		if err != nil {
			log.Debugf("[UTXO-DEBUG] VerifyCurrentPruningPointUTXOSet: SerializeUTXO failed: %s", err)
			return
		}
		bucketMultiset.Add(serialized)
		oldBucketEntries[*outpoint] = entry
		entryCount++
	}
	bucketHash := bucketMultiset.Hash()

	perBlockMultiset, perBlockErr := pm.multiSetStore.Get(pm.databaseContext, stagingArea, pruningPoint)
	var perBlockHash *externalapi.DomainHash
	if perBlockErr != nil {
		log.Debugf("[UTXO-DEBUG] VerifyCurrentPruningPointUTXOSet: could not fetch per-block multiset for %s: %s",
			pruningPoint, perBlockErr)
	} else {
		perBlockHash = perBlockMultiset.Hash()
	}

	bucketMatchesHeader := bucketHash.Equal(expectedCommitment)
	perBlockMatchesHeader := perBlockHash != nil && perBlockHash.Equal(expectedCommitment)

	log.Debugf("[UTXO-DEBUG] VerifyCurrentPruningPointUTXOSet for %s: entries=%d | header expects=%s | "+
		"bucket-derived=%s (matchesHeader=%t) | per-block multiset=%s (matchesHeader=%t)",
		pruningPoint, entryCount, expectedCommitment, bucketHash, bucketMatchesHeader, perBlockHash, perBlockMatchesHeader)

	reachedVerdict = true
	switch {
	case bucketMatchesHeader:
		log.Debugf("[UTXO-DEBUG] VerifyCurrentPruningPointUTXOSet PASSED for %s: the served bucket matches "+
			"its own header commitment exactly.", pruningPoint)
	case perBlockHash == nil:
		log.Errorf("[UTXO-DEBUG] VerifyCurrentPruningPointUTXOSet FAILED for %s: bucket does not match header, "+
			"and the per-block multiset couldn't be fetched to localize further.", pruningPoint)
	case perBlockMatchesHeader:
		log.Errorf("[UTXO-DEBUG] VerifyCurrentPruningPointUTXOSet FAILED for %s: the PER-BLOCK multiset "+
			"(normal block resolution) MATCHES the header, but the served bucket does NOT. The bug is "+
			"specifically in the pruning-point-bucket derivation (updatePruningPoint / "+
			"UpdatePruningPointUTXOSet), not in general per-block resolution. Auto-repairing the bucket "+
			"from restorePastUTXO, the proven-correct source.", pruningPoint)
		pm.repairPruningPointUTXOSet(stagingArea, pruningPoint, oldBucketEntries, expectedCommitment)
	case perBlockHash.Equal(bucketHash):
		log.Errorf("[UTXO-DEBUG] VerifyCurrentPruningPointUTXOSet FAILED for %s: the bucket and the "+
			"per-block multiset AGREE with each other but NEITHER matches the header - the corruption is "+
			"upstream of both (e.g. baked into an ancestor block's already-wrong stored multiset), not in "+
			"either the bucket-derivation or per-block-resolution code specifically.", pruningPoint)
	default:
		log.Errorf("[UTXO-DEBUG] VerifyCurrentPruningPointUTXOSet FAILED for %s: bucket, per-block multiset, "+
			"and header all three disagree with each other.", pruningPoint)
	}
}

// checkHistoricalPruningPoints walks backward through this node's RECORDED pruning points (via
// CurrentPruningPointIndex/PruningPointByIndex) rather than the selected-parent chain -
// bisectRestorePastUTXODivergence's selected-parent walk can run into blocks whose bodies were
// never downloaded/validated (only their headers), a wall this mechanism doesn't hit since it only
// ever looks at hashes this node already recorded as an actual past pruning point. For each
// recorded pruning point that's still resolvable (StatusUTXOValid), runs the same three-way check
// as VerifyCurrentPruningPointUTXOSet (restorePastUTXO vs the per-block multiset vs the header).
// Used as a fallback when the selected-parent-chain bisection can't be extended further locally -
// this instead tests each trust-import boundary itself, to see whether the drift predates even the
// earliest import this node still has full data for.
func (pm *pruningManager) checkHistoricalPruningPoints(stagingArea *model.StagingArea) {
	belowIndex, err := pm.pruningStore.CurrentPruningPointIndex(pm.databaseContext, stagingArea)
	if err != nil {
		log.Errorf("[UTXO-DEBUG] checkHistoricalPruningPoints: could not fetch current pruning point index: %s", err)
		return
	}
	log.Debugf("[UTXO-DEBUG] checkHistoricalPruningPoints: walking recorded pruning points below index %d", belowIndex)

	// examined bounds total loop iterations regardless of resolvability - a long run of
	// HeaderOnly/not-found entries (expected: pruning-point advancement and actual body deletion
	// are separate, deferrable steps, so deletion routinely lags behind and produces exactly this)
	// must not let the search run unbounded all the way to index 0. checked separately counts only
	// entries that were actually resolvable and got the real three-way comparison.
	const maxExamined = 200
	const maxChecked = 50
	examined := 0
	checked := 0
	unresolvableStreak := 0
	for idx := belowIndex; examined < maxExamined && checked < maxChecked && idx > 0; {
		idx--
		examined++

		hash, err := pm.pruningStore.PruningPointByIndex(pm.databaseContext, stagingArea, idx)
		if err != nil {
			log.Errorf("[UTXO-DEBUG] checkHistoricalPruningPoints: could not fetch pruning point at index %d: %s", idx, err)
			return
		}

		status, err := pm.blockStatusStore.Get(pm.databaseContext, stagingArea, hash)
		if err != nil || status != externalapi.StatusUTXOValid {
			unresolvableStreak++
			// Log only the first few and then periodically, instead of one line per index - a long
			// unresolvable streak (expected here) would otherwise produce hundreds of near-identical lines.
			if unresolvableStreak <= 3 || unresolvableStreak%50 == 0 {
				reason := "not resolvable"
				if err != nil {
					reason = "could not fetch status: " + err.Error()
				} else {
					reason = fmt.Sprintf("status=%s, not resolvable", status)
				}
				log.Debugf("[UTXO-DEBUG] checkHistoricalPruningPoints: index %d (%s): %s - skipping "+
					"(%d unresolvable in a row so far)", idx, hash, reason, unresolvableStreak)
			}
			continue
		}
		unresolvableStreak = 0
		checked++

		header, err := pm.blockHeaderStore.BlockHeader(pm.databaseContext, stagingArea, hash)
		if err != nil {
			log.Errorf("[UTXO-DEBUG] checkHistoricalPruningPoints: index %d (%s): could not fetch header: %s", idx, hash, err)
			continue
		}
		expectedCommitment := header.UTXOCommitment()

		perBlockMultiset, perBlockErr := pm.multiSetStore.Get(pm.databaseContext, stagingArea, hash)
		perBlockMatches := false
		if perBlockErr == nil {
			perBlockMatches = perBlockMultiset.Hash().Equal(expectedCommitment)
		}

		iterator, err := pm.consensusStateManager.RestorePastUTXOSetIterator(stagingArea, hash)
		if err != nil {
			log.Errorf("[UTXO-DEBUG] checkHistoricalPruningPoints: index %d (%s): RestorePastUTXOSetIterator failed: %s",
				idx, hash, err)
			continue
		}
		ms := multiset.New()
		var iterErr error
		for ok := iterator.First(); ok; ok = iterator.Next() {
			outpoint, entry, getErr := iterator.Get()
			if getErr != nil {
				iterErr = getErr
				break
			}
			serialized, serErr := utxo.SerializeUTXO(entry, outpoint)
			if serErr != nil {
				iterErr = serErr
				break
			}
			ms.Add(serialized)
		}
		iterator.Close()
		if iterErr != nil {
			log.Errorf("[UTXO-DEBUG] checkHistoricalPruningPoints: index %d (%s): iterator failed mid-walk: %s",
				idx, hash, iterErr)
			continue
		}

		restoreMatches := ms.Hash().Equal(expectedCommitment)
		log.Debugf("[UTXO-DEBUG] checkHistoricalPruningPoints: index %d (%s): header=%s | restorePastUTXO=%s "+
			"(matches=%t) | per-block multiset=%s (matches=%t)",
			idx, hash, expectedCommitment, ms.Hash(), restoreMatches, perBlockMultiset.Hash(), perBlockMatches)

		if restoreMatches {
			conclusion := fmt.Sprintf("[UTXO-DEBUG] checkHistoricalPruningPoints CONCLUSION: pruning point at "+
				"index %d (%s) is CLEAN (restorePastUTXO matches its own header) - this trust-import boundary "+
				"is correct. The drift entered somewhere AFTER this point but before the later unresolvable "+
				"wall - i.e. in a range this node no longer has full body data for, and can't be pinpointed "+
				"further without re-downloading blocks in that range.", idx, hash)
			log.Warnf("%s", conclusion)
			return
		}
		log.Errorf("[UTXO-DEBUG] checkHistoricalPruningPoints: index %d (%s) is ALSO wrong - the corruption "+
			"predates this trust-import boundary too. Continuing further back.", idx, hash)
	}
	conclusion := fmt.Sprintf("[UTXO-DEBUG] checkHistoricalPruningPoints CONCLUSION: examined %d indices, "+
		"actually checked %d resolvable ones (rest were HeaderOnly/not-found - likely already deleted by "+
		"normal pruning retention, not evidence they were never validated), without finding a clean one. "+
		"This node's local data can't verify any further back than this without re-downloading blocks. "+
		"Pivoting to check whether the underlying raw UTXO table itself is trustworthy.",
		examined, checked)
	log.Warnf("%s", conclusion)
	pm.verifyVirtualUTXOSetSelfConsistency(stagingArea)
}

// verifyVirtualUTXOSetSelfConsistency checks whether consensusStateStore's raw virtual UTXO table -
// the ultimate base that every restorePastUTXO call combines a diff on top of via IteratorWithDiff -
// is itself trustworthy, independent of any diff-walking. Virtual has no header/UTXOCommitment to
// compare against (it's a synthetic marker, not a mined block), but it DOES have its own entry in
// the same incremental multiset chain proven correct elsewhere (multiSetStore never has to merge
// competing branches - it just inherits its parent's already-settled multiset and adds to it, same
// as any other block). So this compares that stored multiset directly against a fresh hash of
// VirtualUTXOSetIterator's raw contents - no diff, no UTXODiffChild walk, nothing but the base table
// itself. If they agree, the raw table is proven correct and the entire corruption must live in how
// diffs get computed/walked/merged on top of it (restorePastUTXO, updatePruningPoint) - not in the
// underlying data. If they disagree, even the base table has drifted.
func (pm *pruningManager) verifyVirtualUTXOSetSelfConsistency(stagingArea *model.StagingArea) {
	log.Debugf("[UTXO-DEBUG] verifyVirtualUTXOSetSelfConsistency: checking consensusStateStore's raw virtual " +
		"UTXO table against its own stored multiset (no diff involved)...")

	storedMultiset, err := pm.multiSetStore.Get(pm.databaseContext, stagingArea, model.VirtualBlockHash)
	if err != nil {
		log.Errorf("[UTXO-DEBUG] verifyVirtualUTXOSetSelfConsistency: could not fetch stored multiset for virtual: %s", err)
		return
	}

	iterator, err := pm.consensusStateStore.VirtualUTXOSetIterator(pm.databaseContext, stagingArea)
	if err != nil {
		log.Errorf("[UTXO-DEBUG] verifyVirtualUTXOSetSelfConsistency: could not get virtual UTXO set iterator: %s", err)
		return
	}
	defer iterator.Close()

	fresh := multiset.New()
	entryCount := 0
	for ok := iterator.First(); ok; ok = iterator.Next() {
		outpoint, entry, err := iterator.Get()
		if err != nil {
			log.Errorf("[UTXO-DEBUG] verifyVirtualUTXOSetSelfConsistency: iterator.Get failed: %s", err)
			return
		}
		serialized, err := utxo.SerializeUTXO(entry, outpoint)
		if err != nil {
			log.Errorf("[UTXO-DEBUG] verifyVirtualUTXOSetSelfConsistency: SerializeUTXO failed: %s", err)
			return
		}
		fresh.Add(serialized)
		entryCount++
	}

	freshHash := fresh.Hash()
	storedHash := storedMultiset.Hash()
	if freshHash.Equal(storedHash) {
		conclusion := fmt.Sprintf("[UTXO-DEBUG] verifyVirtualUTXOSetSelfConsistency CONCLUSION: PASSED (%d "+
			"entries) - consensusStateStore's raw virtual UTXO table matches its own stored multiset (%s) "+
			"exactly. The base table every restorePastUTXO call builds a diff on top of is PROVEN CORRECT. "+
			"The entire corruption must live in how diffs get computed/walked/merged on top of it "+
			"(restorePastUTXO's UTXODiffChild walk, updatePruningPoint's diff computation) - not in the "+
			"underlying data itself.", entryCount, storedHash)
		log.Warnf("%s", conclusion)
		return
	}
	conclusion := fmt.Sprintf("[UTXO-DEBUG] verifyVirtualUTXOSetSelfConsistency CONCLUSION: FAILED (%d "+
		"entries) - consensusStateStore's raw virtual UTXO table hashes to %s, but its own stored multiset "+
		"says %s. Even the base table itself has drifted from the trusted multiset chain - this is not "+
		"confined to diff-walk logic alone.", entryCount, freshHash, storedHash)
	log.Errorf("%s", conclusion)
}

// bisectRestorePastUTXODivergence walks backward from a known-bad block (restorePastUTXO's
// reconstruction doesn't match the block's own header commitment) along the selected-parent chain,
// using exponential-then-binary search, to find the EXACT transition where the divergence from the
// trusted per-block multiset chain first appears. Each check calls RestorePastUTXOSetIterator,
// which is expensive (iterates virtual's entire current UTXO set - tens of seconds on a mature
// chain), so this minimizes the number of checks to O(log distance) rather than O(distance): first
// doubling backward to bracket the divergence point between a known-bad and known-good ancestor,
// then binary searching within that bracket. SelectedParent lookups (single, cheap DB reads) build
// an indexed ancestor list as a side effect of walking, so jumping to a given distance is O(1) once
// that far has already been walked - never re-walked from scratch for each check.
func (pm *pruningManager) bisectRestorePastUTXODivergence(stagingArea *model.StagingArea, badBlock *externalapi.DomainHash) {
	log.Debugf("[UTXO-DEBUG] bisectRestorePastUTXODivergence: starting from known-bad block %s", badBlock)

	ancestors := []*externalapi.DomainHash{badBlock}
	extendTo := func(distance int) (*externalapi.DomainHash, bool) {
		for len(ancestors) <= distance {
			current := ancestors[len(ancestors)-1]
			ghostdagData, err := pm.ghostdagDataStore.Get(pm.databaseContext, stagingArea, current, false)
			if err != nil {
				log.Errorf("[UTXO-DEBUG] bisectRestorePastUTXODivergence: could not fetch GHOSTDAG data for %s: %s", current, err)
				return nil, false
			}
			parent := ghostdagData.SelectedParent()
			if parent == nil {
				log.Debugf("[UTXO-DEBUG] bisectRestorePastUTXODivergence: reached genesis/end of chain at "+
					"distance %d without finding a matching ancestor - the divergence may predate the "+
					"available chain data, or every ancestor checked so far is also wrong.", len(ancestors)-1)
				return nil, false
			}
			ancestors = append(ancestors, parent)
		}
		return ancestors[distance], true
	}

	checkMatches := func(hash *externalapi.DomainHash) (bool, error) {
		header, err := pm.blockHeaderStore.BlockHeader(pm.databaseContext, stagingArea, hash)
		if err != nil {
			return false, err
		}
		expectedCommitment := header.UTXOCommitment()

		iterator, err := pm.consensusStateManager.RestorePastUTXOSetIterator(stagingArea, hash)
		if err != nil {
			return false, err
		}
		defer iterator.Close()
		ms := multiset.New()
		for ok := iterator.First(); ok; ok = iterator.Next() {
			outpoint, entry, err := iterator.Get()
			if err != nil {
				return false, err
			}
			serialized, err := utxo.SerializeUTXO(entry, outpoint)
			if err != nil {
				return false, err
			}
			ms.Add(serialized)
		}
		matches := ms.Hash().Equal(expectedCommitment)
		log.Debugf("[UTXO-DEBUG] bisectRestorePastUTXODivergence: checked %s - matches=%t "+
			"(restorePastUTXO=%s, header=%s)", hash, matches, ms.Hash(), expectedCommitment)
		return matches, nil
	}

	distance := 1
	var goodDistance, badDistance int
	foundGood := false
	for {
		ancestor, ok := extendTo(distance)
		if !ok {
			log.Debugf("[UTXO-DEBUG] bisectRestorePastUTXODivergence: selected-parent chain exhausted before " +
				"finding a matching ancestor - falling back to checking recorded pruning points instead.")
			pm.checkHistoricalPruningPoints(stagingArea)
			return
		}
		matches, err := checkMatches(ancestor)
		if err != nil {
			log.Errorf("[UTXO-DEBUG] bisectRestorePastUTXODivergence: check failed at distance %d (%s): %s - "+
				"falling back to checking recorded pruning points instead.", distance, ancestor, err)
			pm.checkHistoricalPruningPoints(stagingArea)
			return
		}
		if matches {
			goodDistance = distance
			foundGood = true
			break
		}
		badDistance = distance
		distance *= 2
	}
	if !foundGood {
		return
	}

	for goodDistance-badDistance > 1 {
		mid := (badDistance + goodDistance) / 2
		ancestor, ok := extendTo(mid)
		if !ok {
			log.Debugf("[UTXO-DEBUG] bisectRestorePastUTXODivergence: selected-parent chain exhausted during " +
				"binary search - falling back to checking recorded pruning points instead.")
			pm.checkHistoricalPruningPoints(stagingArea)
			return
		}
		matches, err := checkMatches(ancestor)
		if err != nil {
			log.Errorf("[UTXO-DEBUG] bisectRestorePastUTXODivergence: check failed at distance %d (%s): %s - "+
				"falling back to checking recorded pruning points instead.", mid, ancestor, err)
			pm.checkHistoricalPruningPoints(stagingArea)
			return
		}
		if matches {
			goodDistance = mid
		} else {
			badDistance = mid
		}
	}

	badAncestor := ancestors[badDistance]
	goodAncestor := ancestors[goodDistance]
	conclusion := fmt.Sprintf("[UTXO-DEBUG] bisectRestorePastUTXODivergence CONCLUSION: the divergence is "+
		"exactly at block %s (distance %d from %s) - restorePastUTXO is WRONG for this block but CORRECT for "+
		"its selected parent %s (distance %d). This specific block's own resolution, or its own persisted "+
		"utxoDiffStore entry, is where the drift enters.", badAncestor, badDistance, badBlock, goodAncestor, goodDistance)
	log.Errorf("%s", conclusion)
}

// repairPruningPointUTXOSet rebuilds the served pruningPointUTXOSetBucket to match
// restorePastUTXO(pruningPoint). oldBucketEntries is the bucket's current (wrong) content, already
// read once by the caller, so this only needs to walk restorePastUTXO's result to compute a
// minimal repair diff (only entries that actually differ or are missing get added; only entries no
// longer present get removed) rather than clearing and re-adding all 13M+ entries unconditionally.
//
// IMPORTANT: restorePastUTXO/RestorePastUTXOSetIterator is a DIFFERENT mechanism from the
// per-block incremental multiset (multiSetStore.Get) that VerifyCurrentPruningPointUTXOSet already
// proved correct - it walks the UTXODiffChild chain and merges diffs via WithDiffInPlace/DiffFrom,
// which is exactly the machinery containing isTolerableConflict (silently picks one side when two
// independently-built diffs disagree on an outpoint's BlockDAAScore, without erroring). The
// incremental multiset never has to pass through that merge - it just inherits its parent's
// already-settled multiset and adds to it - so it being correct does NOT prove this diff-chain
// reconstruction is also correct. So before trusting this iterator's output for anything, this
// independently hashes everything it produces and checks THAT against the header first. Logs the
// outcome; never returns an error since this is called from a non-fatal diagnostic path.
func (pm *pruningManager) repairPruningPointUTXOSet(stagingArea *model.StagingArea,
	pruningPoint *externalapi.DomainHash, oldBucketEntries map[externalapi.DomainOutpoint]externalapi.UTXOEntry,
	expectedCommitment *externalapi.DomainHash,
) {
	correctIterator, err := pm.consensusStateManager.RestorePastUTXOSetIterator(stagingArea, pruningPoint)
	if err != nil {
		log.Errorf("[UTXO-DEBUG] repairPruningPointUTXOSet: could not get restorePastUTXO iterator for %s: %s", pruningPoint, err)
		return
	}
	defer correctIterator.Close()

	toAdd := make(map[externalapi.DomainOutpoint]externalapi.UTXOEntry)
	correctOutpoints := make(map[externalapi.DomainOutpoint]bool, len(oldBucketEntries))
	sourceMultiset := multiset.New()
	for ok := correctIterator.First(); ok; ok = correctIterator.Next() {
		outpoint, entry, err := correctIterator.Get()
		if err != nil {
			log.Errorf("[UTXO-DEBUG] repairPruningPointUTXOSet: iterator.Get failed for %s: %s", pruningPoint, err)
			return
		}
		serialized, err := utxo.SerializeUTXO(entry, outpoint)
		if err != nil {
			log.Errorf("[UTXO-DEBUG] repairPruningPointUTXOSet: SerializeUTXO failed for %s: %s", pruningPoint, err)
			return
		}
		sourceMultiset.Add(serialized)
		correctOutpoints[*outpoint] = true
		oldEntry, existedBefore := oldBucketEntries[*outpoint]
		if !existedBefore || oldEntry.Amount() != entry.Amount() || !oldEntry.ScriptPublicKey().Equal(entry.ScriptPublicKey()) ||
			oldEntry.IsCoinbase() != entry.IsCoinbase() || oldEntry.BlockDAAScore() != entry.BlockDAAScore() {
			toAdd[*outpoint] = entry
		}
	}

	sourceHash := sourceMultiset.Hash()
	if !sourceHash.Equal(expectedCommitment) {
		log.Errorf("[UTXO-DEBUG] repairPruningPointUTXOSet ABORTED for %s: RestorePastUTXOSetIterator's own "+
			"output hashes to %s, which does NOT match the header commitment %s either. This diff-chain-walk "+
			"reconstruction is unreliable here - it is NOT the same mechanism as the per-block incremental "+
			"multiset that was proven correct, and it has independently drifted too. Refusing to repair the "+
			"bucket from a source that's itself unverified. Bisecting to localize the exact divergence point "+
			"instead of guessing.", pruningPoint, sourceHash, expectedCommitment)
		pm.bisectRestorePastUTXODivergence(stagingArea, pruningPoint)
		return
	}
	log.Debugf("[UTXO-DEBUG] repairPruningPointUTXOSet: RestorePastUTXOSetIterator's own output for %s "+
		"independently matches the header commitment - proceeding with repair.", pruningPoint)

	toRemove := make(map[externalapi.DomainOutpoint]externalapi.UTXOEntry)
	for outpoint, entry := range oldBucketEntries {
		if !correctOutpoints[outpoint] {
			toRemove[outpoint] = entry
		}
	}

	log.Debugf("[UTXO-DEBUG] repairPruningPointUTXOSet for %s: %d entries to add/replace, %d entries to "+
		"remove (correct set has %d entries, bucket previously had %d)",
		pruningPoint, len(toAdd), len(toRemove), len(correctOutpoints), len(oldBucketEntries))

	if len(toAdd) == 0 && len(toRemove) == 0 {
		log.Errorf("[UTXO-DEBUG] repairPruningPointUTXOSet for %s: no differing entries found, yet the "+
			"bucket multiset didn't match the header - this shouldn't be possible; leaving the bucket "+
			"untouched rather than guessing.", pruningPoint)
		return
	}

	repairDiff, err := utxo.NewUTXODiffFromCollections(utxo.NewUTXOCollection(toAdd), utxo.NewUTXOCollection(toRemove))
	if err != nil {
		log.Errorf("[UTXO-DEBUG] repairPruningPointUTXOSet: failed to build repair diff for %s: %s", pruningPoint, err)
		return
	}
	err = pm.pruningStore.UpdatePruningPointUTXOSet(pm.databaseContext, repairDiff)
	if err != nil {
		log.Errorf("[UTXO-DEBUG] repairPruningPointUTXOSet: failed to apply repair diff for %s: %s", pruningPoint, err)
		return
	}

	// Re-verify from scratch rather than trusting the diff was applied correctly.
	verifyIterator, err := pm.pruningStore.PruningPointUTXOIterator(pm.databaseContext)
	if err != nil {
		log.Errorf("[UTXO-DEBUG] repairPruningPointUTXOSet: repair applied, but could not re-verify: %s", err)
		return
	}
	defer verifyIterator.Close()
	verifyMultiset := multiset.New()
	for ok := verifyIterator.First(); ok; ok = verifyIterator.Next() {
		outpoint, entry, err := verifyIterator.Get()
		if err != nil {
			log.Errorf("[UTXO-DEBUG] repairPruningPointUTXOSet: repair applied, but re-verify iterator.Get failed: %s", err)
			return
		}
		serialized, err := utxo.SerializeUTXO(entry, outpoint)
		if err != nil {
			log.Errorf("[UTXO-DEBUG] repairPruningPointUTXOSet: repair applied, but re-verify SerializeUTXO failed: %s", err)
			return
		}
		verifyMultiset.Add(serialized)
	}
	if verifyMultiset.Hash().Equal(expectedCommitment) {
		log.Debugf("[UTXO-DEBUG] repairPruningPointUTXOSet SUCCEEDED for %s: bucket now matches the header "+
			"commitment (%s).", pruningPoint, expectedCommitment)
	} else {
		log.Errorf("[UTXO-DEBUG] repairPruningPointUTXOSet FAILED for %s: bucket still does not match the "+
			"header commitment after repair (now %s, expected %s) - repair diff itself was wrong, or "+
			"UpdatePruningPointUTXOSet didn't apply it correctly. Needs manual investigation.",
			pruningPoint, verifyMultiset.Hash(), expectedCommitment)
	}
}

func (pm *pruningManager) ClearImportedPruningPointData() error {
	err := pm.pruningStore.ClearImportedPruningPointMultiset(pm.databaseContext)
	if err != nil {
		return err
	}
	return pm.pruningStore.ClearImportedPruningPointUTXOs(pm.databaseContext)
}

func (pm *pruningManager) AppendImportedPruningPointUTXOs(outpointAndUTXOEntryPairs []*externalapi.OutpointAndUTXOEntryPair) error {
	dbTx, err := pm.databaseContext.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = dbTx.RollbackUnlessClosed() }()

	importedMultiset, err := pm.pruningStore.ImportedPruningPointMultiset(dbTx)
	if err != nil {
		if !database.IsNotFoundError(err) {
			return err
		}
		importedMultiset = multiset.New()
	}
	for _, outpointAndUTXOEntryPair := range outpointAndUTXOEntryPairs {
		serializedUTXO, err := utxo.SerializeUTXO(outpointAndUTXOEntryPair.UTXOEntry, outpointAndUTXOEntryPair.Outpoint)
		if err != nil {
			return err
		}
		importedMultiset.Add(serializedUTXO)
	}
	err = pm.pruningStore.UpdateImportedPruningPointMultiset(dbTx, importedMultiset)
	if err != nil {
		return err
	}

	err = pm.pruningStore.AppendImportedPruningPointUTXOs(dbTx, outpointAndUTXOEntryPairs)
	if err != nil {
		return err
	}

	return dbTx.Commit()
}

func (pm *pruningManager) UpdatePruningPointIfRequired() error {
	hadStartedUpdatingPruningPointUTXOSet, err := pm.pruningStore.HadStartedUpdatingPruningPointUTXOSet(pm.databaseContext)
	if err != nil {
		return err
	}
	if !hadStartedUpdatingPruningPointUTXOSet {
		return nil
	}

	log.Infof("Pruning point UTXO set update is required")
	err = pm.updatePruningPoint()
	if err != nil {
		return err
	}
	log.Info("Pruning point UTXO set updated")

	return nil
}

func (pm *pruningManager) CheckIfShouldDeletePastBlocks(stagingArea *model.StagingArea, pruningPoint *externalapi.DomainHash) (bool, *externalapi.DomainHash) {
	if pm.deletionDepth == 0 {
		return true, pruningPoint
	}
	pruningPointIndex, err := pm.pruningStore.CurrentPruningPointIndex(pm.databaseContext, stagingArea)
	if err != nil {
		return false, nil
	}
	if pruningPointIndex < pm.deletionDepth {
		return false, nil
	}
	previousDeletionPoint, err := pm.pruningStore.PruningPointByIndex(pm.databaseContext, stagingArea, pruningPointIndex-pm.deletionDepth)
	if err != nil {
		return false, nil
	}
	previousDeletionPointHeader, err := pm.blockHeaderStore.BlockHeader(pm.databaseContext, stagingArea, previousDeletionPoint)
	if err != nil {
		return false, nil
	}
	currentPruningPointHeader, err := pm.blockHeaderStore.BlockHeader(pm.databaseContext, stagingArea, pruningPoint)
	if err != nil {
		return false, nil
	}
	if currentPruningPointHeader.BlueScore()-previousDeletionPointHeader.BlueScore() < pm.pruningDepth {
		return false, nil
	}
	return true, previousDeletionPoint
}

// shouldDeferDeletion returns true if block deletion should be skipped this time based on
// data retention and pruning interval constraints. It is designed to be safe during IBD:
// when the pruning point is far in the past (as it is during initial or partial IBD),
// the retention check will not trigger, so deletion proceeds normally.
func (pm *pruningManager) shouldDeferDeletion(stagingArea *model.StagingArea, pruningPoint *externalapi.DomainHash) bool {
	// Check pruning interval: if configured and not enough time has passed since the last deletion, defer.
	if pm.pruningInterval > 0 && !pm.lastPruningTime.IsZero() {
		if time.Since(pm.lastPruningTime) < pm.pruningInterval {
			log.Debugf("Pruning interval not reached: %s since last pruning, interval is %s",
				time.Since(pm.lastPruningTime), pm.pruningInterval)
			return true
		}
	}

	// Check data retention: if configured, check whether the pruning point's blocks
	// are still within the retention window (i.e., too recent to delete).
	if pm.dataRetentionDuration > 0 {
		pruningPointHeader, err := pm.blockHeaderStore.BlockHeader(pm.databaseContext, stagingArea, pruningPoint)
		if err != nil {
			log.Warnf("Failed to get pruning point header for retention check: %s", err)
			return false
		}
		pruningPointTime := time.UnixMilli(pruningPointHeader.TimeInMilliseconds())
		blockAge := time.Since(pruningPointTime)
		if blockAge < pm.dataRetentionDuration {
			log.Debugf("Data retention constraint: pruning point age %s is less than retention duration %s",
				blockAge, pm.dataRetentionDuration)
			return true
		}
	}

	return false
}

// verifyPruningPointDiffAgainstCommitment empirically checks whatever diff updatePruningPoint just
// computed - regardless of which of the two methods (acceptance-data replay or diff-chain walk)
// produced it - against the one ground truth that actually matters: applying the diff to
// previousPruningHash's already-established multiset must produce currentPruningHash's own,
// PoW-secured header UTXO commitment. This tests the OUTPUT directly instead of reasoning about
// which method "should" be correct, and pins the exact pruning-point transition where a bad diff
// was introduced, if any.
// applyDiffToMultiset returns a clone of startMultiset with utxoSetDiff's toAdd entries added and
// its toRemove entries removed - the multiset of "startMultiset's UTXO set, transformed by
// utxoSetDiff".
func applyDiffToMultiset(startMultiset model.Multiset, utxoSetDiff externalapi.UTXODiff) (model.Multiset, error) {
	result := startMultiset.Clone()

	toAddIterator := utxoSetDiff.ToAdd().Iterator()
	defer toAddIterator.Close()
	for ok := toAddIterator.First(); ok; ok = toAddIterator.Next() {
		outpoint, entry, err := toAddIterator.Get()
		if err != nil {
			return nil, err
		}
		serialized, err := utxo.SerializeUTXO(entry, outpoint)
		if err != nil {
			return nil, err
		}
		result.Add(serialized)
	}

	toRemoveIterator := utxoSetDiff.ToRemove().Iterator()
	defer toRemoveIterator.Close()
	for ok := toRemoveIterator.First(); ok; ok = toRemoveIterator.Next() {
		outpoint, entry, err := toRemoveIterator.Get()
		if err != nil {
			return nil, err
		}
		serialized, err := utxo.SerializeUTXO(entry, outpoint)
		if err != nil {
			return nil, err
		}
		result.Remove(serialized)
	}

	return result, nil
}

// verifyPruningPointDiffAgainstCommitment logs whether applying utxoSetDiff to previousPruningHash's
// stored per-block multiset reproduces currentPruningHash's own header UTXO commitment, and returns
// that verdict. A false result means either the diff for this transition is wrong, or the baseline
// it's applied to is already offset from the header (the network-wide condition where an imported
// pruning-point UTXO set doesn't match its own header).
func (pm *pruningManager) verifyPruningPointDiffAgainstCommitment(stagingArea *model.StagingArea,
	previousPruningHash, currentPruningHash *externalapi.DomainHash, utxoSetDiff externalapi.UTXODiff, methodUsed string,
) bool {
	startingMultiset, err := pm.multiSetStore.Get(pm.databaseContext, stagingArea, previousPruningHash)
	if err != nil {
		log.Debugf("[UTXO-DEBUG] pruning point diff verification (%s): could not fetch starting multiset for %s: %s",
			methodUsed, previousPruningHash, err)
		return false
	}

	resultingMultiset, err := applyDiffToMultiset(startingMultiset, utxoSetDiff)
	if err != nil {
		log.Debugf("[UTXO-DEBUG] pruning point diff verification (%s): applying diff failed: %s", methodUsed, err)
		return false
	}

	currentHeader, err := pm.blockHeaderStore.BlockHeader(pm.databaseContext, stagingArea, currentPruningHash)
	if err != nil {
		log.Debugf("[UTXO-DEBUG] pruning point diff verification (%s): could not fetch header for %s: %s",
			methodUsed, currentPruningHash, err)
		return false
	}

	resultHash := resultingMultiset.Hash()
	expectedCommitment := currentHeader.UTXOCommitment()
	if resultHash.Equal(expectedCommitment) {
		log.Debugf("[UTXO-DEBUG] pruning point diff verification (%s) PASSED: applying the computed diff to "+
			"%s's multiset produces %s, matching %s's own header UTXO commitment exactly.",
			methodUsed, previousPruningHash, resultHash, currentPruningHash)
		return true
	}
	log.Errorf("[UTXO-DEBUG] pruning point diff verification (%s) FAILED: applying the computed diff to "+
		"%s's multiset produces %s, but %s's own header expects UTXO commitment %s.",
		methodUsed, previousPruningHash, resultHash, currentPruningHash, expectedCommitment)
	return false
}

// pruningPointBucketMultiset hashes the entire served pruning-point UTXO set bucket into a multiset.
// O(bucket size) - only called when a cheaper check has already failed.
func (pm *pruningManager) pruningPointBucketMultiset() (model.Multiset, int, error) {
	iterator, err := pm.pruningStore.PruningPointUTXOIterator(pm.databaseContext)
	if err != nil {
		return nil, 0, err
	}
	defer iterator.Close()

	bucketMultiset := multiset.New()
	entryCount := 0
	for ok := iterator.First(); ok; ok = iterator.Next() {
		outpoint, entry, err := iterator.Get()
		if err != nil {
			return nil, 0, err
		}
		serialized, err := utxo.SerializeUTXO(entry, outpoint)
		if err != nil {
			return nil, 0, err
		}
		bucketMultiset.Add(serialized)
		entryCount++
	}
	return bucketMultiset, entryCount, nil
}

// pickConsistentPruningPointDiff chooses which prev-PP -> current-PP diff to actually apply to the
// served bucket, when the cheap header check has already failed.
//
// On a network where an imported pruning-point UTXO set doesn't match its own header, every derived
// bucket and every per-block multiset inherits the same fixed offset delta from that import, so no
// diff can ever reproduce the header. But a *correct* diff still makes the served bucket agree with
// currentPruningHash's own stored per-block multiset (they carry the same delta). That check is what
// distinguishes "the diff is right, just applied on top of the inherited offset" from "the diff
// itself is wrong and would compound the offset". So: try the primary diff against that target, and
// if it fails, try the other derivation method; keep whichever agrees. If neither does, keep the
// primary and log that the served set is genuinely wrong (not just offset).
func (pm *pruningManager) pickConsistentPruningPointDiff(stagingArea *model.StagingArea,
	previousPruningHash, currentPruningHash *externalapi.DomainHash,
	primaryDiff externalapi.UTXODiff, primaryMethod string,
) (externalapi.UTXODiff, string) {
	targetMultiset, err := pm.multiSetStore.Get(pm.databaseContext, stagingArea, currentPruningHash)
	if err != nil {
		log.Warnf("pruning point %s: could not fetch per-block multiset to pick a consistent diff (%s) - "+
			"keeping the %s diff", currentPruningHash, err, primaryMethod)
		return primaryDiff, primaryMethod
	}
	targetHash := targetMultiset.Hash()

	bucketMultiset, entryCount, err := pm.pruningPointBucketMultiset()
	if err != nil {
		log.Warnf("pruning point %s: could not hash the served bucket to pick a consistent diff (%s) - "+
			"keeping the %s diff", currentPruningHash, err, primaryMethod)
		return primaryDiff, primaryMethod
	}

	diffAgrees := func(diff externalapi.UTXODiff) bool {
		resulting, applyErr := applyDiffToMultiset(bucketMultiset, diff)
		if applyErr != nil {
			return false
		}
		return resulting.Hash().Equal(targetHash)
	}

	if diffAgrees(primaryDiff) {
		log.Warnf("pruning point %s: the %s diff doesn't reproduce the header (inherited import offset) but "+
			"is consistent with this node's own per-block multiset chain - using it", currentPruningHash, primaryMethod)
		return primaryDiff, primaryMethod
	}

	altMethod := "diff-chain-walk"
	altDiffFunc := pm.calculateDiffBetweenPreviousAndCurrentPruningPoints
	if primaryMethod == "diff-chain-walk" {
		altMethod = "acceptance-data"
		altDiffFunc = pm.calculateDiffBetweenPreviousAndCurrentPruningPointsUsingAcceptanceData
	}
	altDiff, altErr := altDiffFunc(stagingArea, currentPruningHash)
	if altErr != nil {
		log.Warnf("pruning point %s: %s diff is inconsistent and the alternate derivation (%s) failed: %s - "+
			"keeping the %s diff", currentPruningHash, primaryMethod, altMethod, altErr, primaryMethod)
		return primaryDiff, primaryMethod
	}
	if diffAgrees(altDiff) {
		log.Warnf("pruning point %s: the %s diff was inconsistent with this node's per-block multiset chain "+
			"but the %s diff is - switching to %s to keep the pruning-point offset from compounding",
			currentPruningHash, primaryMethod, altMethod, altMethod)
		return altDiff, altMethod
	}

	log.Errorf("pruning point %s: NEITHER the %s nor the %s diff makes the served bucket (%d entries) agree "+
		"with this node's own per-block multiset chain - the served pruning-point UTXO set for this "+
		"transition is genuinely wrong, not just offset. Applying the %s diff anyway.",
		currentPruningHash, primaryMethod, altMethod, entryCount, primaryMethod)
	return primaryDiff, primaryMethod
}

func (pm *pruningManager) updatePruningPoint() error {
	onEnd := logger.LogAndMeasureExecutionTime(log, "updatePruningPoint")
	defer onEnd()

	logger.LogMemoryStats(log, "updatePruningPoint start")
	defer logger.LogMemoryStats(log, "updatePruningPoint end")

	stagingArea := model.NewStagingArea()
	log.Info("Getting the pruning point")
	pruningPoint, err := pm.pruningStore.PruningPoint(pm.databaseContext, stagingArea)
	if err != nil {
		return err
	}

	log.Info("Restoring the pruning point UTXO set from acceptance data")
	utxoSetDiff, err := pm.calculateDiffBetweenPreviousAndCurrentPruningPointsUsingAcceptanceData(stagingArea, pruningPoint)
	methodUsed := "acceptance-data"

	if err != nil {
		log.Infof("Calculating pruning points diff failed %s. Falling back to calculate "+
			"through iterating previous and current pruning points diffs", err)
		utxoSetDiff, err = pm.calculateDiffBetweenPreviousAndCurrentPruningPoints(stagingArea, pruningPoint)
		methodUsed = "diff-chain-walk"
		if err != nil {
			log.Infof("Calculating pruning points diff failed eitherway %s", err)
			return err
		}
	}

	// Verify whichever method produced utxoSetDiff against the new pruning point's own header
	// commitment, before it gets applied to the served bucket. If it doesn't match, don't just log
	// it - try the other derivation method and keep whichever one keeps the served bucket consistent
	// with this node's own per-block multiset chain, so a buggy derivation can't compound the
	// pruning-point offset from one advancement to the next.
	if !pruningPoint.Equal(pm.genesisHash) {
		if pruningPointIndex, idxErr := pm.pruningStore.CurrentPruningPointIndex(pm.databaseContext, stagingArea); idxErr == nil && pruningPointIndex > 0 {
			if previousPruningHash, prevErr := pm.pruningStore.PruningPointByIndex(pm.databaseContext, stagingArea, pruningPointIndex-1); prevErr == nil {
				if !pm.verifyPruningPointDiffAgainstCommitment(stagingArea, previousPruningHash, pruningPoint, utxoSetDiff, methodUsed) {
					utxoSetDiff, methodUsed = pm.pickConsistentPruningPointDiff(
						stagingArea, previousPruningHash, pruningPoint, utxoSetDiff, methodUsed)
				}
			} else {
				log.Debugf("[UTXO-DEBUG] could not fetch previous pruning point for diff verification: %s", prevErr)
			}
		} else if idxErr != nil {
			log.Debugf("[UTXO-DEBUG] could not fetch pruning point index for diff verification: %s", idxErr)
		}
	}
	log.Infof("Restored the pruning point UTXO set (diff method: %s)", methodUsed)

	log.Info("Updating the pruning point UTXO set")
	err = pm.pruningStore.UpdatePruningPointUTXOSet(pm.databaseContext, utxoSetDiff)
	if err != nil {
		return err
	}
	log.Info("Validating the UTXO set fits commitment")
	if pm.shouldSanityCheckPruningUTXOSet && !pruningPoint.Equal(pm.genesisHash) {
		err = pm.validateUTXOSetFitsCommitment(stagingArea, pruningPoint)
		if err != nil {
			return err
		}
	}
	var newPruningTime *time.Time
	if pm.shouldDeferDeletion(stagingArea, pruningPoint) {
		log.Infof("Pruning point advanced, but block deletion deferred (data retention/interval not met)")
	} else {
		log.Infof("Deletion of past blocks")
		err = pm.deletePastBlocks(stagingArea, pruningPoint)
		if err != nil {
			return err
		}

		t := time.Now()
		newPruningTime = &t
		// Stage the durable timestamp to the database in the same transaction
		pm.pruningStore.StageLastPruningTime(stagingArea, *newPruningTime)
	}

	log.Info("Commit all changes")
	err = staging.CommitAllChanges(pm.databaseContext, stagingArea)
	if err != nil {
		return err
	}

	if newPruningTime != nil {
		pm.lastPruningTime = *newPruningTime
	}

	// Invalidate the cached pruning point and anticone since the pruning point just moved
	pm.cachedPruningPoint = nil
	pm.cachedPruningPointAnticone = nil

	log.Info("Finishing updating the pruning point UTXO set")
	return pm.pruningStore.FinishUpdatingPruningPointUTXOSet(pm.databaseContext)
}

func (pm *pruningManager) PruneAllBlocksBelow(stagingArea *model.StagingArea, pruningPointHash *externalapi.DomainHash) error {
	onEnd := logger.LogAndMeasureExecutionTime(log, "PruneAllBlocksBelow")
	defer onEnd()

	iterator, err := pm.blocksStore.AllBlockHashesIterator(pm.databaseContext)
	if err != nil {
		return err
	}
	defer iterator.Close()

	for ok := iterator.First(); ok; ok = iterator.Next() {
		blockHash, err := iterator.Get()
		if err != nil {
			return err
		}
		isInPastOfPruningPoint, err := pm.dagTopologyManager.IsAncestorOf(stagingArea, pruningPointHash, blockHash)
		if err != nil {
			return err
		}
		if !isInPastOfPruningPoint {
			continue
		}
		_, err = pm.deleteBlock(stagingArea, blockHash)
		if err != nil {
			return err
		}
	}
	return nil
}

func (pm *pruningManager) PruningPointAndItsAnticone() ([]*externalapi.DomainHash, error) {
	onEnd := logger.LogAndMeasureExecutionTime(log, "PruningPointAndItsAnticone")
	defer onEnd()

	stagingArea := model.NewStagingArea()
	pruningPoint, err := pm.pruningStore.PruningPoint(pm.databaseContext, stagingArea)
	if err != nil {
		return nil, err
	}

	// By the Prunality proof, the pruning point anticone is a closed set (i.e., guaranteed not to change) ,
	// so we can safely cache it.
	if pm.cachedPruningPoint != nil && pm.cachedPruningPoint.Equal(pruningPoint) {
		return append([]*externalapi.DomainHash{pruningPoint}, pm.cachedPruningPointAnticone...), nil
	}

	pruningPointAnticone, err := pm.dagTraversalManager.AnticoneFromVirtualPOV(stagingArea, pruningPoint)
	if err != nil {
		return nil, err
	}

	// Sorting the blocks in topological order
	var sortErr error
	sort.Slice(pruningPointAnticone, func(i, j int) bool {
		headerI, err := pm.blockHeaderStore.BlockHeader(pm.databaseContext, stagingArea, pruningPointAnticone[i])
		if err != nil {
			sortErr = err
			return false
		}

		headerJ, err := pm.blockHeaderStore.BlockHeader(pm.databaseContext, stagingArea, pruningPointAnticone[j])
		if err != nil {
			sortErr = err
			return false
		}

		return headerI.BlueWork().Cmp(headerJ.BlueWork()) < 0
	})
	if sortErr != nil {
		return nil, sortErr
	}

	pm.cachedPruningPoint = pruningPoint
	pm.cachedPruningPointAnticone = pruningPointAnticone

	// The pruning point should always come first
	return append([]*externalapi.DomainHash{pruningPoint}, pruningPointAnticone...), nil
}

func (pm *pruningManager) ExpectedHeaderPruningPoint(stagingArea *model.StagingArea, blockHash *externalapi.DomainHash) (*externalapi.DomainHash, error) {
	ghostdagData, err := pm.ghostdagDataStore.Get(pm.databaseContext, stagingArea, blockHash, false)
	if database.IsNotFoundError(err) {
		// Virtual GHOSTDAG data might be missing during early init / IBD teardown.
		// For virtual, fall back to current pruning point (or genesis if unset) so we
		// don't hard-fail block template building.
		if blockHash.Equal(model.VirtualBlockHash) {
			pruningPoint, ppErr := pm.pruningStore.PruningPoint(pm.databaseContext, stagingArea)
			if database.IsNotFoundError(ppErr) {
				return pm.genesisHash, nil
			}
			if ppErr != nil {
				return nil, ppErr
			}
			return pruningPoint, nil
		}

		log.Infof("ExpectedHeaderPruningPoint failed to retrieve with %s\n", blockHash)
		return nil, err
	}
	if err != nil {
		return nil, err
	}

	if ghostdagData.SelectedParent().Equal(pm.genesisHash) {
		return pm.genesisHash, nil
	}

	selectedParentHeader, err := pm.blockHeaderStore.BlockHeader(pm.databaseContext, stagingArea, ghostdagData.SelectedParent())
	if database.IsNotFoundError(err) {
		log.Infof("ExpectedHeaderPruningPoint: block header not found for selected parent %s\n", ghostdagData.SelectedParent())
		return nil, err
	}
	if err != nil {
		return nil, err
	}

	selectedParentPruningPointHeader, err := pm.blockHeaderStore.BlockHeader(pm.databaseContext, stagingArea, selectedParentHeader.PruningPoint())
	if database.IsNotFoundError(err) {
		return selectedParentHeader.PruningPoint(), nil
	}
	if err != nil {
		return nil, err
	}

	nextOrCurrentPruningPoint := selectedParentHeader.PruningPoint()
	pruningPoint, err := pm.pruningStore.PruningPoint(pm.databaseContext, stagingArea)
	if err != nil {
		return nil, err
	}

	// If the block doesn't have the pruning in its selected chain we know for sure that it can't trigger a pruning point
	// change (we check the selected parent to take care of the case where the block is the virtual which doesn't have reachability data).
	hasPruningPointInItsSelectedChain, err := pm.dagTopologyManager.IsInSelectedParentChainOf(stagingArea, pruningPoint, ghostdagData.SelectedParent())
	if err != nil {
		return nil, err
	}

	// Note: the pruning point from the POV of the current block is the first block in its chain that is in depth of pm.pruningDepth and
	// its finality score is greater than the previous pruning point. This is why the diff between finalityScore(selectedParent.blueScore + 1) * finalityInterval
	// and the current block blue score is less than pm.pruningDepth we can know for sure that this block didn't trigger a pruning point change.

	minRequiredBlueScoreForNextPruningPoint := (pm.finalityScore(selectedParentPruningPointHeader.BlueScore()) + 1) * pm.finalityInterval

	if hasPruningPointInItsSelectedChain &&
		minRequiredBlueScoreForNextPruningPoint+pm.pruningDepth <= ghostdagData.BlueScore() {
		var suggestedLowHash *externalapi.DomainHash
		hasReachabilityData, err := pm.reachabilityDataStore.HasReachabilityData(pm.databaseContext, stagingArea, selectedParentHeader.PruningPoint())
		if err != nil {
			return nil, err
		}

		if hasReachabilityData {
			// nextPruningPointAndCandidateByBlockHash needs suggestedLowHash to be in the future of the pruning point because
			// otherwise reachability selected chain data is unreliable.
			isInFutureOfCurrentPruningPoint, err := pm.dagTopologyManager.IsAncestorOf(stagingArea, pruningPoint, selectedParentHeader.PruningPoint())
			if err != nil {
				return nil, err
			}
			if isInFutureOfCurrentPruningPoint {
				suggestedLowHash = selectedParentHeader.PruningPoint()
			}
		}

		nextOrCurrentPruningPoint, _, err = pm.nextPruningPointAndCandidateByBlockHash(stagingArea, blockHash, suggestedLowHash)
		if err != nil {
			return nil, err
		}
	}

	isHeaderPruningPoint, err := pm.isPruningPointInPruningDepth(stagingArea, blockHash, nextOrCurrentPruningPoint)
	if err != nil {
		return nil, err
	}

	if isHeaderPruningPoint {
		return nextOrCurrentPruningPoint, nil
	}

	pruningPointIndex, err := pm.pruningStore.CurrentPruningPointIndex(pm.databaseContext, stagingArea)
	if err != nil {
		return nil, err
	}

	for i := pruningPointIndex; ; i-- {
		currentPruningPoint, err := pm.pruningStore.PruningPointByIndex(pm.databaseContext, stagingArea, i)
		if err != nil {
			return nil, err
		}

		isHeaderPruningPoint, err := pm.isPruningPointInPruningDepth(stagingArea, blockHash, currentPruningPoint)
		if err != nil {
			return nil, err
		}

		if isHeaderPruningPoint {
			return currentPruningPoint, nil
		}

		if i == 0 {
			break
		}
	}

	return pm.genesisHash, nil
}

func (pm *pruningManager) isPruningPointInPruningDepth(stagingArea *model.StagingArea, blockHash, pruningPoint *externalapi.DomainHash) (bool, error) {
	pruningPointHeader, err := pm.blockHeaderStore.BlockHeader(pm.databaseContext, stagingArea, pruningPoint)
	if err != nil {
		return false, err
	}

	blockGHOSTDAGData, err := pm.ghostdagDataStore.Get(pm.databaseContext, stagingArea, blockHash, false)
	if database.IsNotFoundError(err) {
		log.Infof("isPruningPointInPruningDepth failed to retrieve with %s\n", blockHash)
		return false, err
	}
	if err != nil {
		return false, err
	}

	return blockGHOSTDAGData.BlueScore() >= pruningPointHeader.BlueScore()+pm.pruningDepth, nil
}

func (pm *pruningManager) TrustedBlockAssociatedGHOSTDAGDataBlockHashes(stagingArea *model.StagingArea, blockHash *externalapi.DomainHash) ([]*externalapi.DomainHash, error) {
	blockHashes := make([]*externalapi.DomainHash, 0, pm.k[constants.GetBlockVersion()-1])
	current := blockHash
	isTrustedData := false
	for i := externalapi.KType(0); i <= pm.k[constants.GetBlockVersion()-1]; i++ {
		ghostdagData, err := pm.ghostdagDataStore.Get(pm.databaseContext, stagingArea, current, isTrustedData)
		if database.IsNotFoundError(err) {
			log.Infof("TrustedBlockAssociatedGHOSTDAGDataBlockHashes failed to retrieve with %s\n", current)
			return nil, err
		}
		isNotFoundError := database.IsNotFoundError(err)
		if !isNotFoundError && err != nil {
			return nil, err
		}
		if isNotFoundError || ghostdagData.SelectedParent().Equal(model.VirtualGenesisBlockHash) {
			isTrustedData = true
			ghostdagData, err = pm.ghostdagDataStore.Get(pm.databaseContext, stagingArea, current, true)
			if err != nil {
				return nil, err
			}
		}

		blockHashes = append(blockHashes, current)

		if ghostdagData.SelectedParent().Equal(pm.genesisHash) {
			break
		}

		if current.Equal(pm.genesisHash) {
			break
		}

		current = ghostdagData.SelectedParent()
	}

	return blockHashes, nil
}
