package model

import "github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"

// WindowHeapSliceStore caches the slices that are needed for the heap implementation of DAGTraversalManager.BlockWindow
type WindowHeapSliceStore interface {
	Stage(stagingArea *StagingArea, blockHash *externalapi.DomainHash, windowSize int, includeTrustedWindow bool, pairs []*externalapi.BlockGHOSTDAGDataHashPair)
	IsStaged(stagingArea *StagingArea) bool
	Get(stagingArea *StagingArea, blockHash *externalapi.DomainHash, windowSize int, includeTrustedWindow bool) ([]*externalapi.BlockGHOSTDAGDataHashPair, error)

	CacheLen() int
	UnstageAll(stagingArea *StagingArea)
}
