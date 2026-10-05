package consensusstatemanager

import (
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/database"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/hashset"
)

type cacheTestConsensusStateStore struct {
	model.ConsensusStateStore
	tip *externalapi.DomainHash
}

func (s cacheTestConsensusStateStore) Tips(*model.StagingArea, model.DBReader) ([]*externalapi.DomainHash, error) {
	return []*externalapi.DomainHash{s.tip}, nil
}

type cacheTestNoVirtualGHOSTDAGDataStore struct {
	model.GHOSTDAGDataStore
}

func (cacheTestNoVirtualGHOSTDAGDataStore) Get(model.DBReader, *model.StagingArea, *externalapi.DomainHash, bool,
) (*externalapi.BlockGHOSTDAGData, error) {
	// findNextPendingTip looks up virtual's own GHOSTDAG data first, purely to resolve which ordering
	// to use. Not-found here makes it fall back to the process-global block version, same as a fresh
	// consensus with no virtual GHOSTDAG data staged yet.
	return nil, database.ErrNotFound
}

type cacheTestOrderedGHOSTDAGManager struct {
	model.GHOSTDAGManager
	order []*externalapi.DomainHash
}

func (m cacheTestOrderedGHOSTDAGManager) OrderDAG(*model.StagingArea, []*externalapi.DomainHash,
) (*externalapi.DomainHash, []*externalapi.DomainHash, error) {
	return m.order[0], m.order, nil
}

type cacheTestCountingFinalityManager struct {
	model.FinalityManager
	virtualFinalityPoint *externalapi.DomainHash
	calls                *int
}

func (m cacheTestCountingFinalityManager) VirtualFinalityPoint(*model.StagingArea) (*externalapi.DomainHash, error) {
	*m.calls++
	return m.virtualFinalityPoint, nil
}

type cacheTestPruningStore struct {
	model.PruningStore
	pruningPoint *externalapi.DomainHash
}

func (s cacheTestPruningStore) PruningPoint(model.DBReader, *model.StagingArea) (*externalapi.DomainHash, error) {
	return s.pruningPoint, nil
}

// cacheTestViolatingDAGTopologyManager makes isViolatingFinality see the finality point as NOT in the
// past of the pruning point (IsAncestorOf false) and the checked tip as NOT on the finality point's
// selected parent chain (IsInSelectedParentChainOf false) - the combination that reports a genuine,
// notify-worthy finality violation.
type cacheTestViolatingDAGTopologyManager struct {
	model.DAGTopologyManager
}

func (cacheTestViolatingDAGTopologyManager) IsAncestorOf(*model.StagingArea, *externalapi.DomainHash, *externalapi.DomainHash,
) (bool, error) {
	return false, nil
}

func (cacheTestViolatingDAGTopologyManager) IsInSelectedParentChainOf(*model.StagingArea, *externalapi.DomainHash, *externalapi.DomainHash,
) (bool, error) {
	return false, nil
}

type cacheTestHeaderSelectedTipStore struct {
	model.HeaderSelectedTipStore
	selectedTip *externalapi.DomainHash
}

func (s cacheTestHeaderSelectedTipStore) HeadersSelectedTip(model.DBReader, *model.StagingArea) (*externalapi.DomainHash, error) {
	return s.selectedTip, nil
}

type cacheTestValidBlockStatusStore struct {
	model.BlockStatusStore
}

func (cacheTestValidBlockStatusStore) Get(model.DBReader, *model.StagingArea, *externalapi.DomainHash,
) (externalapi.BlockStatus, error) {
	return externalapi.StatusUTXOValid, nil
}

