package pruningproofmanager

import (
	"fmt"
	"math/big"
	"time"

	consensusDB "github.com/Hoosat-Oy/HTND/domain/consensus/database"
	"github.com/Hoosat-Oy/HTND/domain/consensus/datastructures/blockheaderstore"
	"github.com/Hoosat-Oy/HTND/domain/consensus/datastructures/blockrelationstore"
	"github.com/Hoosat-Oy/HTND/domain/consensus/datastructures/ghostdagdatastore"
	"github.com/Hoosat-Oy/HTND/domain/consensus/datastructures/reachabilitydatastore"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/domain/consensus/processes/dagtopologymanager"
	"github.com/Hoosat-Oy/HTND/domain/consensus/processes/dagtraversalmanager"
	"github.com/Hoosat-Oy/HTND/domain/consensus/processes/ghostdagmanager"
	"github.com/Hoosat-Oy/HTND/domain/consensus/processes/reachabilitymanager"
	"github.com/Hoosat-Oy/HTND/domain/consensus/ruleerrors"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/consensushashing"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/hashset"
	"github.com/Hoosat-Oy/HTND/infrastructure/db/database"
	"github.com/Hoosat-Oy/HTND/infrastructure/logger"
	"github.com/Hoosat-Oy/HTND/util/staging"
	"github.com/pkg/errors"
)

const pruningProofProgressLogInterval = 30 * time.Second

// pruningProofReindexRootUpdateInterval throttles reachability root updates during pruning proof
// validation/reachability building. Updating the root is a performance heuristic, but calling it
// for every processed header is disproportionately expensive during IBD.
//
// Note: This does not change consensus semantics. It only affects how reachability interval
// reindexes are biased.
const pruningProofReindexRootUpdateInterval = 200

type pruningProofManager struct {
	databaseContext model.DBManager

	dagTopologyManagers  []model.DAGTopologyManager
	ghostdagManagers     []model.GHOSTDAGManager
	reachabilityManager  model.ReachabilityManager
	dagTraversalManagers []model.DAGTraversalManager
	parentsManager       model.ParentsManager
	pruningManager       model.PruningManager

	ghostdagDataStores    []model.GHOSTDAGDataStore
	pruningStore          model.PruningStore
	blockHeaderStore      model.BlockHeaderStore
	blockStatusStore      model.BlockStatusStore
	finalityStore         model.FinalityStore
	consensusStateStore   model.ConsensusStateStore
	blockRelationStore    model.BlockRelationStore
	reachabilityDataStore model.ReachabilityDataStore

	genesisHash   *externalapi.DomainHash
	k             []externalapi.KType
	pruningProofM uint64
	maxBlockLevel int

	cachedPruningPoint *externalapi.DomainHash
	cachedProof        *externalapi.PruningPointProof
}

// New instantiates a new PruningManager
func New(
	databaseContext model.DBManager,

	dagTopologyManagers []model.DAGTopologyManager,
	ghostdagManagers []model.GHOSTDAGManager,
	reachabilityManager model.ReachabilityManager,
	dagTraversalManagers []model.DAGTraversalManager,
	parentsManager model.ParentsManager,
	pruningManager model.PruningManager,

	ghostdagDataStores []model.GHOSTDAGDataStore,
	pruningStore model.PruningStore,
	blockHeaderStore model.BlockHeaderStore,
	blockStatusStore model.BlockStatusStore,
	finalityStore model.FinalityStore,
	consensusStateStore model.ConsensusStateStore,
	blockRelationStore model.BlockRelationStore,
	reachabilityDataStore model.ReachabilityDataStore,

	genesisHash *externalapi.DomainHash,
	k []externalapi.KType,
	pruningProofM uint64,
	maxBlockLevel int,
) model.PruningProofManager {
	return &pruningProofManager{
		databaseContext:      databaseContext,
		dagTopologyManagers:  dagTopologyManagers,
		ghostdagManagers:     ghostdagManagers,
		reachabilityManager:  reachabilityManager,
		dagTraversalManagers: dagTraversalManagers,
		parentsManager:       parentsManager,
		pruningManager:       pruningManager,

		ghostdagDataStores:    ghostdagDataStores,
		pruningStore:          pruningStore,
		blockHeaderStore:      blockHeaderStore,
		blockStatusStore:      blockStatusStore,
		finalityStore:         finalityStore,
		consensusStateStore:   consensusStateStore,
		blockRelationStore:    blockRelationStore,
		reachabilityDataStore: reachabilityDataStore,

		genesisHash:   genesisHash,
		k:             k,
		pruningProofM: pruningProofM,
		maxBlockLevel: maxBlockLevel,
	}
}

func (ppm *pruningProofManager) BuildPruningPointProof(stagingArea *model.StagingArea) (*externalapi.PruningPointProof, error) {
	onEnd := logger.LogAndMeasureExecutionTime(log, "BuildPruningPointProof")
	defer onEnd()

	pruningPoint, err := ppm.pruningStore.PruningPoint(ppm.databaseContext, stagingArea)
	if err != nil {
		return nil, err
	}

	if ppm.cachedPruningPoint != nil && ppm.cachedPruningPoint.Equal(pruningPoint) {
		return ppm.cachedProof, nil
	}

	proof, err := ppm.buildPruningPointProof(stagingArea)
	if err != nil {
		return nil, err
	}

	ppm.cachedProof = proof
	ppm.cachedPruningPoint = pruningPoint

	return proof, nil
}

