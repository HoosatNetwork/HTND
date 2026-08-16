package ghostdagmanager

import (
	"math/big"
	"time"

	"github.com/HoosatNetwork/HTND/domain/consensus/database"
	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/util/difficulty"
	"github.com/pkg/errors"
)

type blockGHOSTDAGData struct {
	blueScore          uint64
	blueWork           *big.Int
	dynamicK           externalapi.KType
	selectedParent     *externalapi.DomainHash
	mergeSetBlues      []*externalapi.DomainHash
	mergeSetReds       []*externalapi.DomainHash
	bluesAnticoneSizes map[externalapi.DomainHash]externalapi.KType
}

func (bg *blockGHOSTDAGData) toModel() *externalapi.BlockGHOSTDAGData {
	ghostdagData := externalapi.NewBlockGHOSTDAGData(bg.blueScore, bg.blueWork, bg.selectedParent, bg.mergeSetBlues, bg.mergeSetReds, bg.bluesAnticoneSizes, bg.dynamicK)
	return ghostdagData
}

// GHOSTDAG runs the GHOSTDAG protocol and calculates the block BlockGHOSTDAGData by the given parents.
// The function calculates MergeSetBlues by iterating over the blocks in
// the anticone of the new block selected parent (which is the parent with the
// highest blue score) and adds any block to newNode.blues if by adding
// it to MergeSetBlues these conditions will not be violated:
//
// 1) |anticone-of-candidate-block ∩ blue-set-of-newBlock| ≤ K
//
//  2. For every blue block in blue-set-of-newBlock:
//     |(anticone-of-blue-block ∩ blue-set-newBlock) ∪ {candidate-block}| ≤ K.
//     We validate this condition by maintaining a map BluesAnticoneSizes for
//     each block which holds all the blue anticone sizes that were affected by
//     the new added blue blocks.
//     So to find out what is |anticone-of-blue ∩ blue-set-of-newBlock| we just iterate in
//     the selected parent chain of the new block until we find an existing entry in
//     BluesAnticoneSizes.
//
// For further details see the article https://eprint.iacr.org/2018/104.pdf
func (gm *ghostdagManager) GHOSTDAG(stagingArea *model.StagingArea, blockHash *externalapi.DomainHash) error {
	newBlockData := &blockGHOSTDAGData{
		blueWork:           new(big.Int),
		mergeSetBlues:      make([]*externalapi.DomainHash, 0),
		mergeSetReds:       make([]*externalapi.DomainHash, 0),
		bluesAnticoneSizes: make(map[externalapi.DomainHash]externalapi.KType),
	}

	blockParents, err := gm.dagTopologyManager.Parents(stagingArea, blockHash)
	if err != nil {
		return err
	}

	// Calculate rank using DAGKnight algorithm to determine dynamic K for the block
	// DAGKnight TODO: modify blockversions before mainnet release.
	var k externalapi.KType
	if constants.GetBlockVersion() >= 6 {
		if len(blockParents) == 0 {
			// Genesis block uses default K
			k = gm.k[constants.GetBlockVersion()-1]
		} else {
			blockGhostDagData, err := gm.ghostdagDataStore.Get(gm.databaseContext, stagingArea, blockHash, false)
			if err != nil {
				rank, err := gm.CalculateRank(stagingArea, blockParents, blockParents)
				if err != nil {
					return err
				}
				k = externalapi.KType(rank)
				newBlockData.dynamicK = k
			} else { // this skips about 50% of k being recalculated.
				k = blockGhostDagData.DynamicK()
			}
		}
	} else {
		k = gm.k[constants.GetBlockVersion()-1]
	}

	isGenesis := len(blockParents) == 0
	if !isGenesis {
		selectedParent, err := gm.findSelectedParent(stagingArea, blockParents)
		if err != nil {
			return err
		}
		if selectedParent == nil {
			return errors.Errorf("findSelectedParent returned nil")
		}

		newBlockData.selectedParent = selectedParent
		newBlockData.mergeSetBlues = append(newBlockData.mergeSetBlues, selectedParent)
		newBlockData.bluesAnticoneSizes[*selectedParent] = 0
	}

	mergeSetWithoutSelectedParent, err := gm.mergeSetWithoutSelectedParent(
		stagingArea, newBlockData.selectedParent, blockParents, k)
	if err != nil {
		return err
	}

	for _, blueCandidate := range mergeSetWithoutSelectedParent {
		isBlue, candidateAnticoneSize, candidateBluesAnticoneSizes, err := gm.checkBlueCandidate(
			stagingArea, newBlockData.toModel(), blueCandidate, k)
		if err != nil {
			return err
		}

		if isBlue {
			// No k-cluster violation found, we can now set the candidate block as blue
			newBlockData.mergeSetBlues = append(newBlockData.mergeSetBlues, blueCandidate)
			newBlockData.bluesAnticoneSizes[*blueCandidate] = candidateAnticoneSize
			for blue, blueAnticoneSize := range candidateBluesAnticoneSizes {
				newBlockData.bluesAnticoneSizes[blue] = blueAnticoneSize + 1
			}
		} else {
			newBlockData.mergeSetReds = append(newBlockData.mergeSetReds, blueCandidate)
		}
	}
	// log.Debugf("Mergeset blues %d, reds %d", len(newBlockData.mergeSetBlues), len(newBlockData.mergeSetReds))

	if !isGenesis {
		selectedParentGHOSTDAGData, err := gm.ghostdagDataStore.Get(gm.databaseContext, stagingArea, newBlockData.selectedParent, false)
		if database.IsNotFoundError(err) {
			log.Debugf("GHOSTDAG failed to retrieve with %s\n", newBlockData.selectedParent)
			return err
		}
		if err != nil {
			return err
		}
		newBlockData.blueScore = selectedParentGHOSTDAGData.BlueScore() + uint64(len(newBlockData.mergeSetBlues))
		// We inherit the bluework from the selected parent
		newBlockData.blueWork.Set(selectedParentGHOSTDAGData.BlueWork())
		// Then we add up all the *work*(not blueWork) that all of newBlock merge set blues and selected parent did
		for _, blue := range newBlockData.mergeSetBlues {
			// We don't count the work of the virtual genesis
			if blue.Equal(model.VirtualGenesisBlockHash) {
				continue
			}

			header, err := gm.headerStore.BlockHeader(gm.databaseContext, stagingArea, blue)
			if err != nil {
				return err
			}
			newBlockData.blueWork.Add(newBlockData.blueWork, difficulty.CalcWork(header.Bits()))
		}
	} else {
		// Genesis's blue score is defined to be 0.
		newBlockData.blueScore = 0
		newBlockData.blueWork.SetUint64(0)
	}

	gm.ghostdagDataStore.Stage(stagingArea, blockHash, newBlockData.toModel(), false)

	return nil
}

