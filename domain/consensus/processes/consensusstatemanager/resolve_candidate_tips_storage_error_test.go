package consensusstatemanager

import (
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/database"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/pkg/errors"
)

var errStorageTestInjected = errors.New("injected storage error")

type storageErrorTestTipsStore struct {
	model.ConsensusStateStore
	tips []*externalapi.DomainHash
}

func (s storageErrorTestTipsStore) Tips(*model.StagingArea, model.DBReader) ([]*externalapi.DomainHash, error) {
	return s.tips, nil
}

// storageErrorTestStatusStore returns the status in statuses, the injected error for a block in failing, and
// not-found for any other block.
type storageErrorTestStatusStore struct {
	model.BlockStatusStore
	statuses map[externalapi.DomainHash]externalapi.BlockStatus
	failing  map[externalapi.DomainHash]bool
}

func (s storageErrorTestStatusStore) Get(_ model.DBReader, _ *model.StagingArea, blockHash *externalapi.DomainHash,
) (externalapi.BlockStatus, error) {
	if s.failing[*blockHash] {
		return 0, errStorageTestInjected
	}
	status, ok := s.statuses[*blockHash]
	if !ok {
		return 0, database.ErrNotFound
	}
	return status, nil
}

type storageErrorTestTopology struct {
	model.DAGTopologyManager
	parents  map[externalapi.DomainHash][]*externalapi.DomainHash
	children map[externalapi.DomainHash][]*externalapi.DomainHash
}

func (t storageErrorTestTopology) Parents(_ *model.StagingArea, blockHash *externalapi.DomainHash,
) ([]*externalapi.DomainHash, error) {
	return t.parents[*blockHash], nil
}

func (t storageErrorTestTopology) Children(_ *model.StagingArea, blockHash *externalapi.DomainHash,
) ([]*externalapi.DomainHash, error) {
	return t.children[*blockHash], nil
}

// TestResolveCandidateTipsReturnsStorageErrors checks that replacing a disqualified tip by its valid ancestors returns
// a block status store error instead of treating the block as absent; only not-found is skipped. The DAG is
// P <- D (disqualified tip) and P <- C, so every status the walk reads can be made to fail.
func TestResolveCandidateTipsReturnsStorageErrors(t *testing.T) {
	p := externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{1})
	d := externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{2})
	c := externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{3})
	topology := storageErrorTestTopology{
		parents:  map[externalapi.DomainHash][]*externalapi.DomainHash{*d: {p}, *c: {p}},
		children: map[externalapi.DomainHash][]*externalapi.DomainHash{*p: {d, c}},
	}

	for _, test := range []struct {
		name    string
		failing *externalapi.DomainHash // nil: no error
		cStatus externalapi.BlockStatus
		want    []*externalapi.DomainHash
	}{
		{name: "tip status", failing: d},
		{name: "parent status", failing: p},
		{name: "child status", failing: c},
		{name: "no error, parent covered only by the disqualified tip", cStatus: externalapi.StatusInvalid,
			want: []*externalapi.DomainHash{p}},
		{name: "no error, parent covered by a valid child", cStatus: externalapi.StatusUTXOValid},
	} {
		statuses := map[externalapi.DomainHash]externalapi.BlockStatus{
			*d: externalapi.StatusDisqualifiedFromChain,
			*p: externalapi.StatusUTXOValid,
			*c: test.cStatus,
		}
		failing := map[externalapi.DomainHash]bool{}
		if test.failing != nil {
			failing[*test.failing] = true
		}
		csm := &consensusStateManager{
			consensusStateStore: storageErrorTestTipsStore{tips: []*externalapi.DomainHash{d}},
			blockStatusStore:    storageErrorTestStatusStore{statuses: statuses, failing: failing},
			dagTopologyManager:  topology,
		}

		tips, err := csm.resolveCandidateTips(model.NewStagingArea())
		if test.failing != nil {
			if !errors.Is(err, errStorageTestInjected) {
				t.Fatalf("%s: got tips %v and error %v, want the injected storage error", test.name, tips, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: unexpected error: %+v", test.name, err)
		}
		if !externalapi.HashesEqual(tips, test.want) {
			t.Fatalf("%s: got tips %v, want %v", test.name, tips, test.want)
		}
	}
}

// TestCalculateNewTipsReturnsTipStatusStorageErrors checks that calculateNewTips returns a block status store error for
// an existing tip instead of dropping the tip; only a tip whose status is not found is skipped. The new block N is a
// child of P, and T is the other existing tip.
func TestCalculateNewTipsReturnsTipStatusStorageErrors(t *testing.T) {
	genesis := externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{9})
	p := externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{1})
	n := externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{2})
	other := externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{3})
	topology := storageErrorTestTopology{
		parents:  map[externalapi.DomainHash][]*externalapi.DomainHash{*n: {p}},
		children: map[externalapi.DomainHash][]*externalapi.DomainHash{*p: {n}},
	}

	for _, test := range []struct {
		name     string
		failing  bool
		notFound bool
		want     []*externalapi.DomainHash
	}{
		{name: "tip status error", failing: true},
		{name: "tip status not found", notFound: true, want: []*externalapi.DomainHash{n}},
		{name: "no error", want: []*externalapi.DomainHash{n, other}},
	} {
		statuses := map[externalapi.DomainHash]externalapi.BlockStatus{*p: externalapi.StatusUTXOValid}
		if !test.notFound {
			statuses[*other] = externalapi.StatusUTXOValid
		}
		csm := &consensusStateManager{
			genesisHash:         genesis,
			consensusStateStore: storageErrorTestTipsStore{tips: []*externalapi.DomainHash{p, other}},
			blockStatusStore: storageErrorTestStatusStore{statuses: statuses,
				failing: map[externalapi.DomainHash]bool{*other: test.failing}},
			dagTopologyManager: topology,
		}

		tips, err := csm.calculateNewTips(model.NewStagingArea(), n, externalapi.StatusUTXOPendingVerification)
		if test.failing {
			if !errors.Is(err, errStorageTestInjected) {
				t.Fatalf("%s: got tips %v and error %v, want the injected storage error", test.name, tips, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: unexpected error: %+v", test.name, err)
		}
		if !externalapi.HashesEqual(tips, test.want) {
			t.Fatalf("%s: got tips %v, want %v", test.name, tips, test.want)
		}
	}
}