func (ppm *pruningProofManager) buildPruningPointProof(stagingArea *model.StagingArea) (*externalapi.PruningPointProof, error) {
	onEnd := logger.LogAndMeasureExecutionTime(log, "buildPruningPointProof")
	defer onEnd()

	pruningPoint, err := ppm.pruningStore.PruningPoint(ppm.databaseContext, stagingArea)
	if err != nil {
		return nil, err
	}

	if pruningPoint.Equal(ppm.genesisHash) {
		return &externalapi.PruningPointProof{}, nil
	}

	pruningPointHeader, err := ppm.blockHeaderStore.BlockHeader(ppm.databaseContext, stagingArea, pruningPoint)
	if err != nil {
		return nil, err
	}

	maxLevel := len(ppm.parentsManager.Parents(pruningPointHeader)) - 1
	headersByLevel := make(map[int][]externalapi.BlockHeader)
	selectedTipByLevel := make([]*externalapi.DomainHash, maxLevel+1)
	pruningPointLevel := pruningPointHeader.BlockLevel(ppm.maxBlockLevel)
	for blockLevel := maxLevel; blockLevel >= 0; blockLevel-- {
		var selectedTip *externalapi.DomainHash
		if blockLevel <= pruningPointLevel {
			selectedTip = pruningPoint
		} else {
			blockLevelParents := ppm.parentsManager.ParentsAtLevel(pruningPointHeader, blockLevel)
			selectedTipCandidates := make([]*externalapi.DomainHash, 0, len(blockLevelParents))

			// In a pruned node, some pruning point parents might be missing, but we're guaranteed that its
			// selected parent is not missing.
			for _, parent := range blockLevelParents {
				_, err := ppm.ghostdagDataStores[blockLevel].Get(ppm.databaseContext, stagingArea, parent, false)
				if database.IsNotFoundError(err) {
					continue
				}
				if err != nil {
					return nil, err
				}

				selectedTipCandidates = append(selectedTipCandidates, parent)
			}

			selectedTip, err = ppm.ghostdagManagers[blockLevel].ChooseSelectedParent(stagingArea, selectedTipCandidates...)
			if err != nil {
				return nil, err
			}
		}
		selectedTipByLevel[blockLevel] = selectedTip

		blockAtDepth2M, err := ppm.blockAtDepth(stagingArea, ppm.ghostdagDataStores[blockLevel], selectedTip, 2*ppm.pruningProofM)
		if err != nil {
			return nil, err
		}

		root := blockAtDepth2M
		var blockAtDepthMAtNextLevel *externalapi.DomainHash
		if blockLevel != maxLevel {
			blockAtDepthMAtNextLevel, err = ppm.blockAtDepth(stagingArea, ppm.ghostdagDataStores[blockLevel+1], selectedTipByLevel[blockLevel+1], ppm.pruningProofM)
			if err != nil {
				return nil, err
			}

			isBlockAtDepthMAtNextLevelAncestorOfBlockAtDepth2M, err := ppm.dagTopologyManagers[blockLevel].IsAncestorOf(stagingArea, blockAtDepthMAtNextLevel, blockAtDepth2M)
			if err != nil {
				return nil, err
			}

			if isBlockAtDepthMAtNextLevelAncestorOfBlockAtDepth2M {
				root = blockAtDepthMAtNextLevel
			} else {
				isBlockAtDepth2MAncestorOfBlockAtDepthMAtNextLevel, err := ppm.dagTopologyManagers[blockLevel].IsAncestorOf(stagingArea, blockAtDepth2M, blockAtDepthMAtNextLevel)
				if err != nil {
					return nil, err
				}

				if !isBlockAtDepth2MAncestorOfBlockAtDepthMAtNextLevel {
					// find common ancestor
					current := blockAtDepthMAtNextLevel
					for {
						ghostdagData, err := ppm.ghostdagDataStores[blockLevel].Get(ppm.databaseContext, stagingArea, current, false)
						if err != nil {
							return nil, err
						}

						current = ghostdagData.SelectedParent()
						if current.Equal(model.VirtualGenesisBlockHash) {
							return nil, errors.Errorf("No common ancestor between %s and %s at level %d", blockAtDepth2M, blockAtDepthMAtNextLevel, blockLevel)
						}

						isCurrentAncestorOfBlockAtDepth2M, err := ppm.dagTopologyManagers[blockLevel].IsAncestorOf(stagingArea, current, blockAtDepth2M)
						if err != nil {
							return nil, err
						}

						if isCurrentAncestorOfBlockAtDepth2M {
							root = current
							break
						}
					}
				}
			}
		}

		headers := make([]externalapi.BlockHeader, 0, 2*ppm.pruningProofM)
		visited := hashset.New()
		queue := ppm.dagTraversalManagers[blockLevel].NewUpHeap(stagingArea)
		err = queue.Push(root)
		if err != nil {
			return nil, err
		}
		for queue.Len() > 0 {
			current := queue.Pop()

			if visited.Contains(current) {
				continue
			}

			visited.Add(current)
			isRelevantForProof, err := ppm.dagTopologyManagers[blockLevel].IsAncestorOf(stagingArea, current, selectedTip)
			if err != nil {
				return nil, err
			}

			// Proof levels above the pruning point must also include (and connect to) the block-at-depth-m
			// of the selected tip in the next level, even when it's not on the selected tip cone of this
			// level. This is required so the validator can link adjacent proof levels.
			if !isRelevantForProof && blockAtDepthMAtNextLevel != nil {
				isRelevantForProof, err = ppm.dagTopologyManagers[blockLevel].IsAncestorOf(stagingArea, current, blockAtDepthMAtNextLevel)
				if err != nil {
					return nil, err
				}
			}

			if !isRelevantForProof {
				continue
			}

			currentHeader, err := ppm.blockHeaderStore.BlockHeader(ppm.databaseContext, stagingArea, current)
			if err != nil {
				return nil, err
			}

			headers = append(headers, currentHeader)
			children, err := ppm.dagTopologyManagers[blockLevel].Children(stagingArea, current)
			if err != nil {
				return nil, err
			}

			for _, child := range children {
				if child.Equal(model.VirtualBlockHash) {
					continue
				}

				err = queue.Push(child)
				if err != nil {
					return nil, err
				}
			}
		}

		headersByLevel[blockLevel] = headers
	}

	proof := &externalapi.PruningPointProof{Headers: make([][]externalapi.BlockHeader, len(headersByLevel))}
	for i := 0; i < len(headersByLevel); i++ {
		proof.Headers[i] = headersByLevel[i]
	}

	return proof, nil
}

