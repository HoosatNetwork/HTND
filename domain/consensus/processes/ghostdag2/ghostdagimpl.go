package ghostdag2

import (
	"sort"

	"github.com/Hoosat-Oy/HTND/util/difficulty"

	"math/big"

	"github.com/Hoosat-Oy/HTND/domain/consensus/database"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/constants"
)

type ghostdagHelper struct {
	k                   []externalapi.KType
	dataStore           model.GHOSTDAGDataStore
	dbAccess            model.DBReader
	dagTopologyManager  model.DAGTopologyManager
	dagTraversalManager model.DAGTraversalManager
	headerStore         model.BlockHeaderStore
	consensusStateStore model.ConsensusStateStore
}

// SetDAGTraversalManager sets the dagTraversalManager
func (gh *ghostdagHelper) SetDAGTraversalManager(dagTraversalManager model.DAGTraversalManager) {
	gh.dagTraversalManager = dagTraversalManager
}

// New creates a new instance of this alternative ghostdag impl
func New(
	databaseContext model.DBReader,
	dagTopologyManager model.DAGTopologyManager,
	dagTraversalManager model.DAGTraversalManager,
	ghostdagDataStore model.GHOSTDAGDataStore,
	headerStore model.BlockHeaderStore,
	consensusStateStore model.ConsensusStateStore,
	k []externalapi.KType,
	genesisHash *externalapi.DomainHash) model.GHOSTDAGManager {

	return &ghostdagHelper{
		dbAccess:            databaseContext,
		dagTopologyManager:  dagTopologyManager,
		dagTraversalManager: dagTraversalManager,
		dataStore:           ghostdagDataStore,
		headerStore:         headerStore,
		consensusStateStore: consensusStateStore,
		k:                   k,
	}
}

/* --------------------------------------------- */

func (gh *ghostdagHelper) GHOSTDAG(stagingArea *model.StagingArea, blockCandidate *externalapi.DomainHash) error {
	myWork := new(big.Int)
	maxWork := new(big.Int)
	var myScore uint64
	var spScore uint64
	/* find the selectedParent */
	blockParents, err := gh.dagTopologyManager.Parents(stagingArea, blockCandidate)
	if err != nil {
		return err
	}
	var selectedParent = blockParents[0]
	for _, parent := range blockParents {
		blockData, err := gh.dataStore.Get(gh.dbAccess, stagingArea, parent, false)
		if database.IsNotFoundError(err) {
			log.Infof("GHOSTDAG failed to retrieve with %s\n", parent)
			return err
		}
		if err != nil {
			return err
		}
		blockWork := blockData.BlueWork()
		blockScore := blockData.BlueScore()
		if blockWork.Cmp(maxWork) == 1 {
			selectedParent = parent
			maxWork = blockWork
			spScore = blockScore
		}
		if blockWork.Cmp(maxWork) == 0 && ismoreHash(parent, selectedParent) {
			selectedParent = parent
			maxWork = blockWork
			spScore = blockScore
		}
	}
	myWork.Set(maxWork)
	myScore = spScore

	/* Goal: iterate blockCandidate's mergeSet and divide it to : blue, blues, reds. */
	var mergeSetBlues = make([]*externalapi.DomainHash, 0)
	var mergeSetReds = make([]*externalapi.DomainHash, 0)
	var blueSet = make([]*externalapi.DomainHash, 0)

	mergeSetBlues = append(mergeSetBlues, selectedParent)

	mergeSetArr, err := gh.findMergeSet(stagingArea, blockParents, selectedParent)
	if err != nil {
		return err
	}

	err = gh.sortByBlueWork(stagingArea, mergeSetArr)
	if err != nil {
		return err
	}
	err = gh.findBlueSet(stagingArea, &blueSet, selectedParent)
	if err != nil {
		return err
	}

	for _, mergeSetBlock := range mergeSetArr {
		if mergeSetBlock.Equal(selectedParent) {
			if !contains(selectedParent, mergeSetBlues) {
				mergeSetBlues = append(mergeSetBlues, selectedParent)
				blueSet = append(blueSet, selectedParent)
			}
			continue
		}
		err := gh.divideBlueRed(stagingArea, selectedParent, mergeSetBlock, &mergeSetBlues, &mergeSetReds, &blueSet)
		if err != nil {
			return err
		}
	}
	myScore += uint64(len(mergeSetBlues))

	// We add up all the *work*(not blueWork) that all our blues and selected parent did
	for _, blue := range mergeSetBlues {
		header, err := gh.headerStore.BlockHeader(gh.dbAccess, stagingArea, blue)
		if err != nil {
			return err
		}
		myWork.Add(myWork, difficulty.CalcWork(header.Bits()))
	}

	e := externalapi.NewBlockGHOSTDAGData(myScore, myWork, selectedParent, mergeSetBlues, mergeSetReds, nil)
	gh.dataStore.Stage(stagingArea, blockCandidate, e, false)
	return nil
}

