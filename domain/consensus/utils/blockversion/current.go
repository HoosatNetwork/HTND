// Package blockversion derives the block version the chain is currently at from consensus data.
package blockversion

import (
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database"
)

// Current returns the block version the chain is at now: the higher of the versions of the virtual selected parent
// and of the headers selected tip, each derived from a DAA score this node computed itself.
//
// Parameters that follow the current version (finality and pruning depth) must be read through this at the point of
// use. Capturing them when a consensus object is built froze them at whatever the process-global block version was
// at that moment - 1 on a node that just started, the tip version on a staging consensus built during IBD - so nodes
// holding identical blocks chose different pruning and finality points. The process-global is used only while
// neither block has a DAA score yet (genesis, pruning point import).
func Current(databaseContext model.DBReader, stagingArea *model.StagingArea,
	ghostdagDataStore model.GHOSTDAGDataStore, headersSelectedTipStore model.HeaderSelectedTipStore,
	daaBlocksStore model.DAABlocksStore, powScores []uint64,
) (uint16, error) {
	found := false
	var version uint16

	consider := func(blockHash *externalapi.DomainHash) error {
		if blockHash == nil {
			return nil
		}
		daaScore, err := daaBlocksStore.DAAScore(databaseContext, stagingArea, blockHash)
		if database.IsNotFoundError(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if blockVersion := constants.BlockVersionForDAAScore(powScores, daaScore); !found || blockVersion > version {
			version = blockVersion
		}
		found = true
		return nil
	}

	virtualGHOSTDAGData, err := ghostdagDataStore.Get(databaseContext, stagingArea, model.VirtualBlockHash, false)
	if err != nil && !database.IsNotFoundError(err) {
		return 0, err
	}
	if err == nil {
		if err := consider(virtualGHOSTDAGData.SelectedParent()); err != nil {
			return 0, err
		}
	}

	headersSelectedTip, err := headersSelectedTipStore.HeadersSelectedTip(databaseContext, stagingArea)
	if err != nil && !database.IsNotFoundError(err) {
		return 0, err
	}
	if err == nil {
		if err := consider(headersSelectedTip); err != nil {
			return 0, err
		}
	}

	if !found {
		return constants.GetBlockVersion(), nil
	}
	return version, nil
}

// OfSelectedParent returns the block version derived from the DAA score, as this node computed it, of blockHash's
// selected parent. That is the version whose window size and GHOSTDAG parameters govern blockHash: the block's own DAA
// score is computed from its window, so it cannot choose it. Activation scores are far apart, so this equals the
// block's own version except at an activation boundary, and it is the same on every node. The process-global version
// is returned only without an activation table or while the needed data is missing (genesis, trusted-data bootstrap).
func OfSelectedParent(databaseContext model.DBReader, stagingArea *model.StagingArea,
	ghostdagDataStore model.GHOSTDAGDataStore, daaBlocksStore model.DAABlocksStore, powScores []uint64,
	blockHash *externalapi.DomainHash,
) (uint16, error) {
	if len(powScores) == 0 || ghostdagDataStore == nil || daaBlocksStore == nil {
		return constants.GetBlockVersion(), nil
	}
	ghostdagData, err := ghostdagDataStore.Get(databaseContext, stagingArea, blockHash, false)
	if database.IsNotFoundError(err) {
		return constants.GetBlockVersion(), nil
	}
	if err != nil {
		return 0, err
	}
	if ghostdagData.SelectedParent() == nil {
		return 1, nil
	}
	daaScore, err := daaBlocksStore.DAAScore(databaseContext, stagingArea, ghostdagData.SelectedParent())
	if database.IsNotFoundError(err) {
		return constants.GetBlockVersion(), nil
	}
	if err != nil {
		return 0, err
	}
	return constants.BlockVersionForDAAScore(powScores, daaScore), nil
}

// Index returns the index into a per-version parameter table of the given length for blockVersion, using the last
// entry when the table is shorter.
func Index(blockVersion uint16, length int) int {
	index := max(int(blockVersion)-1, 0)
	if index >= length {
		index = length - 1
	}
	return index
}
