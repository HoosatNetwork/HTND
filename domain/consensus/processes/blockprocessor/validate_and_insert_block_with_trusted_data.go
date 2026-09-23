package blockprocessor

import (
	"fmt"

	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/infrastructure/db/database"
)

func (bp *blockProcessor) validateAndInsertBlockWithTrustedData(stagingArea *model.StagingArea,
	block *externalapi.BlockWithTrustedData, validateUTXO bool,
) (*externalapi.VirtualChangeSet, externalapi.BlockStatus, error) {
	blockHash := consensushashing.BlockHash(block.Block)
	for i, daaBlock := range block.DAAWindow {
		if i < 0 {
			return nil, externalapi.StatusInvalid, fmt.Errorf("index overflow in DAAWindow: %d", i)
		}
		index := uint64(uint(i))
		hash := consensushashing.HeaderHash(daaBlock.Header)
		bp.blocksWithTrustedDataDAAWindowStore.Stage(stagingArea, blockHash, index, &externalapi.BlockGHOSTDAGDataHashPair{
			Hash:         hash,
			GHOSTDAGData: daaBlock.GHOSTDAGData,
		})
		bp.blockHeaderStore.Stage(stagingArea, hash, daaBlock.Header)
	}

	for _, pair := range block.GHOSTDAGData {
		bp.ghostdagDataStore.Stage(stagingArea, pair.Hash, pair.GHOSTDAGData, true)
	}

	bp.daaBlocksStore.StageDAAScore(stagingArea, blockHash, block.Block.Header.DAAScore())

	blockReplacedGHOSTDAGData, err := bp.ghostdagDataWithoutPrunedBlocks(stagingArea, block.GHOSTDAGData[0].GHOSTDAGData)
	if err != nil {
		return nil, externalapi.StatusInvalid, err
	}
	bp.ghostdagDataStore.Stage(stagingArea, blockHash, blockReplacedGHOSTDAGData, false)

	err = bp.adoptTrustedGHOSTDAGDataOfProofBlocks(stagingArea, block.GHOSTDAGData[1:])
	if err != nil {
		return nil, externalapi.StatusInvalid, err
	}

	return bp.validateAndInsertBlock(stagingArea, block.Block, false, validateUTXO, true, true, true)
}

// adoptTrustedGHOSTDAGDataOfProofBlocks replaces the GHOSTDAG data of the header-only blocks below a
// trusted block with the syncer's own GHOSTDAG data for them, as sent with the trusted block.
//
// Those blocks are pruning point proof headers, and ApplyPruningPointProof colored them over the
// proof's partial DAG with the blue score and blue work written in their headers. Nothing validates
// those header values (HTN-006), and mainnet headers do misstate them, so the parents the proof
// coloring selected and the merge sets it built can differ from the syncer's. Coloring the headers
// above the imported pruning point walks down exactly these blocks - the trusted block's selected
// chain, K+1 deep, which is what the syncer sends GHOSTDAG data for - so a node that kept the
// proof-derived data could color those headers differently from the syncer (HTN-196).
//
// Blocks this node has no GHOSTDAG data for are left alone, and references to blocks it cannot
// color with are dropped the same way they are for the trusted block itself.
func (bp *blockProcessor) adoptTrustedGHOSTDAGDataOfProofBlocks(stagingArea *model.StagingArea,
	pairs []*externalapi.BlockGHOSTDAGDataHashPair,
) error {
	for _, pair := range pairs {
		isPruned, err := bp.isPruned(stagingArea, pair.Hash)
		if err != nil {
			return err
		}
		if isPruned {
			continue
		}
		status, err := bp.blockStatusStore.Get(bp.databaseContext, stagingArea, pair.Hash)
		if err != nil {
			if database.IsNotFoundError(err) {
				continue
			}
			return err
		}
		if status != externalapi.StatusHeaderOnly {
			continue
		}
		replaced, err := bp.ghostdagDataWithoutPrunedBlocks(stagingArea, pair.GHOSTDAGData)
		if err != nil {
			return err
		}
		bp.ghostdagDataStore.Stage(stagingArea, pair.Hash, replaced, false)
	}
	return nil
}

func (bp *blockProcessor) ghostdagDataWithoutPrunedBlocks(stagingArea *model.StagingArea,
	data *externalapi.BlockGHOSTDAGData,
) (*externalapi.BlockGHOSTDAGData, error) {
	mergeSetBlues := make([]*externalapi.DomainHash, 0, len(data.MergeSetBlues()))
	for _, blockHash := range data.MergeSetBlues() {
		isPruned, err := bp.isPruned(stagingArea, blockHash)
		if err != nil {
			return nil, err
		}
		if isPruned {
			if data.SelectedParent().Equal(blockHash) {
				mergeSetBlues = append(mergeSetBlues, model.VirtualGenesisBlockHash)
			}
			continue
		}

		mergeSetBlues = append(mergeSetBlues, blockHash)
	}

	mergeSetReds := make([]*externalapi.DomainHash, 0, len(data.MergeSetReds()))
	for _, blockHash := range data.MergeSetReds() {
		isPruned, err := bp.isPruned(stagingArea, blockHash)
		if err != nil {
			return nil, err
		}
		if isPruned {
			continue
		}

		mergeSetReds = append(mergeSetReds, blockHash)
	}

	selectedParent := data.SelectedParent()
	isPruned, err := bp.isPruned(stagingArea, data.SelectedParent())
	if err != nil {
		return nil, err
	}

	if isPruned {
		selectedParent = model.VirtualGenesisBlockHash
	}

	return externalapi.NewBlockGHOSTDAGData(
		data.BlueScore(),
		data.BlueWork(),
		selectedParent,
		mergeSetBlues,
		mergeSetReds,
		data.BluesAnticoneSizes(),
		data.DynamicK(),
	), nil
}

// isPruned reports whether blockHash is outside what this consensus can color with: a block with no
// GHOSTDAG data of its own. Only such a block is dropped from a trusted block's GHOSTDAG data, and
// a dropped selected parent is replaced by virtual genesis.
//
// A header-only block is NOT pruned. Every header the pruning point proof delivers is header-only,
// and those headers carry the GHOSTDAG data ApplyPruningPointProof computed for them, including the
// pruning point's real selected parent and the blocks it merges. Counting them as pruned (as this
// function did from 7b4a9248b until HTN-196's root cause was found) gave the imported pruning point
// and its anticone virtual genesis as selected parent and emptied their merge sets. GHOSTDAG's blue
// candidate walk then stopped at the pruning point, undercounted anticones and colored blocks blue
// that every other node colors red. The syncing node's blue work above the pruning point came out
// higher than the network's, often enough to pick a different selected parent, and when that parent
// was not a chain descendant of the pruning point the tip's selected chain missed the pruning point
// and met it only at virtual genesis.
func (bp *blockProcessor) isPruned(stagingArea *model.StagingArea, blockHash *externalapi.DomainHash) (bool, error) {
	_, err := bp.ghostdagDataStore.Get(bp.databaseContext, stagingArea, blockHash, false)
	if database.IsNotFoundError(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return false, nil
}