/* --------isMoreHash(w, selectedParent)----------------*/
func ismoreHash(parent *externalapi.DomainHash, selectedParent *externalapi.DomainHash) bool {
	parentByteArray := parent.ByteArray()
	selectedParentByteArray := selectedParent.ByteArray()
	//Check if parentHash is more then selectedParentHash
	for i := 0; i < len(parentByteArray); i++ {
		switch {
		case parentByteArray[i] < selectedParentByteArray[i]:
			return false
		case parentByteArray[i] > selectedParentByteArray[i]:
			return true
		}
	}
	return false
}

/*  1. blue = selectedParent.blue + blues
    2. not connected to at most K blocks (from the blue group)
    3. for each block at blue , check if not destroy
*/

/* ---------------divideBluesReds--------------------- */
func (gh *ghostdagHelper) divideBlueRed(stagingArea *model.StagingArea,
	selectedParent *externalapi.DomainHash, desiredBlock *externalapi.DomainHash,
	blues *[]*externalapi.DomainHash, reds *[]*externalapi.DomainHash, blueSet *[]*externalapi.DomainHash) error {

	var k = int(gh.k[constants.GetBlockVersion()-1])
	counter := 0

	var suspectsBlues = make([]*externalapi.DomainHash, 0)
	isMergeBlue := true
	//check that not-connected to at most k.
	for _, block := range *blueSet {
		isAnticone, err := gh.isAnticone(stagingArea, block, desiredBlock)
		if err != nil {
			return err
		}
		if isAnticone {
			counter++
			suspectsBlues = append(suspectsBlues, block)
		}
		if counter > k {
			isMergeBlue = false
			break
		}
	}
	if !isMergeBlue {
		if !contains(desiredBlock, *reds) {
			*reds = append(*reds, desiredBlock)
		}
		return nil
	}

	// check that the k-cluster of each blue is still valid.
	for _, blue := range suspectsBlues {
		isDestroyed, err := gh.checkIfDestroy(stagingArea, blue, blueSet)
		if err != nil {
			return err
		}
		if isDestroyed {
			isMergeBlue = false
			break
		}
	}
	if !isMergeBlue {
		if !contains(desiredBlock, *reds) {
			*reds = append(*reds, desiredBlock)
		}
		return nil
	}
	if !contains(desiredBlock, *blues) {
		*blues = append(*blues, desiredBlock)
	}
	if !contains(desiredBlock, *blueSet) {
		*blueSet = append(*blueSet, desiredBlock)
	}
	return nil
}

/* ---------------isAnticone-------------------------- */
func (gh *ghostdagHelper) isAnticone(stagingArea *model.StagingArea, blockA, blockB *externalapi.DomainHash) (bool, error) {
	// Check if blockA is ancestor of blockB or vice versa
	isAAncestorOfB, err := gh.dagTopologyManager.IsAncestorOf(stagingArea, blockA, blockB)
	if err != nil {
		return false, err
	}
	if isAAncestorOfB {
		return false, nil
	}

	isBAncestorOfA, err := gh.dagTopologyManager.IsAncestorOf(stagingArea, blockB, blockA)
	if err != nil {
		return false, err
	}
	return !isBAncestorOfA, nil
}

/*----------------contains-------------------------- */
func contains(item *externalapi.DomainHash, items []*externalapi.DomainHash) bool {
	for _, r := range items {
		if r.Equal(item) {
			return true
		}
	}
	return false
}

/* ----------------checkIfDestroy------------------- */
/* find number of not-connected in his blue*/
func (gh *ghostdagHelper) checkIfDestroy(stagingArea *model.StagingArea, blockBlue *externalapi.DomainHash,
	blueSet *[]*externalapi.DomainHash) (bool, error) {

	// Goal: check that the K-cluster of each block in the blueSet is not destroyed when adding the block to the mergeSet.
	var k = int(gh.k[constants.GetBlockVersion()-1])
	counter := 0
	for _, blue := range *blueSet {
		isAnticone, err := gh.isAnticone(stagingArea, blue, blockBlue)
		if err != nil {
			return true, err
		}
		if isAnticone {
			counter++
		}
		if counter > k {
			return true, nil
		}
	}
	return false, nil
}