func (ppm *pruningProofManager) blockAtDepth(stagingArea *model.StagingArea, ghostdagDataStore model.GHOSTDAGDataStore, highHash *externalapi.DomainHash, depth uint64) (*externalapi.DomainHash, error) {
	currentBlockHash := highHash
	highBlockGHOSTDAGData, err := ghostdagDataStore.Get(ppm.databaseContext, stagingArea, highHash, false)
	if database.IsNotFoundError(err) {
		log.Infof("isPruningPointInPruningDepth failed to retrieve with %s\n", highHash)
		return nil, err
	}
	if err != nil {
		return nil, err
	}

	requiredBlueScore := uint64(0)
	if highBlockGHOSTDAGData.BlueScore() > depth {
		requiredBlueScore = highBlockGHOSTDAGData.BlueScore() - depth
	}

	currentBlockGHOSTDAGData := highBlockGHOSTDAGData
	// If we used `BlockIterator` we'd need to do more calls to `ghostdagDataStore` so we can get the blueScore
	for currentBlockGHOSTDAGData.BlueScore() >= requiredBlueScore {
		if currentBlockGHOSTDAGData.SelectedParent().Equal(model.VirtualGenesisBlockHash) {
			break
		}

		currentBlockHash = currentBlockGHOSTDAGData.SelectedParent()
		currentBlockGHOSTDAGData, err = ghostdagDataStore.Get(ppm.databaseContext, stagingArea, currentBlockHash, false)
		if err != nil {
			return nil, err
		}
	}
	return currentBlockHash, nil
}

