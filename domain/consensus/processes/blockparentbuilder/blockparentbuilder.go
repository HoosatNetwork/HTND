package blockparentbuilder

import (
	"sync"

	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/infrastructure/logger"
	"github.com/pkg/errors"
)

var log = logger.RegisterSubSystem("BLPB")

var hashSetPool = sync.Pool{
	New: func() any {
		hashSet := make(map[externalapi.DomainHash]struct{}, 16)
		return &hashSet
	},
}

var domainHashSlicePool = sync.Pool{
	New: func() any {
		slice := make([]*externalapi.DomainHash, 0, 16)
		return &slice
	},
}

var blockHeaderSlicePool = sync.Pool{
	New: func() any {
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
	New: func() any {
		m := make(candidateMap, 16)
		return &m
	},
}

type virtualGenesisChild struct {
	hash   *externalapi.DomainHash
	header externalapi.BlockHeader
}

var virtualGenesisChildSlicePool = sync.Pool{
	New: func() any {
		slice := make([]virtualGenesisChild, 0, 16)
		return &slice
	},
}

type blockParentBuilder struct {
	databaseContext       model.DBManager
	blockHeaderStore      model.BlockHeaderStore
	blockStatusStore      model.BlockStatusStore
	dagTopologyManager    model.DAGTopologyManager
	parentsManager        model.ParentsManager
	consensusStateManager model.ConsensusStateManager
	reachabilityDataStore model.ReachabilityDataStore
	pruningStore          model.PruningStore

	genesisHash   *externalapi.DomainHash
	maxBlockLevel int
}

// New creates a new instance of a BlockParentBuilder
func New(
	databaseContext model.DBManager,
	blockHeaderStore model.BlockHeaderStore,
	blockStatusStore model.BlockStatusStore,
	dagTopologyManager model.DAGTopologyManager,
	parentsManager model.ParentsManager,
	consensusStateManager model.ConsensusStateManager,

	reachabilityDataStore model.ReachabilityDataStore,
	pruningStore model.PruningStore,

	genesisHash *externalapi.DomainHash,
	maxBlockLevel int,
) model.BlockParentBuilder {
	return &blockParentBuilder{
		databaseContext:       databaseContext,
		blockHeaderStore:      blockHeaderStore,
		blockStatusStore:      blockStatusStore,
		dagTopologyManager:    dagTopologyManager,
		parentsManager:        parentsManager,
		consensusStateManager: consensusStateManager,

		reachabilityDataStore: reachabilityDataStore,
		pruningStore:          pruningStore,
		genesisHash:           genesisHash,
		maxBlockLevel:         maxBlockLevel,
	}
}

func (bpb *blockParentBuilder) BuildParents(stagingArea *model.StagingArea,
	daaScore uint64, directParentHashes []*externalapi.DomainHash, newBlockParents bool,
) ([]externalapi.BlockLevelParents, error) {
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
	// 
	// During IBD, reachability data might be incomplete, so IsAncestorOf might return
	// errors or false negatives. In this case, we use the first direct parent as a fallback.
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
	
	// Try to find a parent in the future of the pruning point
	for i, directParentHash := range directParentHashesCopy {
		isInFutureOfPruningPoint, err := bpb.dagTopologyManager.IsAncestorOf(stagingArea, pruningPoint, directParentHash)
		if err != nil {
			// During IBD, reachability data might be incomplete, causing IsAncestorOf to fail.
			// Log and continue to try other parents instead of failing immediately.
			log.Debugf("BuildParents: IsAncestorOf failed for parent %s of block %s: %v. Trying other parents...",
				directParentHash, directParentHashesCopy[0], err)
			continue
		}

		if !isInFutureOfPruningPoint {
			continue
		}

		firstParentInFutureOfPruningPointIndex = i
		foundFirstParentInFutureOfPruningPoint = true
		break
	}

	// If no parent found in the future of pruning point (can happen during IBD with incomplete reachability),
	// use the first direct parent as a fallback. This is safe during IBD since we're receiving
	// blocks from a trusted peer and the direct parents are already validated to exist.
	if !foundFirstParentInFutureOfPruningPoint && len(directParentHashesCopy) > 0 {
		log.Debugf("BuildParents: No parent found in future of pruning point for block %s. Using first direct parent as fallback (IBD scenario).", directParentHashesCopy[0])
		firstParentInFutureOfPruningPointIndex = 0
		foundFirstParentInFutureOfPruningPoint = true
	}

	if !foundFirstParentInFutureOfPruningPoint {
		return nil, errors.New("BuildParents should get at least one parent in the future of the pruning point")
	}

	directParentHashesCopy[0], directParentHashesCopy[firstParentInFutureOfPruningPointIndex] =
		directParentHashesCopy[firstParentInFutureOfPruningPointIndex], directParentHashesCopy[0]

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

	// Build filtered list: skip virtual sentinels (no headers / no reachability data).
	virtualGenesisChildrenWithHeadersPtr := virtualGenesisChildSlicePool.Get().(*[]virtualGenesisChild)
	virtualGenesisChildrenWithHeaders := (*virtualGenesisChildrenWithHeadersPtr)[:0]
	if cap(virtualGenesisChildrenWithHeaders) < len(virtualGenesisChildren) {
		virtualGenesisChildrenWithHeaders = make([]virtualGenesisChild, 0, len(virtualGenesisChildren))
	}
	defer func() {
		clear(virtualGenesisChildrenWithHeaders[:cap(virtualGenesisChildrenWithHeaders)])
		*virtualGenesisChildrenWithHeadersPtr = virtualGenesisChildrenWithHeaders[:0]
		virtualGenesisChildSlicePool.Put(virtualGenesisChildrenWithHeadersPtr)
	}()

	virtualGenesisChildHashes := make([]*externalapi.DomainHash, 0, len(virtualGenesisChildren))
	for _, child := range virtualGenesisChildren {
		if child.Equal(model.VirtualBlockHash) || child.Equal(model.VirtualGenesisBlockHash) {
			continue
		}
		childHeader, err := bpb.blockHeaderStore.BlockHeader(bpb.databaseContext, stagingArea, child)
		if err != nil {
			return nil, err
		}
		virtualGenesisChildrenWithHeaders = append(virtualGenesisChildrenWithHeaders, virtualGenesisChild{
			hash:   child,
			header: childHeader,
		})
		virtualGenesisChildHashes = append(virtualGenesisChildHashes, child)
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
				// Virtual markers are never real parents.
				if parent.Equal(model.VirtualBlockHash) || parent.Equal(model.VirtualGenesisBlockHash) {
					continue
				}

				isInFutureOfVirtualGenesisChildren := false
				hasReachabilityData, err := bpb.reachabilityDataStore.HasReachabilityData(bpb.databaseContext, stagingArea, parent)
				if err != nil {
					return nil, err
				}
				if hasReachabilityData {
					// Use filtered child hashes only — never pass VirtualBlockHash / VirtualGenesis into reachability.
					isInFutureOfVirtualGenesisChildren, err = bpb.dagTopologyManager.IsAnyAncestorOf(
						stagingArea, virtualGenesisChildHashes, parent)
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
	for blockLevel := range candidatesByLevel {
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
		if len(levelBlocks) > 0 {
			parents = append(parents, levelBlocks)
		}
	}
	return parents, nil
}

func (bpb *blockParentBuilder) isInFutureOfReferences(stagingArea *model.StagingArea,
	references candidateReferences, blockHash *externalapi.DomainHash,
) (bool, error) {
	if references.single != nil {
		return bpb.dagTopologyManager.IsAncestorOf(stagingArea, references.single, blockHash)
	}
	return bpb.dagTopologyManager.IsAnyAncestorOf(stagingArea, references.multi, blockHash)
}

func (bpb *blockParentBuilder) isAncestorOfReferences(stagingArea *model.StagingArea,
	blockHash *externalapi.DomainHash, references candidateReferences,
) (bool, error) {
	if references.single != nil {
		return bpb.dagTopologyManager.IsAncestorOf(stagingArea, blockHash, references.single)
	}
	return bpb.dagTopologyManager.IsAncestorOfAny(stagingArea, blockHash, references.multi)
}
