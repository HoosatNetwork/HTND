package ghostdagmanager

import (
	"math/big"
	"testing"

	"github.com/Hoosat-Oy/HTND/domain/consensus/model"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/lrucache"
)

type dagKnightCacheTopologyStub struct {
	parents       map[externalapi.DomainHash][]*externalapi.DomainHash
	children      map[externalapi.DomainHash][]*externalapi.DomainHash
	parentsCalls  int
	childrenCalls int
}

type dagKnightCacheTraversalStub struct {
	anticones     map[externalapi.DomainHash][]*externalapi.DomainHash
	anticoneCalls int
}

type dagKnightCacheGHOSTDAGStoreStub struct {
	data     map[externalapi.DomainHash]*externalapi.BlockGHOSTDAGData
	getCalls int
}

func (d *dagKnightCacheTopologyStub) Parents(_ *model.StagingArea, blockHash *externalapi.DomainHash) ([]*externalapi.DomainHash, error) {
	d.parentsCalls++
	return d.parents[*blockHash], nil
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

func (d *dagKnightCacheTraversalStub) LowestChainBlockAboveOrEqualToBlueScore(_ *model.StagingArea, _ *externalapi.DomainHash, _ uint64) (*externalapi.DomainHash, error) {
	panic("not implemented")
}

func (d *dagKnightCacheTraversalStub) SelectedChildIterator(_ *model.StagingArea, _, _ *externalapi.DomainHash, _ bool) (model.BlockIterator, error) {
	panic("not implemented")
}

func (d *dagKnightCacheTraversalStub) SelectedChild(_ *model.StagingArea, _, _ *externalapi.DomainHash) (*externalapi.DomainHash, error) {
	panic("not implemented")
}

func (d *dagKnightCacheTraversalStub) AnticoneFromBlocks(_ *model.StagingArea, _ []*externalapi.DomainHash, blockHash *externalapi.DomainHash, _ uint64) ([]*externalapi.DomainHash, error) {
	d.anticoneCalls++
	return d.anticones[*blockHash], nil
}

func (d *dagKnightCacheTraversalStub) AnticoneFromVirtualPOV(_ *model.StagingArea, _ *externalapi.DomainHash) ([]*externalapi.DomainHash, error) {
	panic("not implemented")
}

func (d *dagKnightCacheTraversalStub) BlockWindowHeapSlice(_ *model.StagingArea, _ *externalapi.DomainHash, _ int) ([]*externalapi.BlockGHOSTDAGDataHashPair, error) {
	panic("not implemented")
}

func (d *dagKnightCacheTraversalStub) BlockWindow(_ *model.StagingArea, _ *externalapi.DomainHash, _ int) ([]*externalapi.DomainHash, error) {
	panic("not implemented")
}

func (d *dagKnightCacheTraversalStub) DAABlockWindow(_ *model.StagingArea, _ *externalapi.DomainHash) ([]*externalapi.DomainHash, error) {
	panic("not implemented")
}

func (d *dagKnightCacheTraversalStub) NewDownHeap(_ *model.StagingArea) model.BlockHeap {
	panic("not implemented")
}

func (d *dagKnightCacheTraversalStub) NewUpHeap(_ *model.StagingArea) model.BlockHeap {
	panic("not implemented")
}

func (d *dagKnightCacheTraversalStub) CalculateChainPath(_ *model.StagingArea, _, _ *externalapi.DomainHash) (*externalapi.SelectedChainPath, error) {
	panic("not implemented")
}

func (d *dagKnightCacheGHOSTDAGStoreStub) Stage(_ *model.StagingArea, blockHash *externalapi.DomainHash, blockGHOSTDAGData *externalapi.BlockGHOSTDAGData, _ bool) {
	d.data[*blockHash] = blockGHOSTDAGData
}

func (d *dagKnightCacheGHOSTDAGStoreStub) IsStaged(_ *model.StagingArea) bool {
	return false
}

func (d *dagKnightCacheGHOSTDAGStoreStub) Get(_ model.DBReader, _ *model.StagingArea, blockHash *externalapi.DomainHash, _ bool) (*externalapi.BlockGHOSTDAGData, error) {
	d.getCalls++
	return d.data[*blockHash], nil
}

func (d *dagKnightCacheGHOSTDAGStoreStub) UnstageAll(_ *model.StagingArea) {
	d.data = make(map[externalapi.DomainHash]*externalapi.BlockGHOSTDAGData)
}

func (d *dagKnightCacheGHOSTDAGStoreStub) CacheLen() int {
	return len(d.data)
}

func TestGetPastUsesCacheWithoutSharingSlices(t *testing.T) {
	genesis := externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{0x01})
	parent := externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{0x02})
	block := externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{0x03})
	replacement := externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{0x7F})

	topology := &dagKnightCacheTopologyStub{
		parents: map[externalapi.DomainHash][]*externalapi.DomainHash{
			*block:  {parent},
			*parent: {genesis},
		},
	}

	manager := &ghostdagManager{
		dagTopologyManager: topology,
		pastCache:          lrucache.New[[]*externalapi.DomainHash](16, false),
		futureCache:        lrucache.New[[]*externalapi.DomainHash](16, false),
		anticoneCache:      lrucache.New[[]*externalapi.DomainHash](16, false),
	}

	graph := []*externalapi.DomainHash{genesis, parent, block}
	first, err := manager.getPast(nil, block, graph)
	if err != nil {
		t.Fatalf("getPast returned error: %v", err)
	}
	if len(first) != 2 || !first[0].Equal(parent) || !first[1].Equal(genesis) {
		t.Fatalf("unexpected getPast result: %v", first)
	}
	if topology.parentsCalls != 3 {
		t.Fatalf("expected three parent traversals, got %d", topology.parentsCalls)
	}

	first[0] = replacement

	second, err := manager.getPast(nil, block, graph)
	if err != nil {
		t.Fatalf("second getPast returned error: %v", err)
	}
	if topology.parentsCalls != 3 {
		t.Fatalf("expected cached getPast call to avoid extra traversal, got %d parent calls", topology.parentsCalls)
	}
	if len(second) != 2 || !second[0].Equal(parent) || !second[1].Equal(genesis) {
		t.Fatalf("cached getPast result was mutated by caller: %v", second)
	}
}

