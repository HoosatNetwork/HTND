package syncmanager

import (
	"math/big"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/pkg/errors"
)

// Fakes for the three stores missingBlockBodyHashes reads on the path to its virtual-genesis
// give-up branch: a pruning point that is not on highHash's selected parent chain, and whose
// selected-parent walk (via findLowHashInHighHashSelectedParentChain) reaches virtual genesis
// before finding a shared ancestor.

type fixedPruningPointOnlyStore struct {
	model.PruningStore
	pruningPoint *externalapi.DomainHash
}

func (s fixedPruningPointOnlyStore) PruningPoint(model.DBReader, *model.StagingArea) (*externalapi.DomainHash, error) {
	return s.pruningPoint, nil
}

// onlyVirtualGenesisIsOnChainTopology reports every hash as NOT on highHash's selected parent chain
// except virtual genesis, which (as in the real reachability tree) is "on" every chain.
type onlyVirtualGenesisIsOnChainTopology struct {
	model.DAGTopologyManager
	highHash *externalapi.DomainHash
}

func (t onlyVirtualGenesisIsOnChainTopology) IsInSelectedParentChainOf(_ *model.StagingArea,
	blockHashA, blockHashB *externalapi.DomainHash,
) (bool, error) {
	if !blockHashB.Equal(t.highHash) {
		return false, errors.Errorf("unexpected highHash in test fixture: %s", blockHashB)
	}
	return blockHashA.Equal(model.VirtualGenesisBlockHash), nil
}

// chainToVirtualGenesisGHOSTDAGStore gives every block one selected parent, ending at virtual
// genesis - a straight-line walk up from the pruning point that never rejoins highHash's chain.
type chainToVirtualGenesisGHOSTDAGStore struct {
	model.GHOSTDAGDataStore
	selectedParent map[externalapi.DomainHash]*externalapi.DomainHash
}

func (s chainToVirtualGenesisGHOSTDAGStore) Get(_ model.DBReader, _ *model.StagingArea,
	blockHash *externalapi.DomainHash, _ bool,
) (*externalapi.BlockGHOSTDAGData, error) {
	parent, ok := s.selectedParent[*blockHash]
	if !ok {
		return nil, errors.Errorf("no GHOSTDAG data for %s", blockHash)
	}
	return externalapi.NewBlockGHOSTDAGData(1, big.NewInt(1), parent, nil, nil, nil, 1), nil
}

func missingBodyTestHash(b byte) *externalapi.DomainHash {
	return externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{b})
}

// TestMissingBlockBodyHashesSkipsWhenChainsOnlyShareVirtualGenesis: when this node's pruning point
// is not on highHash's selected parent chain and the only shared ancestor reachable is virtual
// genesis, missingBlockBodyHashes skips that segment. Anchoring on virtual genesis would request
// every header-only block, including pruning-proof headers no peer has a body for. Returning an
// error here aborts a clean sync whose pruning point and the peer tip only meet there.
func TestMissingBlockBodyHashesSkipsWhenChainsOnlyShareVirtualGenesis(t *testing.T) {
	pruningPoint := missingBodyTestHash(1)
	intermediate := missingBodyTestHash(2)
	highHash := missingBodyTestHash(3)

	sm := &syncManager{
		pruningStore:       fixedPruningPointOnlyStore{pruningPoint: pruningPoint},
		dagTopologyManager: onlyVirtualGenesisIsOnChainTopology{highHash: highHash},
		ghostdagDataStore: chainToVirtualGenesisGHOSTDAGStore{selectedParent: map[externalapi.DomainHash]*externalapi.DomainHash{
			*pruningPoint: intermediate,
			*intermediate: model.VirtualGenesisBlockHash,
		}},
	}

	hashes, err := sm.missingBlockBodyHashes(model.NewStagingArea(), highHash)
	if err != nil {
		t.Fatalf("expected body sync to be skipped, got: %+v", err)
	}
	if len(hashes) != 0 {
		t.Fatalf("expected no missing bodies, got %d", len(hashes))
	}
}