func (ppm *pruningProofManager) ValidatePruningPointProof(pruningPointProof *externalapi.PruningPointProof) error {
	onEnd := logger.LogAndMeasureExecutionTime(log, "ValidatePruningPointProof")
	defer onEnd()

	stagingArea := model.NewStagingArea()

	if len(pruningPointProof.Headers) == 0 {
		return errors.Wrap(ruleerrors.ErrPruningProofEmpty, "pruning proof is empty")
	}

	level0Headers := pruningPointProof.Headers[0]
	pruningPointHeader := level0Headers[len(level0Headers)-1]
	pruningPoint := consensushashing.HeaderHash(pruningPointHeader)
	pruningPointBlockLevel := pruningPointHeader.BlockLevel(ppm.maxBlockLevel)
	maxLevel := len(ppm.parentsManager.Parents(pruningPointHeader)) - 1
	if maxLevel >= len(pruningPointProof.Headers) {
		return errors.Wrapf(ruleerrors.ErrPruningProofEmpty, "proof has only %d levels while pruning point "+
			"has parents from %d levels", len(pruningPointProof.Headers), maxLevel+1)
	}

	blockHeaderStore, blockRelationStores, reachabilityDataStores, ghostdagDataStores, err := ppm.dagStores(maxLevel)
	if err != nil {
		return err
	}

	reachabilityManagers, dagTopologyManagers, ghostdagManagers := ppm.dagProcesses(maxLevel, blockHeaderStore, blockRelationStores, reachabilityDataStores, ghostdagDataStores)

	defer func() {
		for i := 0; i <= maxLevel; i++ {
			ghostdagDataStores[i].UnstageAll(stagingArea)
			blockRelationStores[i].UnstageAll(stagingArea)
			reachabilityDataStores[i].UnstageAll(stagingArea)
		}
		blockHeaderStore.UnstageAll(stagingArea)
		stagingArea = nil
	}()

	for blockLevel := 0; blockLevel <= maxLevel; blockLevel++ {
		err := reachabilityManagers[blockLevel].Init(stagingArea)
		if err != nil {
			return err
		}

		err = dagTopologyManagers[blockLevel].SetParents(stagingArea, model.VirtualGenesisBlockHash, nil)
		if err != nil {
			return err
		}

		ghostdagDataStores[blockLevel].Stage(stagingArea, model.VirtualGenesisBlockHash, externalapi.NewBlockGHOSTDAGData(
			0,
			big.NewInt(0),
			nil,
			nil,
			nil,
			nil,
		), false)
	}

	selectedTipByLevel := make([]*externalapi.DomainHash, maxLevel+1)
	for blockLevel := maxLevel; blockLevel >= 0; blockLevel-- {
		levelStartTime := time.Now()
		headers := make([]externalapi.BlockHeader, len(pruningPointProof.Headers[blockLevel]))
		copy(headers, pruningPointProof.Headers[blockLevel])
		totalHeaders := len(headers)
		log.Infof("Validating level %d from the pruning point proof (%d headers)", blockLevel, totalHeaders)
		lastProgressLogTime := time.Now()

		var (
			parentsLookupDuration          time.Duration
			setParentsDuration             time.Duration
			ghostdagDuration               time.Duration
			chooseSelectedParentDuration   time.Duration
			reachabilityAddDuration        time.Duration
			reachabilityUpdateRootDuration time.Duration
			parentsLookupCount             int
			reindexRootUpdateCount         int
		)

		var selectedTip *externalapi.DomainHash
		for i, header := range headers {
			blockHash := consensushashing.HeaderHash(header)
			if header.BlockLevel(ppm.maxBlockLevel) < blockLevel {
				return errors.Wrapf(ruleerrors.ErrPruningProofWrongBlockLevel, "block %s level is %d when it's "+
					"expected to be at least %d", blockHash, header.BlockLevel(ppm.maxBlockLevel), blockLevel)
			}

			blockHeaderStore.Stage(stagingArea, blockHash, header)

			var parents []*externalapi.DomainHash
			parentsLookupStart := time.Now()
			for _, parent := range ppm.parentsManager.ParentsAtLevel(header, blockLevel) {
				parentsLookupCount++
				_, err := ghostdagDataStores[blockLevel].Get(ppm.databaseContext, stagingArea, parent, false)
				if database.IsNotFoundError(err) {
					continue
				}
				if err != nil {
					return err
				}

				parents = append(parents, parent)
			}

			if len(parents) == 0 {
				if i != 0 {
					return errors.Wrapf(ruleerrors.ErrPruningProofHeaderWithNoKnownParents, "the proof header "+
						"%s is missing known parents", blockHash)
				}
				parents = append(parents, model.VirtualGenesisBlockHash)
			}

			parentsLookupDuration += time.Since(parentsLookupStart)

			setParentsStart := time.Now()
			err := dagTopologyManagers[blockLevel].SetParents(stagingArea, blockHash, parents)
			if err != nil {
				return err
			}
			setParentsDuration += time.Since(setParentsStart)

			ghostdagStart := time.Now()
			err = ghostdagManagers[blockLevel].GHOSTDAG(stagingArea, blockHash)
			if err != nil {
				return err
			}
			ghostdagDuration += time.Since(ghostdagStart)

			chooseSelectedParentStart := time.Now()
			if selectedTip == nil {
				selectedTip = blockHash
			} else {
				selectedTip, err = ghostdagManagers[blockLevel].ChooseSelectedParent(stagingArea, selectedTip, blockHash)
				if err != nil {
					return err
				}
			}
			chooseSelectedParentDuration += time.Since(chooseSelectedParentStart)

			reachabilityAddStart := time.Now()
			err = reachabilityManagers[blockLevel].AddBlock(stagingArea, blockHash)
			if err != nil {
				return err
			}
			reachabilityAddDuration += time.Since(reachabilityAddStart)

			if selectedTip.Equal(blockHash) && ((i+1)%pruningProofReindexRootUpdateInterval == 0 || i+1 == len(headers)) {
				reindexRootUpdateCount++
				reachabilityUpdateRootStart := time.Now()
				err := reachabilityManagers[blockLevel].UpdateReindexRoot(stagingArea, selectedTip)
				if err != nil {
					return err
				}
				reachabilityUpdateRootDuration += time.Since(reachabilityUpdateRootStart)
			}

			if totalHeaders > 0 && time.Since(lastProgressLogTime) >= pruningProofProgressLogInterval {
				processed := i + 1
				elapsed := time.Since(levelStartTime)
				rate := float64(processed) / elapsed.Seconds()
				eta := time.Duration(0)
				if rate > 0 {
					eta = time.Duration(float64(totalHeaders-processed)/rate) * time.Second
				}

				perHdr := func(d time.Duration) float64 {
					if processed == 0 {
						return 0
					}
					return float64(d.Microseconds()) / float64(processed)
				}

				timingSummary := fmt.Sprintf(
					"cost_us/hdr parents=%.1f setParents=%.1f ghostdag=%.1f chooseSP=%.1f reachAdd=%.1f reindexRoot=%.1f parentsLookups=%d rootUpdates=%d",
					perHdr(parentsLookupDuration),
					perHdr(setParentsDuration),
					perHdr(ghostdagDuration),
					perHdr(chooseSelectedParentDuration),
					perHdr(reachabilityAddDuration),
					perHdr(reachabilityUpdateRootDuration),
					parentsLookupCount,
					reindexRootUpdateCount,
				)
				log.Infof("Pruning proof validate level %d progress: %d/%d (%.1f%%) elapsed=%s rate=%.0f hdr/s eta~%s",
					blockLevel, processed, totalHeaders, 100*float64(processed)/float64(totalHeaders), elapsed.Truncate(time.Second), rate, eta.Truncate(time.Second))
				log.Debugf("Pruning proof validate level %d timings: %s", blockLevel, timingSummary)
				lastProgressLogTime = time.Now()
			}
		}

		log.Debugf("Finished validating level %d from the pruning point proof (headers=%d selectedTip=%s duration=%s)",
			blockLevel, totalHeaders, selectedTip, time.Since(levelStartTime).Truncate(time.Second))

		if blockLevel < maxLevel {
			blockAtDepthMAtNextLevel, err := ppm.blockAtDepth(stagingArea, ghostdagDataStores[blockLevel+1], selectedTipByLevel[blockLevel+1], ppm.pruningProofM)
			if err != nil {
				return err
			}

			hasBlockAtDepthMAtNextLevel, err := blockRelationStores[blockLevel].Has(ppm.databaseContext, stagingArea, blockAtDepthMAtNextLevel)
			if err != nil {
				return err
			}

			if !hasBlockAtDepthMAtNextLevel {
				return errors.Wrapf(ruleerrors.ErrPruningProofMissingBlockAtDepthMFromNextLevel, "proof level %d "+
					"is missing the block at depth m in level %d", blockLevel, blockLevel+1)
			}
		}

		if !selectedTip.Equal(pruningPoint) && !ppm.parentsManager.ParentsAtLevel(pruningPointHeader, blockLevel).Contains(selectedTip) {
			return errors.Wrapf(ruleerrors.ErrPruningProofMissesBlocksBelowPruningPoint, "the selected tip %s at "+
				"level %d is not a parent of the pruning point", selectedTip, blockLevel)
		}
		selectedTipByLevel[blockLevel] = selectedTip
	}

	currentDAGPruningPoint, err := ppm.pruningStore.PruningPoint(ppm.databaseContext, model.NewStagingArea())
	if err != nil {
		return err
	}

	currentDAGPruningPointHeader, err := ppm.blockHeaderStore.BlockHeader(ppm.databaseContext, model.NewStagingArea(), currentDAGPruningPoint)
	if err != nil {
		return err
	}

	for blockLevel, selectedTip := range selectedTipByLevel {
		if blockLevel <= pruningPointBlockLevel {
			if !selectedTip.Equal(consensushashing.HeaderHash(pruningPointHeader)) {
				return errors.Wrapf(ruleerrors.ErrPruningProofSelectedTipIsNotThePruningPoint, "the pruning "+
					"proof selected tip %s at level %d is not the pruning point", selectedTip, blockLevel)
			}
		} else if !ppm.parentsManager.ParentsAtLevel(pruningPointHeader, blockLevel).Contains(selectedTip) {
			return errors.Wrapf(ruleerrors.ErrPruningProofSelectedTipNotParentOfPruningPoint, "the pruning "+
				"proof selected tip %s at level %d is not a parent of the of the pruning point on the same "+
				"level", selectedTip, blockLevel)
		}

		selectedTipGHOSTDAGData, err := ghostdagDataStores[blockLevel].Get(ppm.databaseContext, stagingArea, selectedTip, false)
		if err != nil {
			return err
		}

		if selectedTipGHOSTDAGData.BlueScore() < 2*ppm.pruningProofM {
			continue
		}

		current := selectedTip
		currentGHOSTDAGData := selectedTipGHOSTDAGData
		var commonAncestor *externalapi.DomainHash
		var commonAncestorGHOSTDAGData *externalapi.BlockGHOSTDAGData
		var currentDAGCommonAncestorGHOSTDAGData *externalapi.BlockGHOSTDAGData
		for {
			currentDAGHOSTDAGData, err := ppm.ghostdagDataStores[blockLevel].Get(ppm.databaseContext, model.NewStagingArea(), current, false)
			if err == nil {
				commonAncestor = current
				commonAncestorGHOSTDAGData = currentGHOSTDAGData
				currentDAGCommonAncestorGHOSTDAGData = currentDAGHOSTDAGData
				break
			}

			if !database.IsNotFoundError(err) {
				return err
			}

			current = currentGHOSTDAGData.SelectedParent()
			if current.Equal(model.VirtualGenesisBlockHash) {
				break
			}

			currentGHOSTDAGData, err = ghostdagDataStores[blockLevel].Get(ppm.databaseContext, stagingArea, current, false)
			if err != nil {
				return err
			}
		}

		if commonAncestor != nil {
			selectedTipBlueWorkDiff := big.NewInt(0).Sub(selectedTipGHOSTDAGData.BlueWork(), commonAncestorGHOSTDAGData.BlueWork())
			currentDAGPruningPointParents := ppm.parentsManager.ParentsAtLevel(currentDAGPruningPointHeader, blockLevel)

			foundBetterParent := false
			for _, parent := range currentDAGPruningPointParents {
				parentGHOSTDAGData, err := ppm.ghostdagDataStores[blockLevel].Get(ppm.databaseContext, model.NewStagingArea(), parent, false)
				if err != nil {
					return err
				}

				parentBlueWorkDiff := big.NewInt(0).Sub(parentGHOSTDAGData.BlueWork(), currentDAGCommonAncestorGHOSTDAGData.BlueWork())
				if parentBlueWorkDiff.Cmp(selectedTipBlueWorkDiff) >= 0 {
					foundBetterParent = true
					break
				}
			}

			if foundBetterParent {
				return errors.Wrapf(ruleerrors.ErrPruningProofInsufficientBlueWork, "the proof doesn't "+
					"have sufficient blue work in order to replace the current DAG")
			}
			return nil
		}
	}

	for blockLevel := maxLevel; blockLevel >= 0; blockLevel-- {
		currentDAGPruningPointParents, err := ppm.dagTopologyManagers[blockLevel].Parents(model.NewStagingArea(), currentDAGPruningPoint)
		// If the current pruning point doesn't have a parent at this level, we consider the proof state to be better.
		if database.IsNotFoundError(err) {
			return nil
		}
		if err != nil {
			return err
		}

		for _, parent := range currentDAGPruningPointParents {
			parentGHOSTDAGData, err := ppm.ghostdagDataStores[blockLevel].Get(ppm.databaseContext, model.NewStagingArea(), parent, false)
			if err != nil {
				return err
			}

			if parentGHOSTDAGData.BlueScore() < 2*ppm.pruningProofM {
				return nil
			}
		}
	}

	return errors.Wrapf(ruleerrors.ErrPruningProofInsufficientBlueWork, "the pruning proof doesn't have any "+
		"shared blocks with the known DAGs, but doesn't have enough headers from levels higher than the existing block levels.")
}

