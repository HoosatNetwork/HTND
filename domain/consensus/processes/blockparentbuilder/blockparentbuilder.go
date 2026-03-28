package blockparentbuilder

import (
	"sync"

	"github.com/Hoosat-Oy/HTND/domain/consensus/model"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/pkg/errors"
)

var hashSetPool = sync.Pool{
	New: func() interface{} {
		hashSet := make(map[externalapi.DomainHash]struct{}, 16)
		return &hashSet
	},
}

var domainHashSlicePool = sync.Pool{
	New: func() interface{} {
		slice := make([]*externalapi.DomainHash, 0, 16)
		return &slice
	},
}

var blockHeaderSlicePool = sync.Pool{
	New: func() interface{} {
		slice := make([]externalapi.BlockHeader, 0, 16)
		return &slice
	},
}

type candidateReferences struct {
	single *externalapi.DomainHash
	multi  []*externalapi.DomainHash
}

type candidateEntry struct {
	hash       *externalapi.DomainHash
	references candidateReferences
}

type candidateMap map[externalapi.DomainHash]candidateEntry

var candidateMapPool = sync.Pool{
	New: func() interface{} {
		m := make(candidateMap, 16)
		return &m
	},
}

type virtualGenesisChild struct {
	hash   *externalapi.DomainHash
	header externalapi.BlockHeader
}

var virtualGenesisChildSlicePool = sync.Pool{
	New: func() interface{} {
		slice := make([]virtualGenesisChild, 0, 16)
		return &slice
	},
}

type blockParentBuilder struct {
	databaseContext       model.DBManager
	blockHeaderStore      model.BlockHeaderStore
	dagTopologyManager    model.DAGTopologyManager
	parentsManager        model.ParentsManager
	reachabilityDataStore model.ReachabilityDataStore
	pruningStore          model.PruningStore

	genesisHash   *externalapi.DomainHash
	maxBlockLevel int
}

// New creates a new instance of a BlockParentBuilder
func New(
	databaseContext model.DBManager,
	blockHeaderStore model.BlockHeaderStore,
	dagTopologyManager model.DAGTopologyManager,
	parentsManager model.ParentsManager,

	reachabilityDataStore model.ReachabilityDataStore,
	pruningStore model.PruningStore,

	genesisHash *externalapi.DomainHash,
	maxBlockLevel int,
) model.BlockParentBuilder {
	return &blockParentBuilder{
		databaseContext:    databaseContext,
		blockHeaderStore:   blockHeaderStore,
		dagTopologyManager: dagTopologyManager,
		parentsManager:     parentsManager,

		reachabilityDataStore: reachabilityDataStore,
		pruningStore:          pruningStore,
		genesisHash:           genesisHash,
		maxBlockLevel:         maxBlockLevel,
	}
}

