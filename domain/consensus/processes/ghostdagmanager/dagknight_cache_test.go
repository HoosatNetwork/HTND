package ghostdagmanager

import (
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/lrucache"
)

type dagKnightCacheTopologyStub struct {
	children      map[externalapi.DomainHash][]*externalapi.DomainHash
	childrenCalls int
}

func (d *dagKnightCacheTopologyStub) Parents(_ *model.StagingArea, _ *externalapi.DomainHash) ([]*externalapi.DomainHash, error) {
	panic("not implemented")
}

func (d *dagKnightCacheTopologyStub) Children(_ *model.StagingArea, blockHash *externalapi.DomainHash) ([]*externalapi.DomainHash, error) {
	d.childrenCalls++
	return d.children[*blockHash], nil
}

func (d *dagKnightCacheTopologyStub) IsParentOf(_ *model.StagingArea, _, _ *externalapi.DomainHash) (bool, error) {
	panic("not implemented")
}

func (d *dagKnightCacheTopologyStub) IsChildOf(_ *model.StagingArea, _, _ *externalapi.DomainHash) (bool, error) {
	panic("not implemented")
}

func (d *dagKnightCacheTopologyStub) IsAncestorOf(_ *model.StagingArea, _, _ *externalapi.DomainHash) (bool, error) {
	panic("not implemented")
}

func (d *dagKnightCacheTopologyStub) IsAncestorOfAny(_ *model.StagingArea, _ *externalapi.DomainHash, _ []*externalapi.DomainHash) (bool, error) {
	panic("not implemented")
}

func (d *dagKnightCacheTopologyStub) IsAnyAncestorOf(_ *model.StagingArea, _ []*externalapi.DomainHash, _ *externalapi.DomainHash) (bool, error) {
	panic("not implemented")
}

func (d *dagKnightCacheTopologyStub) IsInSelectedParentChainOf(_ *model.StagingArea, _, _ *externalapi.DomainHash) (bool, error) {
	panic("not implemented")
}

func (d *dagKnightCacheTopologyStub) ChildInSelectedParentChainOf(_ *model.StagingArea, _, _ *externalapi.DomainHash) (*externalapi.DomainHash, error) {
	panic("not implemented")
}

func (d *dagKnightCacheTopologyStub) SetParents(_ *model.StagingArea, _ *externalapi.DomainHash, _ []*externalapi.DomainHash) error {
	panic("not implemented")
}

func TestUMCVotingUsesMemoizedKeys(t *testing.T) {
	a := externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{0x41})
	b := externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{0x42})
	c := externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{0x43})

	topology := &dagKnightCacheTopologyStub{
		children: map[externalapi.DomainHash][]*externalapi.DomainHash{
			*a: {b},
			*b: {c},
		},
	}

	manager := &ghostdagManager{
		dagTopologyManager: topology,
		umcVotingCache:     lrucache.New[int](16, false),
	}

	graph := []*externalapi.DomainHash{a, b, c}
	blueSet := []*externalapi.DomainHash{a, b}
	first, err := manager.UMCVoting(nil, graph, blueSet, 0)
	if err != nil {
		t.Fatalf("UMCVoting returned error: %v", err)
	}
	cacheLenAfterFirst := manager.umcVotingCache.Len()
	if cacheLenAfterFirst == 0 {
		t.Fatal("expected UMCVoting cache to be populated")
	}

	second, err := manager.UMCVoting(nil, []*externalapi.DomainHash{c, a, b}, []*externalapi.DomainHash{b, a}, 0)
	if err != nil {
		t.Fatalf("second UMCVoting returned error: %v", err)
	}
	if second != first {
		t.Fatalf("expected memoized UMCVoting result %d, got %d", first, second)
	}
	if manager.umcVotingCache.Len() != cacheLenAfterFirst {
		t.Fatal("expected UMCVoting cache key to be order-insensitive for G and U")
	}

	_, err = manager.UMCVoting(nil, graph, blueSet, 1)
	if err != nil {
		t.Fatalf("UMCVoting with different e returned error: %v", err)
	}
	if manager.umcVotingCache.Len() <= cacheLenAfterFirst {
		t.Fatal("expected UMCVoting cache key to include e")
	}
}