func TestGetFutureUsesCacheWithoutSharingSlices(t *testing.T) {
	genesis := externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{0x11})
	child := externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{0x12})
	grandchild := externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{0x13})
	replacement := externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{0x6F})

	topology := &dagKnightCacheTopologyStub{
		children: map[externalapi.DomainHash][]*externalapi.DomainHash{
			*genesis: {child},
			*child:   {grandchild},
		},
	}

	manager := &ghostdagManager{
		dagTopologyManager: topology,
		pastCache:          lrucache.New[[]*externalapi.DomainHash](16, false),
		futureCache:        lrucache.New[[]*externalapi.DomainHash](16, false),
		anticoneCache:      lrucache.New[[]*externalapi.DomainHash](16, false),
	}

	graph := []*externalapi.DomainHash{genesis, child, grandchild}
	first, err := manager.getFuture(nil, genesis, graph)
	if err != nil {
		t.Fatalf("getFuture returned error: %v", err)
	}
	if len(first) != 2 || !first[0].Equal(child) || !first[1].Equal(grandchild) {
		t.Fatalf("unexpected getFuture result: %v", first)
	}
	if topology.childrenCalls != 3 {
		t.Fatalf("expected three child traversals, got %d", topology.childrenCalls)
	}

	first[0] = replacement

	second, err := manager.getFuture(nil, genesis, graph)
	if err != nil {
		t.Fatalf("second getFuture returned error: %v", err)
	}
	if topology.childrenCalls != 3 {
		t.Fatalf("expected cached getFuture call to avoid extra traversal, got %d child calls", topology.childrenCalls)
	}
	if len(second) != 2 || !second[0].Equal(child) || !second[1].Equal(grandchild) {
		t.Fatalf("cached getFuture result was mutated by caller: %v", second)
	}
}

func TestGetAnticoneUsesCacheWithoutSharingSlices(t *testing.T) {
	block := externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{0x21})
	blue := externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{0x22})
	red := externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{0x23})
	replacement := externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{0x2F})

	traversal := &dagKnightCacheTraversalStub{
		anticones: map[externalapi.DomainHash][]*externalapi.DomainHash{
			*block: {blue, red},
		},
	}

	manager := &ghostdagManager{
		dagTraversalManager: traversal,
		pastCache:           lrucache.New[[]*externalapi.DomainHash](16, false),
		futureCache:         lrucache.New[[]*externalapi.DomainHash](16, false),
		anticoneCache:       lrucache.New[[]*externalapi.DomainHash](16, false),
	}

	graph := []*externalapi.DomainHash{block, blue, red}
	first, err := manager.getAnticone(nil, block, graph)
	if err != nil {
		t.Fatalf("getAnticone returned error: %v", err)
	}
	if len(first) != 2 || !first[0].Equal(blue) || !first[1].Equal(red) {
		t.Fatalf("unexpected getAnticone result: %v", first)
	}
	if traversal.anticoneCalls != 1 {
		t.Fatalf("expected one anticone traversal, got %d", traversal.anticoneCalls)
	}

	first[0] = replacement

	second, err := manager.getAnticone(nil, block, graph)
	if err != nil {
		t.Fatalf("second getAnticone returned error: %v", err)
	}
	if traversal.anticoneCalls != 1 {
		t.Fatalf("expected cached getAnticone call to avoid extra traversal, got %d anticone calls", traversal.anticoneCalls)
	}
	if len(second) != 2 || !second[0].Equal(blue) || !second[1].Equal(red) {
		t.Fatalf("cached getAnticone result was mutated by caller: %v", second)
	}
}