func (ppm *pruningProofManager) dagStores(maxLevel int) (model.BlockHeaderStore, []model.BlockRelationStore, []model.ReachabilityDataStore, []model.GHOSTDAGDataStore, error) {
	blockRelationStores := make([]model.BlockRelationStore, maxLevel+1)
	reachabilityDataStores := make([]model.ReachabilityDataStore, maxLevel+1)
	ghostdagDataStores := make([]model.GHOSTDAGDataStore, maxLevel+1)

	prefix := consensusDB.MakeBucket([]byte("pruningProofManager"))
	blockHeaderStore, err := blockheaderstore.New(ppm.databaseContext, prefix, 0, false)
	if err != nil {
		return nil, nil, nil, nil, err
	}

	for i := 0; i <= maxLevel; i++ {
		blockRelationStores[i] = blockrelationstore.New(prefix, 0, false)
		reachabilityDataStores[i] = reachabilitydatastore.New(prefix, 0, false)
		ghostdagDataStores[i] = ghostdagdatastore.New(prefix, 0, false)
	}

	return blockHeaderStore, blockRelationStores, reachabilityDataStores, ghostdagDataStores, nil
}

func (ppm *pruningProofManager) dagProcesses(
	maxLevel int,
	blockHeaderStore model.BlockHeaderStore,
	blockRelationStores []model.BlockRelationStore,
	reachabilityDataStores []model.ReachabilityDataStore,
	ghostdagDataStores []model.GHOSTDAGDataStore) (
	[]model.ReachabilityManager,
	[]model.DAGTopologyManager,
	[]model.GHOSTDAGManager,
) {
	reachabilityManagers := make([]model.ReachabilityManager, ppm.maxBlockLevel+1)
	dagTopologyManagers := make([]model.DAGTopologyManager, ppm.maxBlockLevel+1)
	ghostdagManagers := make([]model.GHOSTDAGManager, ppm.maxBlockLevel+1)

	for i := 0; i <= maxLevel; i++ {
		reachabilityManagers[i] = reachabilitymanager.New(
			ppm.databaseContext,
			ghostdagDataStores[i],
			reachabilityDataStores[i])

		dagTopologyManagers[i] = dagtopologymanager.New(
			ppm.databaseContext,
			reachabilityManagers[i],
			blockRelationStores[i],
			ghostdagDataStores[i])

		ghostdagManagers[i] = ghostdagmanager.New(
			ppm.databaseContext,
			dagTopologyManagers[i],
			nil,
			ghostdagDataStores[i],
			blockHeaderStore,
			nil,
			ppm.k,
			ppm.genesisHash)
	}

	return reachabilityManagers, dagTopologyManagers, ghostdagManagers
}

