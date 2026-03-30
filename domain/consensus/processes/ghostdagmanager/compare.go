package ghostdagmanager

import (
	"github.com/Hoosat-Oy/HTND/domain/consensus/database"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
)

func (gm *ghostdagManager) findSelectedParent(stagingArea *model.StagingArea, parentHashes []*externalapi.DomainHash) (
	*externalapi.DomainHash, error,
) {
	var selectedParent *externalapi.DomainHash
	for _, hash := range parentHashes {
		if selectedParent == nil {
			selectedParent = hash
			continue
		}
		isHashBiggerThanSelectedParent, err := gm.less(stagingArea, selectedParent, hash)
		if err != nil {
			return nil, err
		}
		if isHashBiggerThanSelectedParent {
			selectedParent = hash
		}
	}
	return selectedParent, nil
}

func (gm *ghostdagManager) less(stagingArea *model.StagingArea, blockHashA, blockHashB *externalapi.DomainHash) (bool, error) {
	chosenSelectedParent, err := gm.ChooseSelectedParent(stagingArea, blockHashA, blockHashB)
	if err != nil {
		return false, err
	}
	return chosenSelectedParent == blockHashB, nil
}

func (gm *ghostdagManager) ChooseSelectedParent(stagingArea *model.StagingArea, blockHashes ...*externalapi.DomainHash) (*externalapi.DomainHash, error) {
	selectedParent := blockHashes[0]
	selectedParentGHOSTDAGData, err := gm.ghostdagDataStore.Get(gm.databaseContext, stagingArea, selectedParent, false)
	if database.IsNotFoundError(err) {
		log.Infof("ChooseSelectedParent failed to retrieve with %s\n", selectedParent)
		return nil, err
	}
	if err != nil {
		return nil, err
	}
	for _, blockHash := range blockHashes {
		blockGHOSTDAGData, err := gm.ghostdagDataStore.Get(gm.databaseContext, stagingArea, blockHash, false)
		if database.IsNotFoundError(err) {
			log.Infof("ChooseSelectedParent failed to retrieve with %s\n", blockHash)
			return nil, err
		}
		if err != nil {
			return nil, err
		}

		if gm.Less(selectedParent, selectedParentGHOSTDAGData, blockHash, blockGHOSTDAGData) {
			selectedParent = blockHash
			selectedParentGHOSTDAGData = blockGHOSTDAGData
		}
	}

	return selectedParent, nil
}

func (gm *ghostdagManager) Less(blockHashA *externalapi.DomainHash, ghostdagDataA *externalapi.BlockGHOSTDAGData,
	blockHashB *externalapi.DomainHash, ghostdagDataB *externalapi.BlockGHOSTDAGData,
) bool {
	switch ghostdagDataA.BlueWork().Cmp(ghostdagDataB.BlueWork()) {
	case -1:
		return true
	case 1:
		return false
	case 0:
		return blockHashA.Less(blockHashB)
	default:
		panic("big.Int.Cmp is defined to always return -1/1/0 and nothing else")
	}
}