type chainBlockData struct {
	hash      *externalapi.DomainHash
	blockData *externalapi.BlockGHOSTDAGData
}

func (gm *ghostdagManager) checkBlueCandidate(stagingArea *model.StagingArea, newBlockData *externalapi.BlockGHOSTDAGData,
	blueCandidate *externalapi.DomainHash, k externalapi.KType) (isBlue bool, candidateAnticoneSize externalapi.KType,
	candidateBluesAnticoneSizes map[externalapi.DomainHash]externalapi.KType, err error,
) {
	// The maximum length of node.blues can be K+1 because
	// it contains the selected parent.
	if externalapi.KType(len(newBlockData.MergeSetBlues())) == k+1 {
		return false, 0, nil, nil
	}

	candidateBluesAnticoneSizes = make(map[externalapi.DomainHash]externalapi.KType, k)

	// Iterate over all blocks in the blue set of newNode that are not in the past
	// of blueCandidate, and check for each one of them if blueCandidate potentially
	// enlarges their blue anticone to be over K, or that they enlarge the blue anticone
	// of blueCandidate to be over K.

	blueCandidateCheckStart := time.Now()
	selectedParentGHOSTDAGData, err := gm.ghostdagDataStore.Get(gm.databaseContext, stagingArea, newBlockData.SelectedParent(), false)
	if err != nil {
		return false, 0, nil, err
	}
	chainBlock := chainBlockData{
		hash:      newBlockData.SelectedParent(),
		blockData: selectedParentGHOSTDAGData,
	}
	// if we give chainBlock with hash, next loop does only one iteration.
	// Because gm.dagTopologyManager.IsAncestorOf(stagingArea, chainBlock.hash, blueCandidate)
	// returns either
	for {
		isBlue, isRed, err := gm.checkBlueCandidateWithChainBlock(stagingArea, newBlockData, chainBlock, blueCandidate,
			candidateBluesAnticoneSizes, &candidateAnticoneSize, k)
		if err != nil {
			return false, 0, nil, err
		}

		if isBlue {
			break
		}

		if isRed {
			return false, 0, nil, nil
		}

		selectedParentGHOSTDAGData, err := gm.ghostdagDataStore.Get(gm.databaseContext, stagingArea, chainBlock.blockData.SelectedParent(), false)
		if err != nil {
			return false, 0, nil, err
		}

		chainBlock = chainBlockData{hash: chainBlock.blockData.SelectedParent(),
			blockData: selectedParentGHOSTDAGData,
		}
	}
	log.Debugf("CheckBlueCandidate took %v", time.Since(blueCandidateCheckStart))

	return true, candidateAnticoneSize, candidateBluesAnticoneSizes, nil
}