func (ppm *pruningProofManager) populateProofReachabilityAndHeaders(pruningPointProof *externalapi.PruningPointProof,
	targetReachabilityDataStore model.ReachabilityDataStore,
) error {
	// We build a DAG of all multi-level relations between blocks in the proof. We make a upHeap of all blocks, so we can iterate
	// over them in a topological way, and then build a DAG where we use all multi-level parents of a block to create edges, except
	// parents that are already in the past of another parent (This can happen between two levels). We run GHOSTDAG on each block of
	// that DAG, because GHOSTDAG is a requirement to calculate reachability. We then dismiss the GHOSTDAG data because it's not related
	// to the GHOSTDAG data of the real DAG, and was used only for reachability.

	// We need two staging areas: stagingArea which is used to commit the reachability data, and tmpStagingArea for the GHOSTDAG data
	// of allProofBlocksUpHeap. The reason we need two areas is that we use the real GHOSTDAG data in order to order the heap in a topological
	// way, and fake GHOSTDAG data for calculating reachability.
	stagingArea := model.NewStagingArea()
	tmpStagingArea := model.NewStagingArea()

	bucket := consensusDB.MakeBucket([]byte("TMP"))
	ghostdagDataStoreForTargetReachabilityManager := ghostdagdatastore.New(bucket, 0, false)
	ghostdagDataStoreForTargetReachabilityManager.Stage(stagingArea, model.VirtualGenesisBlockHash, externalapi.NewBlockGHOSTDAGData(
		0,
		big.NewInt(0),
		nil,
		nil,
		nil,
		nil,
	), false)
	targetReachabilityManager := reachabilitymanager.New(ppm.databaseContext, ghostdagDataStoreForTargetReachabilityManager, targetReachabilityDataStore)
	blockRelationStoreForTargetReachabilityManager := blockrelationstore.New(bucket, 0, false)
	dagTopologyManagerForTargetReachabilityManager := dagtopologymanager.New(ppm.databaseContext, targetReachabilityManager, blockRelationStoreForTargetReachabilityManager, nil)
	ghostdagManagerForTargetReachabilityManager := ghostdagmanager.New(ppm.databaseContext, dagTopologyManagerForTargetReachabilityManager, nil, ghostdagDataStoreForTargetReachabilityManager, ppm.blockHeaderStore, nil, ppm.k, nil)
	err := dagTopologyManagerForTargetReachabilityManager.SetParents(stagingArea, model.VirtualGenesisBlockHash, nil)
	if err != nil {
		return err
	}

	dagTopologyManager := dagtopologymanager.New(ppm.databaseContext, targetReachabilityManager, nil, nil)
	ghostdagDataStore := ghostdagdatastore.New(bucket, 0, false)
	tmpGHOSTDAGManager := ghostdagmanager.New(ppm.databaseContext, nil, nil, ghostdagDataStore, nil, nil, []externalapi.KType{0}, nil)
	dagTraversalManager := dagtraversalmanager.New(ppm.databaseContext, nil, ghostdagDataStore, nil, tmpGHOSTDAGManager, nil, nil, nil, []int{0})
	allProofBlocksUpHeap := dagTraversalManager.NewUpHeap(tmpStagingArea)
	type proofBlock struct {
		parents []externalapi.DomainHash
	}
	dag := make(map[externalapi.DomainHash]proofBlock)
	hashPtrByValue := make(map[externalapi.DomainHash]*externalapi.DomainHash)
	for _, headers := range pruningPointProof.Headers {
		for _, header := range headers {
			blockHash := consensushashing.HeaderHash(header)
			if _, ok := dag[*blockHash]; ok {
				continue
			}

			hashPtrByValue[*blockHash] = blockHash
			parents := make([]externalapi.DomainHash, 0, ppm.maxBlockLevel+1)

			for level := 0; level <= ppm.maxBlockLevel; level++ {
				for _, parent := range ppm.parentsManager.ParentsAtLevel(header, level) {
					parents = append(parents, *parent)
				}
			}

			dag[*blockHash] = proofBlock{parents: parents}

			// We stage temporary GHOSTDAG data that is needed in order to sort allProofBlocksUpHeap.
			ghostdagDataStore.Stage(tmpStagingArea, blockHash, externalapi.NewBlockGHOSTDAGData(header.BlueScore(), header.BlueWork(), nil, nil, nil, nil), false)
			err := allProofBlocksUpHeap.Push(blockHash)
			if err != nil {
				return err
			}
		}
	}
	log.Infof("Pruning proof reachability: building temporary DAG for %d unique proof blocks", len(dag))

	var selectedTip *externalapi.DomainHash
	startTime := time.Now()
	lastProgressLogTime := time.Now()
	processed := 0
	totalBlocks := allProofBlocksUpHeap.Len()
	for allProofBlocksUpHeap.Len() > 0 {
		blockHash := allProofBlocksUpHeap.Pop()
		processed++
		block := dag[*blockHash]
		parentsHeap := dagTraversalManager.NewDownHeap(tmpStagingArea)
		for _, parentValue := range block.parents {
			parentHash, ok := hashPtrByValue[parentValue]
			if !ok {
				continue
			}

			err := parentsHeap.Push(parentHash)
			if err != nil {
				return err
			}
		}

		fakeParents := []*externalapi.DomainHash{}
		for parentsHeap.Len() > 0 {
			parent := parentsHeap.Pop()
			isAncestorOfAny, err := dagTopologyManager.IsAncestorOfAny(stagingArea, parent, fakeParents)
			if err != nil {
				return err
			}

			if isAncestorOfAny {
				continue
			}

			fakeParents = append(fakeParents, parent)
		}

		if len(fakeParents) == 0 {
			fakeParents = append(fakeParents, model.VirtualGenesisBlockHash)
		}

		err := dagTopologyManagerForTargetReachabilityManager.SetParents(stagingArea, blockHash, fakeParents)
		if err != nil {
			return err
		}

		err = ghostdagManagerForTargetReachabilityManager.GHOSTDAG(stagingArea, blockHash)
		if err != nil {
			return err
		}

		err = targetReachabilityManager.AddBlock(stagingArea, blockHash)
		if err != nil {
			return err
		}

		if selectedTip == nil {
			selectedTip = blockHash
		} else {
			selectedTip, err = ghostdagManagerForTargetReachabilityManager.ChooseSelectedParent(stagingArea, selectedTip, blockHash)
			if err != nil {
				return err
			}
		}

		if selectedTip.Equal(blockHash) && (processed%pruningProofReindexRootUpdateInterval == 0 || processed == totalBlocks) {
			err := targetReachabilityManager.UpdateReindexRoot(stagingArea, selectedTip)
			if err != nil {
				return err
			}
		}

		if totalBlocks > 0 && time.Since(lastProgressLogTime) >= pruningProofProgressLogInterval {
			elapsed := time.Since(startTime)
			rate := float64(processed) / elapsed.Seconds()
			eta := time.Duration(0)
			if rate > 0 {
				eta = time.Duration(float64(totalBlocks-processed)/rate) * time.Second
			}
			log.Infof("Pruning proof reachability progress: %d/%d (%.1f%%) elapsed=%s rate=%.0f blk/s eta~%s",
				processed, totalBlocks, 100*float64(processed)/float64(totalBlocks), elapsed.Truncate(time.Second), rate, eta.Truncate(time.Second))
			lastProgressLogTime = time.Now()
		}
	}
	log.Infof("Pruning proof reachability: finished (blocks=%d duration=%s)", processed, time.Since(startTime).Truncate(time.Second))

	commitStartTime := time.Now()
	log.Infof("Pruning proof reachability: committing reachability data")
	err = staging.CommitAllChanges(ppm.databaseContext, stagingArea)
	if err != nil {
		return err
	}
	log.Infof("Pruning proof reachability: committed reachability data (duration=%s)", time.Since(commitStartTime).Truncate(time.Second))

	ghostdagDataStoreForTargetReachabilityManager.UnstageAll(stagingArea)
	blockRelationStoreForTargetReachabilityManager.UnstageAll(stagingArea)
	ghostdagDataStore.UnstageAll(tmpStagingArea)

	// Clear references to aid GC in low-memory or no-GC environments

	return nil
}