func TestKColouringUsesCacheWithoutSharingSlices(t *testing.T) {
	genesis := externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{0x31})
	parent := externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{0x32})
	block := externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{0x33})
	replacement := externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{0x3F})

	topology := &dagKnightCacheTopologyStub{
		parents: map[externalapi.DomainHash][]*externalapi.DomainHash{
			*block:  {parent},
			*parent: {genesis},
		},
	}
	traversal := &dagKnightCacheTraversalStub{anticones: map[externalapi.DomainHash][]*externalapi.DomainHash{}}
	store := &dagKnightCacheGHOSTDAGStoreStub{
		data: map[externalapi.DomainHash]*externalapi.BlockGHOSTDAGData{
			*genesis: externalapi.NewBlockGHOSTDAGData(0, big.NewInt(0), genesis, nil, nil, nil),
			*parent:  externalapi.NewBlockGHOSTDAGData(0, big.NewInt(0), genesis, nil, nil, nil),
			*block:   externalapi.NewBlockGHOSTDAGData(0, big.NewInt(0), genesis, nil, nil, nil),
		},
	}

	manager := &ghostdagManager{
		dagTopologyManager:  topology,
		dagTraversalManager: traversal,
		ghostdagDataStore:   store,
		pastCache:           lrucache.New[[]*externalapi.DomainHash](16, false),
		futureCache:         lrucache.New[[]*externalapi.DomainHash](16, false),
		anticoneCache:       lrucache.New[[]*externalapi.DomainHash](16, false),
		kColouringCache:     lrucache.New[KColouringResult](16, false),
		umcVotingCache:      lrucache.New[int](16, false),
	}

	graph := []*externalapi.DomainHash{block, genesis, parent}
	first, err := manager.KColouring(nil, block, graph, 0, false, nil)
	if err != nil {
		t.Fatalf("KColouring returned error: %v", err)
	}
	if manager.kColouringCache.Len() == 0 {
		t.Fatal("expected KColouring cache to be populated")
	}
	getCallsAfterFirst := store.getCalls

	first.Blues[0] = replacement

	second, err := manager.KColouring(nil, block, []*externalapi.DomainHash{parent, block, genesis}, 0, false, nil)
	if err != nil {
		t.Fatalf("second KColouring returned error: %v", err)
	}
	if store.getCalls != getCallsAfterFirst {
		t.Fatalf("expected cached KColouring call to avoid extra ghostdag data lookups, got %d additional calls", store.getCalls-getCallsAfterFirst)
	}
	if len(second.Blues) == 0 || second.Blues[0].Equal(replacement) {
		t.Fatalf("cached KColouring blues were mutated by caller: %v", second.Blues)
	}

	cacheLenAfterSameCall := manager.kColouringCache.Len()
	_, err = manager.KColouring(nil, block, graph, 1, false, nil)
	if err != nil {
		t.Fatalf("KColouring with different k returned error: %v", err)
	}
	if manager.kColouringCache.Len() <= cacheLenAfterSameCall {
		t.Fatal("expected KColouring cache key to include k")
	}

	cacheLenAfterKChange := manager.kColouringCache.Len()
	_, err = manager.KColouring(nil, block, graph, 1, true, nil)
	if err != nil {
		t.Fatalf("KColouring with freeSearch returned error: %v", err)
	}
	if manager.kColouringCache.Len() <= cacheLenAfterKChange {
		t.Fatal("expected KColouring cache key to include freeSearch")
	}
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
		pastCache:          lrucache.New[[]*externalapi.DomainHash](16, false),
		futureCache:        lrucache.New[[]*externalapi.DomainHash](16, false),
		anticoneCache:      lrucache.New[[]*externalapi.DomainHash](16, false),
		kColouringCache:    lrucache.New[KColouringResult](16, false),
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