func (bpb *blockParentBuilder) BuildParents(stagingArea *model.StagingArea,
	daaScore uint64, directParentHashes []*externalapi.DomainHash) ([]externalapi.BlockLevelParents, error) {
	_ = daaScore

	// Late on we'll mutate direct parent hashes, so we first clone it.
	directParentHashesCopyPtr := domainHashSlicePool.Get().(*[]*externalapi.DomainHash)
	directParentHashesCopy := *directParentHashesCopyPtr
	if cap(directParentHashesCopy) < len(directParentHashes) {
		directParentHashesCopy = make([]*externalapi.DomainHash, len(directParentHashes))
	} else {
		directParentHashesCopy = directParentHashesCopy[:len(directParentHashes)]
	}
	copy(directParentHashesCopy, directParentHashes)
	defer func() {
		clear(directParentHashesCopy[:cap(directParentHashesCopy)])
		*directParentHashesCopyPtr = directParentHashesCopy[:0]
		domainHashSlicePool.Put(directParentHashesCopyPtr)
	}()

	pruningPoint, err := bpb.pruningStore.PruningPoint(bpb.databaseContext, stagingArea)
	if err != nil {
		return nil, err
	}

	// The first candidates to be added should be from a parent in the future of the pruning
	// point, so later on we'll know that every block that doesn't have reachability data
	// (i.e. pruned) is necessarily in the past of the current candidates and cannot be
	// considered as a valid candidate.
	// This is why we sort the direct parent headers in a way that the first one will be
	// in the future of the pruning point.
	directParentHeadersPtr := blockHeaderSlicePool.Get().(*[]externalapi.BlockHeader)
	directParentHeaders := *directParentHeadersPtr
	if cap(directParentHeaders) < len(directParentHashesCopy) {
		directParentHeaders = make([]externalapi.BlockHeader, len(directParentHashesCopy))
	} else {
		directParentHeaders = directParentHeaders[:len(directParentHashesCopy)]
	}
	defer func() {
		clear(directParentHeaders[:cap(directParentHeaders)])
		*directParentHeadersPtr = directParentHeaders[:0]
		blockHeaderSlicePool.Put(directParentHeadersPtr)
	}()
	firstParentInFutureOfPruningPointIndex := 0
	foundFirstParentInFutureOfPruningPoint := false
	for i, directParentHash := range directParentHashesCopy {
		isInFutureOfPruningPoint, err := bpb.dagTopologyManager.IsAncestorOf(stagingArea, pruningPoint, directParentHash)
		if err != nil {
			return nil, err
		}

		if !isInFutureOfPruningPoint {
			continue
		}

		firstParentInFutureOfPruningPointIndex = i
		foundFirstParentInFutureOfPruningPoint = true
		break
	}

	if !foundFirstParentInFutureOfPruningPoint {
		return nil, errors.New("BuildParents should get at least one parent in the future of the pruning point")
	}

	oldFirstDirectParent := directParentHashesCopy[0]
	directParentHashesCopy[0] = directParentHashesCopy[firstParentInFutureOfPruningPointIndex]
	directParentHashesCopy[firstParentInFutureOfPruningPointIndex] = oldFirstDirectParent

	for i, directParentHash := range directParentHashesCopy {
		directParentHeader, err := bpb.blockHeaderStore.BlockHeader(bpb.databaseContext, stagingArea, directParentHash)
		if err != nil {
			return nil, err
		}
		directParentHeaders[i] = directParentHeader
	}

	candidatesByLevel := make([]*candidateMap, bpb.maxBlockLevel+1)
	usedCandidateMaps := make([]*candidateMap, 0, bpb.maxBlockLevel+1)
	pooledReferenceSlices := make([]*[]*externalapi.DomainHash, 0, len(directParentHeaders))
	defer func() {
		for _, referenceSlicePtr := range pooledReferenceSlices {
			referenceSlice := *referenceSlicePtr
			clear(referenceSlice[:cap(referenceSlice)])
			*referenceSlicePtr = referenceSlice[:0]
			domainHashSlicePool.Put(referenceSlicePtr)
		}

		for _, candidatesPtr := range usedCandidateMaps {
			candidates := *candidatesPtr
			clear(candidates)
			candidateMapPool.Put(candidatesPtr)
		}
	}()

	// Direct parents are guaranteed to be in one other's anticones so add them all to
	// all the block levels they occupy
	for i, directParentHeader := range directParentHeaders {
		directParentHash := directParentHashesCopy[i]
		blockLevel := directParentHeader.BlockLevel(bpb.maxBlockLevel)
		for level := 0; level <= blockLevel; level++ {
			if candidatesByLevel[level] == nil {
				candidatesPtr := candidateMapPool.Get().(*candidateMap)
				clear(*candidatesPtr)
				candidatesByLevel[level] = candidatesPtr
				usedCandidateMaps = append(usedCandidateMaps, candidatesPtr)
			}
			(*candidatesByLevel[level])[*directParentHash] = candidateEntry{
				hash:       directParentHash,
				references: candidateReferences{single: directParentHash},
			}
		}
	}

	virtualGenesisChildren, err := bpb.dagTopologyManager.Children(stagingArea, model.VirtualGenesisBlockHash)
	if err != nil {
		return nil, err
	}

	virtualGenesisChildrenWithHeadersPtr := virtualGenesisChildSlicePool.Get().(*[]virtualGenesisChild)
	virtualGenesisChildrenWithHeaders := *virtualGenesisChildrenWithHeadersPtr
	if cap(virtualGenesisChildrenWithHeaders) < len(virtualGenesisChildren) {
		virtualGenesisChildrenWithHeaders = make([]virtualGenesisChild, len(virtualGenesisChildren))
	} else {
		virtualGenesisChildrenWithHeaders = virtualGenesisChildrenWithHeaders[:len(virtualGenesisChildren)]
	}
	defer func() {
		clear(virtualGenesisChildrenWithHeaders[:cap(virtualGenesisChildrenWithHeaders)])
		*virtualGenesisChildrenWithHeadersPtr = virtualGenesisChildrenWithHeaders[:0]
		virtualGenesisChildSlicePool.Put(virtualGenesisChildrenWithHeadersPtr)
	}()
	for i, child := range virtualGenesisChildren {
		childHeader, err := bpb.blockHeaderStore.BlockHeader(bpb.databaseContext, stagingArea, child)
		if err != nil {
			return nil, err
		}
		virtualGenesisChildrenWithHeaders[i] = virtualGenesisChild{hash: child, header: childHeader}
	}

	for _, directParentHeader := range directParentHeaders {
		for blockLevel, blockLevelParentsInHeader := range bpb.parentsManager.Parents(directParentHeader) {
			candidatesPtr := candidatesByLevel[blockLevel]
			isEmptyLevel := candidatesPtr == nil
			if candidatesPtr == nil {
				candidatesPtr = candidateMapPool.Get().(*candidateMap)
				clear(*candidatesPtr)
				candidatesByLevel[blockLevel] = candidatesPtr
				usedCandidateMaps = append(usedCandidateMaps, candidatesPtr)
			}
			candidates := *candidatesPtr

			for _, parent := range blockLevelParentsInHeader {
				isInFutureOfVirtualGenesisChildren := false
				hasReachabilityData, err := bpb.reachabilityDataStore.HasReachabilityData(bpb.databaseContext, stagingArea, parent)
				if err != nil {
					return nil, err
				}
				if hasReachabilityData {
					// If a block is in the future of one of the virtual genesis children it means we have the full DAG between the current block
					// and this parent, so there's no need for any indirect reference blocks, and normal reachability queries can be used.
					isInFutureOfVirtualGenesisChildren, err = bpb.dagTopologyManager.IsAnyAncestorOf(stagingArea, virtualGenesisChildren, parent)
					if err != nil {
						return nil, err
					}
				}

				if isEmptyLevel {
					referenceBlocks := candidateReferences{single: parent}
					if !isInFutureOfVirtualGenesisChildren {
						referenceSlicePtr := domainHashSlicePool.Get().(*[]*externalapi.DomainHash)
						referenceSlice := *referenceSlicePtr
						if cap(referenceSlice) < len(virtualGenesisChildrenWithHeaders) {
							referenceSlice = make([]*externalapi.DomainHash, 0, len(virtualGenesisChildrenWithHeaders))
						} else {
							referenceSlice = referenceSlice[:0]
						}
						for _, child := range virtualGenesisChildrenWithHeaders {
							if bpb.parentsManager.ParentsAtLevel(child.header, blockLevel).Contains(parent) {
								referenceSlice = append(referenceSlice, child.hash)
							}
						}
						referenceBlocks = candidateReferences{multi: referenceSlice}
						*referenceSlicePtr = referenceSlice
						pooledReferenceSlices = append(pooledReferenceSlices, referenceSlicePtr)
					}
					candidates[*parent] = candidateEntry{hash: parent, references: referenceBlocks}
					continue
				}

				if !isInFutureOfVirtualGenesisChildren {
					continue
				}

				toRemovePtr := hashSetPool.Get().(*map[externalapi.DomainHash]struct{})
				toRemove := *toRemovePtr
				isAncestorOfAnyCandidate := false
				for candidate, candidateEntry := range candidates {
					isInFutureOfCurrentCandidate, err := bpb.isInFutureOfReferences(stagingArea, candidateEntry.references, parent)
					if err != nil {
						return nil, err
					}

					if isInFutureOfCurrentCandidate {
						toRemove[candidate] = struct{}{}
						continue
					}

					if isAncestorOfAnyCandidate {
						continue
					}

					isAncestorOfCurrentCandidate, err := bpb.isAncestorOfReferences(stagingArea, parent, candidateEntry.references)
					if err != nil {
						return nil, err
					}

					if isAncestorOfCurrentCandidate {
						isAncestorOfAnyCandidate = true
					}
				}

				if len(toRemove) > 0 {
					for hash := range toRemove {
						delete(candidates, hash)
					}
				}

				// We should add the block as a candidate if it's in the future of another candidate
				// or in the anticone of all candidates.
				if !isAncestorOfAnyCandidate || len(toRemove) > 0 {
					candidates[*parent] = candidateEntry{
						hash:       parent,
						references: candidateReferences{single: parent},
					}
				}

				clear(toRemove)
				hashSetPool.Put(toRemovePtr)
			}
		}
	}

	parents := make([]externalapi.BlockLevelParents, 0, len(candidatesByLevel))
	for blockLevel := 0; blockLevel < len(candidatesByLevel); blockLevel++ {
		candidatesPtr := candidatesByLevel[blockLevel]
		if candidatesPtr == nil {
			break
		}
		candidates := *candidatesPtr
		if blockLevel > 0 {
			if _, ok := candidates[*bpb.genesisHash]; ok && len(candidates) == 1 {
				break
			}
		}

		levelBlocks := make(externalapi.BlockLevelParents, 0, len(candidates))
		for _, candidate := range candidates {
			levelBlocks = append(levelBlocks, candidate.hash)
		}

		parents = append(parents, levelBlocks)
	}
	return parents, nil
}

func (bpb *blockParentBuilder) isInFutureOfReferences(stagingArea *model.StagingArea,
	references candidateReferences, blockHash *externalapi.DomainHash) (bool, error) {

	if references.single != nil {
		return bpb.dagTopologyManager.IsAncestorOf(stagingArea, references.single, blockHash)
	}
	return bpb.dagTopologyManager.IsAnyAncestorOf(stagingArea, references.multi, blockHash)
}

func (bpb *blockParentBuilder) isAncestorOfReferences(stagingArea *model.StagingArea,
	blockHash *externalapi.DomainHash, references candidateReferences) (bool, error) {

	if references.single != nil {
		return bpb.dagTopologyManager.IsAncestorOf(stagingArea, blockHash, references.single)
	}
	return bpb.dagTopologyManager.IsAncestorOfAny(stagingArea, blockHash, references.multi)
}