// ApplyPruningPointProof applies the given pruning proof to the current consensus. Specifically,
// it's meant to be used against the StagingConsensus during headers-proof IBD. Note that for
// performance reasons this operation is NOT atomic. If the process fails for whatever reason
// (e.g. the process was killed) then the database for this consensus MUST be discarded.
func (ppm *pruningProofManager) ApplyPruningPointProof(pruningPointProof *externalapi.PruningPointProof) error {
	onEnd := logger.LogAndMeasureExecutionTime(log, "ApplyPruningPointProof")
	defer onEnd()

	stagingArea := model.NewStagingArea()
	stageStartTime := time.Now()
	totalHeaders := 0
	for _, headers := range pruningPointProof.Headers {
		totalHeaders += len(headers)
		for _, header := range headers {
			blockHash := consensushashing.HeaderHash(header)
			ppm.blockHeaderStore.Stage(stagingArea, blockHash, header)
		}
	}
	log.Infof("Applying pruning point proof: staging %d headers", totalHeaders)
	err := staging.CommitAllChanges(ppm.databaseContext, stagingArea)
	if err != nil {
		return err
	}
	log.Infof("Applying pruning point proof: staged headers committed (duration=%s)", time.Since(stageStartTime).Truncate(time.Second))

	err = ppm.populateProofReachabilityAndHeaders(pruningPointProof, ppm.reachabilityDataStore)
	if err != nil {
		return err
	}

	for blockLevel, headers := range pruningPointProof.Headers {
		levelStartTime := time.Now()
		totalLevelHeaders := len(headers)
		log.Infof("Applying level %d from the pruning point proof (%d headers)", blockLevel, totalLevelHeaders)
		lastProgressLogTime := time.Now()
		for i, header := range headers {
			stagingArea := model.NewStagingArea()

			blockHash := consensushashing.HeaderHash(header)
			if header.BlockLevel(ppm.maxBlockLevel) < blockLevel {
				return errors.Wrapf(ruleerrors.ErrPruningProofWrongBlockLevel, "block %s level is %d when it's "+
					"expected to be at least %d", blockHash, header.BlockLevel(ppm.maxBlockLevel), blockLevel)
			}

			ppm.blockHeaderStore.Stage(stagingArea, blockHash, header)

			var parents []*externalapi.DomainHash
			for _, parent := range ppm.parentsManager.ParentsAtLevel(header, blockLevel) {
				_, err := ppm.ghostdagDataStores[blockLevel].Get(ppm.databaseContext, stagingArea, parent, false)
				if database.IsNotFoundError(err) {
					continue
				}
				if err != nil {
					return err
				}

				parents = append(parents, parent)
			}

			if len(parents) == 0 {
				if i != 0 {
					return errors.Wrapf(ruleerrors.ErrPruningProofHeaderWithNoKnownParents, "the proof header "+
						"%s is missing known parents", blockHash)
				}
				parents = append(parents, model.VirtualGenesisBlockHash)
			}

			err := ppm.dagTopologyManagers[blockLevel].SetParents(stagingArea, blockHash, parents)
			if err != nil {
				return err
			}

			err = ppm.ghostdagManagers[blockLevel].GHOSTDAG(stagingArea, blockHash)
			if err != nil {
				return err
			}

			if blockLevel == 0 {
				// Override the ghostdag data with the real blue score and blue work
				ghostdagData, err := ppm.ghostdagDataStores[0].Get(ppm.databaseContext, stagingArea, blockHash, false)
				if err != nil {
					return err
				}

				ppm.ghostdagDataStores[0].Stage(stagingArea, blockHash, externalapi.NewBlockGHOSTDAGData(
					header.BlueScore(),
					header.BlueWork(),
					ghostdagData.SelectedParent(),
					ghostdagData.MergeSetBlues(),
					ghostdagData.MergeSetReds(),
					ghostdagData.BluesAnticoneSizes(),
				), false)

				ppm.finalityStore.StageFinalityPoint(stagingArea, blockHash, model.VirtualGenesisBlockHash)
				ppm.blockStatusStore.Stage(stagingArea, blockHash, externalapi.StatusHeaderOnly)
			}

			err = staging.CommitAllChanges(ppm.databaseContext, stagingArea)
			if err != nil {
				return err
			}

			if totalLevelHeaders > 0 && time.Since(lastProgressLogTime) >= pruningProofProgressLogInterval {
				processed := i + 1
				elapsed := time.Since(levelStartTime)
				rate := float64(processed) / elapsed.Seconds()
				eta := time.Duration(0)
				if rate > 0 {
					eta = time.Duration(float64(totalLevelHeaders-processed)/rate) * time.Second
				}
				log.Infof("Pruning proof apply level %d progress: %d/%d (%.1f%%) elapsed=%s rate=%.0f hdr/s eta~%s",
					blockLevel, processed, totalLevelHeaders, 100*float64(processed)/float64(totalLevelHeaders), elapsed.Truncate(time.Second), rate, eta.Truncate(time.Second))
				lastProgressLogTime = time.Now()
			}
		}
		log.Infof("Finished applying level %d from the pruning point proof (headers=%d duration=%s)",
			blockLevel, totalLevelHeaders, time.Since(levelStartTime).Truncate(time.Second))
	}

	pruningPointHeader := pruningPointProof.Headers[0][len(pruningPointProof.Headers[0])-1]
	pruningPoint := consensushashing.HeaderHash(pruningPointHeader)

	stagingArea = model.NewStagingArea()
	ppm.consensusStateStore.StageTips(stagingArea, []*externalapi.DomainHash{pruningPoint})
	return staging.CommitAllChanges(ppm.databaseContext, stagingArea)
}
