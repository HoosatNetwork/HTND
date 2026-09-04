package model

import (
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
)

// ConsensusStateManager manages the node's consensus state
type ConsensusStateManager interface {
	AddBlock(stagingArea *StagingArea, blockHash *externalapi.DomainHash, updateVirtual bool) (*externalapi.SelectedChainPath, externalapi.UTXODiff, *UTXODiffReversalData, error)
	PopulateTransactionWithUTXOEntries(stagingArea *StagingArea, transaction *externalapi.DomainTransaction) error
	ImportPruningPointUTXOSet(stagingArea *StagingArea, newPruningPoint *externalapi.DomainHash) error
	ImportPruningPoints(stagingArea *StagingArea, pruningPoints []externalapi.BlockHeader) error
	RestorePastUTXOSetIterator(stagingArea *StagingArea, blockHash *externalapi.DomainHash) (externalapi.ReadOnlyUTXOSetIterator, error)
	CalculatePastUTXOAndAcceptanceData(stagingArea *StagingArea, blockHash *externalapi.DomainHash) (externalapi.UTXODiff, externalapi.AcceptanceData, Multiset, error)
	GetVirtualSelectedParentChainFromBlock(stagingArea *StagingArea, blockHash *externalapi.DomainHash) (*externalapi.SelectedChainPath, error)
	RecoverUTXOIfRequired() error
	ReverseUTXODiffs(tipHash *externalapi.DomainHash, reversalData *UTXODiffReversalData) error
	ResolveVirtual(maxBlocksToResolve uint64) (*externalapi.VirtualChangeSet, bool, error)
	// RecomputeVirtual re-picks virtual's parents from the current tips and re-colors virtual from
	// scratch. Used to repair a virtual whose stored GHOSTDAG data is unusable - see
	// consensus.repairCollapsedVirtualIfRequired.
	RecomputeVirtual() error
	ValidateUTXODiffChildChains() error
	ResolveBlockStatus(stagingArea *StagingArea, blockHash *externalapi.DomainHash, useSeparateStagingAreaPerBlock bool) (externalapi.BlockStatus, *UTXODiffReversalData, error)
	// ResolveBlockStatusCacheLen returns the number of entries in the ResolveBlockStatus cache
	ResolveBlockStatusCacheLen() int
	// ReproduceDisqualification re-resolves blockHash (already StatusDisqualifiedFromChain from a
	// previous run) against its selectedParentHash (StatusUTXOValid) directly, bypassing the normal
	// cascade path so the original verifyUTXO failure - and its diagnostics - fire again.
	ReproduceDisqualification(blockHash, selectedParentHash *externalapi.DomainHash) error
}
