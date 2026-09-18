package syncmanager

import (
	"math/big"
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/pkg/errors"
)

// Fakes for the three stores missingBlockBodyHashes reads on the path to its virtual-genesis
// give-up branch: a pruning point that is not on highHash's selected parent chain, and whose
// selected-parent walk (via findLowHashInHighHashSelectedParentChain) reaches virtual genesis
// before finding a shared ancestor - the exact shape HTN-196 reproduced on a live node.

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

// TestMissingBlockBodyHashesFailsWhenChainsOnlyShareVirtualGenesis is HTN-196: when this node's
// pruning point cannot be reconciled with the syncer chain it's asked to fill in bodies for - not on
// its selected parent chain, and the only shared ancestor reachable is virtual genesis (which is "on"
// every chain by construction, so anchoring there would request every header-only block including
// ones no peer has a body for) - missingBlockBodyHashes must report that as a failure the caller can
// recognise, not an empty success.
//
// It used to return ([]DomainHash{}, nil), which read as "there is nothing to sync" and let IBD
// report success indefinitely: on the live node this looped 26+ times with zero forward progress,
// because block relay can never add a block below a gap it can't validate.
func TestMissingBlockBodyHashesFailsWhenChainsOnlyShareVirtualGenesis(t *testing.T) {
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

	_, err := sm.missingBlockBodyHashes(model.NewStagingArea(), highHash)
	if err == nil {
		t.Fatal("expected an error when the pruning point and highHash's chain only meet at virtual " +
			"genesis, got a nil error (the old infinite-loop behavior)")
	}
	if !errors.Is(err, externalapi.ErrPruningPointDataDoesNotReconcile) {
		t.Errorf("expected errors.Is(err, externalapi.ErrPruningPointDataDoesNotReconcile), got: %+v", err)
	}
}
