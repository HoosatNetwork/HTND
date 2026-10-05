package model

import "github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"

// BlockParentBuilder exposes a method to build super-block parents for
// a given set of direct parents
type BlockParentBuilder interface {
	BuildParents(stagingArea *StagingArea,
		daaScore uint64,
		directParentHashes []*externalapi.DomainHash,
		newBlockParents bool) ([]externalapi.BlockLevelParents, error)
}