// buildKnownFinalityViolatingTipsCacheTestCSM wires a consensusStateManager whose single DAG tip
// genuinely, deterministically fails isViolatingFinality's checks (see
// cacheTestViolatingDAGTopologyManager), with the finality manager's VirtualFinalityPoint call
// counted so the test can prove whether isViolatingFinality's real logic ran again.
func buildKnownFinalityViolatingTipsCacheTestCSM(t *testing.T) (csm *consensusStateManager,
	violatingTip *externalapi.DomainHash, finalityManagerCalls *int,
) {
	t.Helper()

	genesisHash := externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{0xff})
	violatingTip = externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{1})
	virtualFinalityPointHash := externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{2})
	pruningPointHash := externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{3})

	finalityManagerCalls = new(int)

	// findNextPendingTip's DAGKnight-vs-GHOSTDAG ordering choice depends on the process-global block
	// version. Force it to a DAGKnight-ordered version (>= 6, matching current mainnet) so the fixture
	// doesn't depend on whatever another test left the global at, then restore it afterwards.
	originalVersion := constants.GetBlockVersion()
	constants.ForceSetBlockVersion(6)
	t.Cleanup(func() { constants.ForceSetBlockVersion(uint(originalVersion)) })

	csm = &consensusStateManager{
		genesisHash:                genesisHash,
		consensusStateStore:        cacheTestConsensusStateStore{tip: violatingTip},
		ghostdagDataStore:          cacheTestNoVirtualGHOSTDAGDataStore{},
		ghostdagManager:            cacheTestOrderedGHOSTDAGManager{order: []*externalapi.DomainHash{violatingTip}},
		finalityManager:            cacheTestCountingFinalityManager{virtualFinalityPoint: virtualFinalityPointHash, calls: finalityManagerCalls},
		pruningStore:               cacheTestPruningStore{pruningPoint: pruningPointHash},
		dagTopologyManager:         cacheTestViolatingDAGTopologyManager{},
		headersSelectedTipStore:    cacheTestHeaderSelectedTipStore{selectedTip: genesisHash},
		blockStatusStore:           cacheTestValidBlockStatusStore{},
		knownFinalityViolatingTips: hashset.New(),
	}
	return csm, violatingTip, finalityManagerCalls
}

// TestFindNextPendingTipCachesAKnownFinalityViolatingTip is the regression test for the
// findNextPendingTip fast path added on 2026-09-19: during the mainnet backlog-catchup incident,
// ResolveVirtual ran hundreds of chunks back to back, and findNextPendingTip re-lists every current
// DAG tip and re-runs isViolatingFinality on all of them on every single chunk - including tips it had
// already confirmed violate finality on a previous chunk. That check is monotonic (the finality/
// pruning point it compares against only ever moves forward), so re-checking an already-confirmed-
// violating tip can never change the answer; it only wastes consensus-lock time mining and IBD are
// also contending for.
//
// This pins the fast path directly: with a single DAG tip rigged to fail isViolatingFinality every
// time it is actually evaluated, a first call must evaluate it for real (one VirtualFinalityPoint
// call) and a second call for the same staging area must not - it must be answered purely from
// knownFinalityViolatingTips.
func TestFindNextPendingTipCachesAKnownFinalityViolatingTip(t *testing.T) {
	csm, violatingTip, finalityManagerCalls := buildKnownFinalityViolatingTipsCacheTestCSM(t)
	stagingArea := model.NewStagingArea()

	if csm.knownFinalityViolatingTips.Contains(violatingTip) {
		t.Fatalf("expected the cache to start empty")
	}

	pendingTip, status, err := csm.findNextPendingTip(stagingArea)
	if err != nil {
		t.Fatalf("findNextPendingTip (first call): %+v", err)
	}
	// The rigged tip is the only DAG tip and it violates finality, so findNextPendingTip must fall
	// back to the headers selected tip chain and land on genesis, exactly as it would in a real DAG
	// where every current tip is disqualified.
	if !pendingTip.Equal(csm.genesisHash) || status != externalapi.StatusUTXOValid {
		t.Fatalf("expected the fallback to land on genesis as StatusUTXOValid, got %s/%s", pendingTip, status)
	}
	if *finalityManagerCalls != 1 {
		t.Fatalf("expected exactly one real isViolatingFinality evaluation on the first call, got %d calls",
			*finalityManagerCalls)
	}
	if !csm.knownFinalityViolatingTips.Contains(violatingTip) {
		t.Fatalf("expected the confirmed-violating tip to be cached after the first call")
	}

	pendingTip, status, err = csm.findNextPendingTip(stagingArea)
	if err != nil {
		t.Fatalf("findNextPendingTip (second call): %+v", err)
	}
	if !pendingTip.Equal(csm.genesisHash) || status != externalapi.StatusUTXOValid {
		t.Fatalf("expected the same fallback result on the second call, got %s/%s", pendingTip, status)
	}
	if *finalityManagerCalls != 1 {
		t.Fatalf("expected the second call to skip the real isViolatingFinality evaluation via the cache, "+
			"but VirtualFinalityPoint was called again (total calls now %d)", *finalityManagerCalls)
	}
}