func (gm *ghostdagManager) checkBlueCandidateWithChainBlock(stagingArea *model.StagingArea,
	newBlockData *externalapi.BlockGHOSTDAGData, chainBlock chainBlockData, blueCandidate *externalapi.DomainHash,
	candidateBluesAnticoneSizes map[externalapi.DomainHash]externalapi.KType,
	candidateAnticoneSize *externalapi.KType, k externalapi.KType,
) (isBlue, isRed bool, err error) {
	// If blueCandidate is in the future of chainBlock, it means
	// that all remaining blues are in the past of chainBlock and thus
	// in the past of blueCandidate. In this case we know for sure that
	// the anticone of blueCandidate will not exceed K, and we can mark
	// it as blue.
	//
	// The new block is always in the future of blueCandidate, so there's
	// no point in checking it.

	// We check if chainBlock is not the new block by checking if it has a hash.
	if chainBlock.hash != nil {
		isAncestorOfTimer := time.Now()
		isAncestorOfBlueCandidate, err := gm.dagTopologyManager.IsAncestorOf(stagingArea, chainBlock.hash, blueCandidate)
		log.Debugf("IsAncestorOf took %v", time.Since(isAncestorOfTimer))
		if err != nil {
			return false, false, err
		}
		if isAncestorOfBlueCandidate {
			return true, false, nil
		}
	}
	log.Debugf("Len %d of MergeSetBlues", len(chainBlock.blockData.MergeSetBlues()))
	for _, block := range chainBlock.blockData.MergeSetBlues() {
		// Skip blocks that exist in the past of blueCandidate.
		isAncestorOfTimer := time.Now()
		isAncestorOfBlueCandidate, err := gm.dagTopologyManager.IsAncestorOf(stagingArea, block, blueCandidate)
		log.Debugf("IsAncestorOf in MergeSetBlues loop took %v, with result %t", time.Since(isAncestorOfTimer), isAncestorOfBlueCandidate)
		if err != nil {
			return false, false, err
		}

		if isAncestorOfBlueCandidate {
			continue
		}

		blueAnticoneSize := time.Now()
		candidateBluesAnticoneSizes[*block], err = gm.blueAnticoneSize(stagingArea, block, chainBlock.blockData, k)
		log.Debugf("blueAnticoneSize in MergeSetBlues loop took %v, with result %d", time.Since(blueAnticoneSize), candidateBluesAnticoneSizes[*block])
		if err != nil {
			return false, false, err
		}
		*candidateAnticoneSize++

		// TODO: Increase allowed anticone size to be bigger than k, by adding offset. This will allow more blue blocks.
		var maxAnticoneSize = k
		if constants.GetBlockVersion() >= 7 {
			maxAnticoneSize += 1
		}

		if *candidateAnticoneSize > maxAnticoneSize {
			log.Debugf("Max Anticone size %d", maxAnticoneSize)
			log.Debugf("Candidate Anticone size %d", *candidateAnticoneSize)
			// k-cluster violation: The candidate's blue anticone exceeded maxAnticoneSize
			return false, true, nil
		}

		if candidateBluesAnticoneSizes[*block] == maxAnticoneSize {
			log.Debugf("Max Anticone size %d", maxAnticoneSize)
			log.Debugf("Candidate blues anticone size %d", candidateBluesAnticoneSizes[*block])
			// k-cluster violation: A block in candidate's blue anticone already
			// has maxAnticoneSize blue blocks in its own anticone
			return false, true, nil
		}

		// This is a sanity check that validates that a blue
		// block's blue anticone is not already larger than maxAnticoneSize.
		if candidateBluesAnticoneSizes[*block] > maxAnticoneSize {
			log.Debugf("Max Anticone size %d", maxAnticoneSize)
			log.Debugf("Candidate blues anticone size %d", candidateBluesAnticoneSizes[*block])
			return false, true, nil
		}
	}

	return false, false, nil
}

// blueAnticoneSize returns the blue anticone size of 'block' from the worldview of 'context'.
// Expects 'block' to be in the blue set of 'context'
func (gm *ghostdagManager) blueAnticoneSize(stagingArea *model.StagingArea,
	block *externalapi.DomainHash, context *externalapi.BlockGHOSTDAGData, k externalapi.KType) (externalapi.KType, error) {

	rotationStart := time.Now()
	maxWalk := int(4 * byte(k))
	steps := 0

	isTrustedData := false
	for current := context; current != nil; {
		blueAnticoneSize, ok := current.BluesAnticoneSizes()[*block]
		if ok {
			return blueAnticoneSize, nil
		}
		if steps > maxWalk {
			return 0, nil
		}
		selectedParent := current.SelectedParent()
		if selectedParent == nil || selectedParent.Equal(gm.genesisHash) || selectedParent.Equal(model.VirtualGenesisBlockHash) {
			return 0, nil
		}

		var err error
		current, err = gm.ghostdagDataStore.Get(gm.databaseContext, stagingArea, selectedParent, isTrustedData)
		if err != nil {
			return 0, err
		}
		steps++
	}
	log.Debugf("blueAnticoneSize  took %v", time.Since(rotationStart))
	return 0, errors.Errorf("block %s is not in blue set of the given context", block)
}
