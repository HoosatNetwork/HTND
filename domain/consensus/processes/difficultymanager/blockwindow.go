package difficultymanager

import (
	"math"
	"math/big"

	"github.com/Hoosat-Oy/HTND/domain/consensus/model"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/util/difficulty"
	"github.com/Hoosat-Oy/HTND/util/memory"
)

type difficultyBlock struct {
	timeInMilliseconds int64
	bits               uint32
}

type blockWindow struct {
	buffer            *memory.Block[difficultyBlock]
	blocks            []difficultyBlock
	pairs             []*externalapi.BlockGHOSTDAGDataHashPair
	minTimestamp      int64
	maxTimestamp      int64
	minTimestampIndex int
}

// blockWindow returns a blockWindow of the given size that contains the
// blocks in the past of startingBlock, the sorting is unspecified.
// If the number of blocks in the past of startingBlock is less then windowSize,
// the window will be padded by genesis blocks to achieve a size of windowSize.
func (dm *difficultyManager) blockWindow(stagingArea *model.StagingArea, startingBlock *externalapi.DomainHash, windowSize int) (blockWindow, error) {
	windowPairs, err := dm.dagTraversalManager.BlockWindowHeapSlice(stagingArea, startingBlock, windowSize)
	if err != nil {
		return blockWindow{}, err
	}

	buffer := memory.Malloc[difficultyBlock](len(windowPairs))
	window := blockWindow{
		buffer:            buffer,
		blocks:            buffer.Slice(),
		pairs:             windowPairs,
		minTimestamp:      math.MaxInt64,
		maxTimestamp:      0,
		minTimestampIndex: 0,
	}

	var minBlueWork *big.Int
	var minHash *externalapi.DomainHash
	for i, pair := range windowPairs {
		hash := pair.Hash
		header, err := dm.headerStore.BlockHeader(dm.databaseContext, stagingArea, hash)
		if err != nil {
			return blockWindow{}, err
		}

		window.blocks[i] = difficultyBlock{
			timeInMilliseconds: header.TimeInMilliseconds(),
			bits:               header.Bits(),
		}

		blueWork := pair.GHOSTDAGData.BlueWork()
		if header.TimeInMilliseconds() < window.minTimestamp ||
			(header.TimeInMilliseconds() == window.minTimestamp && ghostdagLess(blueWork, hash, minBlueWork, minHash)) {
			window.minTimestamp = header.TimeInMilliseconds()
			window.minTimestampIndex = i
			minBlueWork = blueWork
			minHash = hash
		}
		if header.TimeInMilliseconds() > window.maxTimestamp {
			window.maxTimestamp = header.TimeInMilliseconds()
		}
	}
	return window, nil
}

func ghostdagLess(blueWorkA *big.Int, hashA *externalapi.DomainHash, blueWorkB *big.Int, hashB *externalapi.DomainHash) bool {
	switch blueWorkA.Cmp(blueWorkB) {
	case -1:
		return true
	case 1:
		return false
	case 0:
		return hashA.Less(hashB)
	default:
		panic("big.Int.Cmp is defined to always return -1/1/0 and nothing else")
	}
}

func (window blockWindow) minMaxTimestamps() (min, max int64, minIndex int) {
	return window.minTimestamp, window.maxTimestamp, window.minTimestampIndex
}

func (window *blockWindow) remove(n int) {
	window.blocks[n] = window.blocks[len(window.blocks)-1]
	window.blocks = window.blocks[:len(window.blocks)-1]
}

func (window blockWindow) averageTarget() *big.Int {
	averageTarget := new(big.Int)
	targetTmp := new(big.Int)
	for _, block := range window.blocks {
		difficulty.CompactToBigWithDestination(block.bits, targetTmp)
		averageTarget.Add(averageTarget, targetTmp)
	}
	return averageTarget.Div(averageTarget, big.NewInt(int64(len(window.blocks))))
}

func (window blockWindow) len() int {
	return len(window.blocks)
}

func (window *blockWindow) free() {
	memory.Free(window.buffer)
	*window = blockWindow{}
}