/* ----------------findMergeSet------------------- */
func (gh *ghostdagHelper) findMergeSet(stagingArea *model.StagingArea, parents []*externalapi.DomainHash,
	selectedParent *externalapi.DomainHash) ([]*externalapi.DomainHash, error) {

	allMergeSet := make([]*externalapi.DomainHash, 0)
	blockQueue := make([]*externalapi.DomainHash, 0)
	for _, parent := range parents {
		if !contains(parent, blockQueue) {
			blockQueue = append(blockQueue, parent)
		}

	}
	for len(blockQueue) > 0 {
		block := blockQueue[0]
		blockQueue = blockQueue[1:]
		if selectedParent.Equal(block) {
			if !contains(block, allMergeSet) {
				allMergeSet = append(allMergeSet, block)
			}
			continue
		}
		isancestorOf, err := gh.dagTopologyManager.IsAncestorOf(stagingArea, block, selectedParent)
		if err != nil {
			return nil, err
		}
		if isancestorOf {
			continue
		}
		if !contains(block, allMergeSet) {
			allMergeSet = append(allMergeSet, block)
		}
		err = gh.insertParent(stagingArea, block, &blockQueue)
		if err != nil {
			return nil, err
		}

	}
	return allMergeSet, nil
}

/* ----------------insertParent------------------- */
/* Insert all parents to the queue*/
func (gh *ghostdagHelper) insertParent(stagingArea *model.StagingArea, child *externalapi.DomainHash,
	queue *[]*externalapi.DomainHash) error {

	parents, err := gh.dagTopologyManager.Parents(stagingArea, child)
	if err != nil {
		return err
	}
	for _, parent := range parents {
		if contains(parent, *queue) {
			continue
		}
		*queue = append(*queue, parent)
	}
	return nil
}

/* ----------------findBlueSet------------------- */
func (gh *ghostdagHelper) findBlueSet(stagingArea *model.StagingArea, blueSet *[]*externalapi.DomainHash, selectedParent *externalapi.DomainHash) error {
	for selectedParent != nil {
		if !contains(selectedParent, *blueSet) {
			*blueSet = append(*blueSet, selectedParent)
		}
		blockData, err := gh.dataStore.Get(gh.dbAccess, stagingArea, selectedParent, false)
		if database.IsNotFoundError(err) {
			log.Infof("findBlueSet failed to retrieve with %s\n", selectedParent)
			return err
		}
		if err != nil {
			return err
		}
		mergeSetBlue := blockData.MergeSetBlues()
		for _, blue := range mergeSetBlue {
			if contains(blue, *blueSet) {
				continue
			}
			*blueSet = append(*blueSet, blue)
		}
		selectedParent = blockData.SelectedParent()
	}
	return nil
}

/* ----------------sortByBlueScore------------------- */
func (gh *ghostdagHelper) sortByBlueWork(stagingArea *model.StagingArea, arr []*externalapi.DomainHash) error {

	var err error = nil
	sort.Slice(arr, func(i, j int) bool {

		blockLeft, error := gh.dataStore.Get(gh.dbAccess, stagingArea, arr[i], false)
		if error != nil {
			err = error
			return false
		}

		blockRight, error := gh.dataStore.Get(gh.dbAccess, stagingArea, arr[j], false)
		if database.IsNotFoundError(err) {
			log.Infof("sortByBlueWork failed to retrieve with %s\n", arr[j])
			return false
		}
		if error != nil {
			err = error
			return false
		}

		if blockLeft.BlueWork().Cmp(blockRight.BlueWork()) == -1 {
			return true
		}
		if blockLeft.BlueWork().Cmp(blockRight.BlueWork()) == 0 {
			return ismoreHash(arr[j], arr[i])
		}
		return false
	})
	return err
}

/* --------------------------------------------- */

func (gh *ghostdagHelper) BlockData(stagingArea *model.StagingArea, blockHash *externalapi.DomainHash) (*externalapi.BlockGHOSTDAGData, error) {
	return gh.dataStore.Get(gh.dbAccess, stagingArea, blockHash, false)
}
func (gh *ghostdagHelper) ChooseSelectedParent(stagingArea *model.StagingArea, blockHashes ...*externalapi.DomainHash) (*externalapi.DomainHash, error) {
	panic("implement me")
}

func (gh *ghostdagHelper) Less(blockHashA *externalapi.DomainHash, ghostdagDataA *externalapi.BlockGHOSTDAGData, blockHashB *externalapi.DomainHash, ghostdagDataB *externalapi.BlockGHOSTDAGData) bool {
	panic("implement me")
}

func (gh *ghostdagHelper) GetSortedMergeSet(*model.StagingArea, *externalapi.DomainHash) ([]*externalapi.DomainHash, error) {
	panic("implement me")
}

func (gh *ghostdagHelper) OrderDAG(stagingArea *model.StagingArea, G []*externalapi.DomainHash) (*externalapi.DomainHash, []*externalapi.DomainHash, error) {
	panic("implement me")
}
