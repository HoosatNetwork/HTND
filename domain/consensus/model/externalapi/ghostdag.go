package externalapi

import (
	"math/big"
)

// KType defines the size of GHOSTDAG consensus algorithm K parameter.
type KType byte

// BlockGHOSTDAGData represents GHOSTDAG data for some block
type BlockGHOSTDAGData struct {
	blueScore          uint64
	blueWork           *big.Int
	dynamicK           KType
	selectedParent     *DomainHash
	mergeSetBlues      []*DomainHash
	mergeSetReds       []*DomainHash
	bluesAnticoneSizes map[DomainHash]KType
}

// NewBlockGHOSTDAGData creates a new instance of BlockGHOSTDAGData
func NewBlockGHOSTDAGData(
	blueScore uint64,
	blueWork *big.Int,
	selectedParent *DomainHash,
	mergeSetBlues []*DomainHash,
	mergeSetReds []*DomainHash,
	bluesAnticoneSizes map[DomainHash]KType,
	dynamicK KType,
) *BlockGHOSTDAGData {
	// Create deep copies to avoid sharing mutable state
	mergeSetBluesCopy := make([]*DomainHash, len(mergeSetBlues))
	copy(mergeSetBluesCopy, mergeSetBlues)

	mergeSetRedsCopy := make([]*DomainHash, len(mergeSetReds))
	copy(mergeSetRedsCopy, mergeSetReds)

	bluesAnticoneSizesCopy := make(map[DomainHash]KType, len(bluesAnticoneSizes))
	for k, v := range bluesAnticoneSizes {
		bluesAnticoneSizesCopy[k] = v
	}

	// Create a copy of blueWork if it's not nil
	var blueWorkCopy *big.Int
	if blueWork != nil {
		blueWorkCopy = new(big.Int).Set(blueWork)
	}

	return &BlockGHOSTDAGData{
		blueScore:          blueScore,
		blueWork:           blueWorkCopy,
		selectedParent:     selectedParent,
		mergeSetBlues:      mergeSetBluesCopy,
		mergeSetReds:       mergeSetRedsCopy,
		bluesAnticoneSizes: bluesAnticoneSizesCopy,
		dynamicK:           dynamicK,
	}
}

// BlueScore returns the BlueScore of the block
func (bgd *BlockGHOSTDAGData) BlueScore() uint64 {
	return bgd.blueScore
}

// BlueWork returns the BlueWork of the block
func (bgd *BlockGHOSTDAGData) BlueWork() *big.Int {
	return bgd.blueWork
}

// DynamicK returns the dynamic K that was used for this block's GHOSTDAG calculation.
func (bgd *BlockGHOSTDAGData) DynamicK() KType {
	return bgd.dynamicK
}

// SetDynamicK sets the dynamic K that was used for this block's GHOSTDAG calculation.
func (bgd *BlockGHOSTDAGData) SetDynamicK(dynamicK KType) {
	bgd.dynamicK = dynamicK
}

// SelectedParent returns the SelectedParent of the block
func (bgd *BlockGHOSTDAGData) SelectedParent() *DomainHash {
	return bgd.selectedParent
}

// MergeSetBlues returns the MergeSetBlues of the block (not a copy)
func (bgd *BlockGHOSTDAGData) MergeSetBlues() []*DomainHash {
	return bgd.mergeSetBlues
}

// MergeSetReds returns the MergeSetReds of the block (not a copy)
func (bgd *BlockGHOSTDAGData) MergeSetReds() []*DomainHash {
	return bgd.mergeSetReds
}

// BluesAnticoneSizes returns a map between the blocks in its MergeSetBlues and the size of their anticone
func (bgd *BlockGHOSTDAGData) BluesAnticoneSizes() map[DomainHash]KType {
	return bgd.bluesAnticoneSizes
}
