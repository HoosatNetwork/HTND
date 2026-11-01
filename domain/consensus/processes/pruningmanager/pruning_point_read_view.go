package pruningmanager

import (
	"bytes"
	"context"
	"math"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/database"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/datastructures/acceptancedatastore"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/datastructures/blockheaderstore"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/datastructures/blockrelationstore"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/datastructures/consensusstatestore"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/datastructures/ghostdagdatastore"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/datastructures/multisetstore"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/datastructures/pruningstore"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/datastructures/reachabilitydatastore"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/datastructures/utxodiffstore"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/processes/dagtopologymanager"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/processes/dagtraversalmanager"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/processes/reachabilitymanager"
	"github.com/pkg/errors"
)

// indexedPruningStore pins the selected index used by BOTH legacy derivations and
// method selection. The target is base+1 even when live selection has moved further.
// All other reads use this snapshot's private store; no live cache is reachable.
type indexedPruningStore struct {
	model.PruningStore
	index uint64
}

func (s *indexedPruningStore) CurrentPruningPointIndex(model.DBReader, *model.StagingArea) (uint64, error) {
	return s.index, nil
}

// pruningReadView binds a transition to one snapshot and instance prefix. The
// manager, all store caches and each derivation's staging area are private. It is
// single-owner; its caller owns the snapshot and must keep it open until finished.
type pruningReadView struct {
	manager      *pruningManager
	base, target externalapi.DomainHash
	baseIndex    uint64
}

func (v *pruningReadView) diff() (externalapi.UTXODiff, string, error) {
	return v.manager.pruningPointUTXOSetDiff(model.NewStagingArea(), &v.target)
}

// readView validates the consecutive base/target identities in the named instance
// at snapshot capture. Live selection may already be ahead of target. The caller
// must capture under consensus ownership and establish that the served bucket is
// the COMPLETE base set, never a partial update. Index membership alone cannot
// establish that fact; durable admission/recovery remains outside this capability.
//
// Genesis restoration uses managers intentionally absent here and is rejected.
// The existing synchronous path remains responsible for genesis. No worker is
// installed by this capability, and no snapshot survives process restart.
func (pm *pruningManager) readView(snapshot *database.ReadSnapshot, p model.DBBucket,
	baseIndex uint64, base, target *externalapi.DomainHash) (*pruningReadView, error) {
	return pm.readViewWithContext(context.Background(), snapshot, p, baseIndex, base, target)
}

// readViewWithContext bounds shutdown by checking cancellation during database reads.
func (pm *pruningManager) readViewWithContext(ctx context.Context, readSnapshot *database.ReadSnapshot, p model.DBBucket,
	baseIndex uint64, base, target *externalapi.DomainHash) (*pruningReadView, error) {
	if readSnapshot == nil || p == nil || base == nil || target == nil || pm.genesisHash == nil {
		return nil, errors.New("incomplete pruning read-view identity")
	}
	if baseIndex == math.MaxUint64 || target.Equal(pm.genesisHash) {
		return nil, errors.New("unsupported pruning read-view transition")
	}
	snapshot := cancelableSnapshot{readSnapshot, ctx}
	// Own the instance path instead of retaining the caller's mutable byte slice.
	p = database.MakeBucket(bytes.Clone(p.Path()))
	privatePruning := pruningstore.New(p, 2, false)
	stage := model.NewStagingArea()
	selected, err := privatePruning.CurrentPruningPointIndex(snapshot, stage)
	if err != nil {
		return nil, err
	}
	if selected < baseIndex+1 {
		return nil, errors.New("pruning target is not selected in snapshot")
	}
	for index, want := range map[uint64]*externalapi.DomainHash{baseIndex: base, baseIndex + 1: target} {
		got, err := privatePruning.PruningPointByIndex(snapshot, stage, index)
		if err != nil {
			return nil, err
		}
		if !got.Equal(want) {
			return nil, errors.Errorf("pruning read-view identity mismatch at index %d", index)
		}
	}

	level := p.Bucket([]byte{0})
	header, e := blockheaderstore.New(snapshot, p, 128, false)
	if e != nil {
		return nil, e
	}
	ghost := ghostdagdatastore.New(level, 128, false)
	reach := reachabilitydatastore.New(level, 128, false)
	old, e := reach.HasReachabilityData(snapshot, model.NewStagingArea(), model.VirtualGenesisBlockHash)
	if e != nil {
		return nil, e
	}
	if !old {
		reach = reachabilitydatastore.New(p, 128, false)
	}
	rm := reachabilitymanager.New(snapshot, ghost, reach)
	topology := dagtopologymanager.New(snapshot, rm, blockrelationstore.New(level, 128, false), ghost)
	genesis := *pm.genesisHash
	traversal := dagtraversalmanager.New(snapshot, topology, ghost, rm, nil, nil, nil, &genesis, nil, nil, append([]uint64(nil), pm.powScores...))
	manager := &pruningManager{databaseContext: snapshot, genesisHash: &genesis,
		pruningStore:     &indexedPruningStore{privatePruning, baseIndex + 1},
		blockHeaderStore: header, ghostdagDataStore: ghost,
		acceptanceDataStore: acceptancedatastore.New(p, 128, 1<<20, false),
		utxoDiffStore:       utxodiffstore.New(p, 128, false),
		consensusStateStore: consensusstatestore.New(p, 128, false),
		multiSetStore:       multisetstore.New(p, 128, false),
		dagTraversalManager: traversal, dagTopologyManager: topology}
	return &pruningReadView{manager: manager, base: *base, target: *target, baseIndex: baseIndex}, nil
}
