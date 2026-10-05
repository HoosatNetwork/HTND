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
	return &BlockGHOSTDAGData{
		blueScore:          blueScore,
		blueWork:           blueWork,
		selectedParent:     selectedParent,
		mergeSetBlues:      mergeSetBlues,
		mergeSetReds:       mergeSetReds,
		bluesAnticoneSizes: bluesAnticoneSizes,
		dynamicK:           dynamicK,
	}
}

// BlueScore returns the BlueScore of the block
func (bgd *BlockGHOSTDAGData) BlueScore() uint64 {
	return bgd.blueScore
}

// BlueWork returns a copy of the BlueWork of the block. See MergeSetBlues for why this
// can't safely return the underlying *big.Int.
func (bgd *BlockGHOSTDAGData) BlueWork() *big.Int {
	if bgd.blueWork == nil {
		return nil
	}
	return new(big.Int).Set(bgd.blueWork)
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

// MergeSetBlues returns a copy of the MergeSetBlues of the block. BlockGHOSTDAGData
// returned from a store is a cached/staged pointer shared with every other reader and
// with what eventually gets persisted, so callers must not be able to corrupt it by
// mutating the slice they get back (e.g. appending to it in place) - see the coinbase
// manager bug this was fixed for.
func (bgd *BlockGHOSTDAGData) MergeSetBlues() []*DomainHash {
	return cloneHashSlice(bgd.mergeSetBlues)
}

// MergeSetReds returns a copy of the MergeSetReds of the block. See MergeSetBlues for why
// this can't safely return the underlying slice.
func (bgd *BlockGHOSTDAGData) MergeSetReds() []*DomainHash {
	return cloneHashSlice(bgd.mergeSetReds)
}

// BluesAnticoneSizes returns a copy of the map between the blocks in its MergeSetBlues and
// the size of their anticone. See MergeSetBlues for why this can't safely return the
// underlying map.
func (bgd *BlockGHOSTDAGData) BluesAnticoneSizes() map[DomainHash]KType {
	if bgd.bluesAnticoneSizes == nil {
		return nil
	}
	cp := make(map[DomainHash]KType, len(bgd.bluesAnticoneSizes))
	for hash, kType := range bgd.bluesAnticoneSizes {
		cp[hash] = kType
	}
	return cp
}

func cloneHashSlice(hashes []*DomainHash) []*DomainHash {
	if hashes == nil {
		return nil
	}
	cp := make([]*DomainHash, len(hashes))
	copy(cp, hashes)
	return cp
}
