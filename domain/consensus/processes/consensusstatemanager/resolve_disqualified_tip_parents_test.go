package consensusstatemanager_test

import (
	"fmt"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
)

// The tests in this file pin that a disqualified block does not take its valid ancestors out of the running for virtual's
// selected parent. kaspad keeps a disqualified block in the tips, and selectVirtualSelectedParent replaces it by its
// parents once all their children are disqualified, so virtual stays on the heaviest valid block. calculateNewTips
// instead dropped the disqualified block and its parents, and ResolveVirtual picked virtual's parents only from tips no
// heavier than its next pending tip, so a single invalid block on top of the heavy chain moved virtual onto a lighter
// branch. Every test also runs the invariant checks of resolve_stale_root_test.go after every step.

// heaviestValidBlock is the reference for virtual's selected parent: of the given blocks, the UTXO-valid one with the
// most blue work, the larger hash on a tie (GHOSTDAG's parent-selection order). It reads only GHOSTDAG data and block
// statuses, not the tips or anything else the code under test chooses.
func (h *staleRootHarness) heaviestValidBlock(blocks []*externalapi.DomainHash) *externalapi.DomainHash {
	h.t.Helper()
	stagingArea := model.NewStagingArea()
	var best *externalapi.DomainHash
	var bestData *externalapi.BlockGHOSTDAGData
	for _, block := range blocks {
		if blockStatus(h.t, h.tc, block) != externalapi.StatusUTXOValid {
			continue
		}
		data, err := h.tc.GHOSTDAGDataStore().Get(h.tc.DatabaseContext(), stagingArea, block, false)
		if err != nil {
			h.t.Fatalf("Error getting the GHOSTDAG data of %s: %+v", block, err)
		}
		if best == nil {
			best, bestData = block, data
			continue
		}
		switch data.BlueWork().Cmp(bestData.BlueWork()) {
		case 1:
			best, bestData = block, data
		case 0:
			if best.Less(block) {
				best, bestData = block, data
			}
		}
	}
	return best
}

func (h *staleRootHarness) expectSelectedParent(step string, want *externalapi.DomainHash) {
	h.t.Helper()
	if sp := virtualSelectedParent(h.t, h.tc); !sp.Equal(want) {
		h.t.Fatalf("%s: virtual's selected parent is %s (%s), expected %s (%s)", step, sp, blockStatus(h.t, h.tc, sp),
			want, blockStatus(h.t, h.tc, want))
	}
}

// expectNoDisqualifiedVirtualParent fails if a virtual parent is disqualified: the fix must not start merging disqualified blocks.
func (h *staleRootHarness) expectNoDisqualifiedVirtualParent(step string) {
	h.t.Helper()
	parents, err := h.tc.DAGTopologyManager().Parents(model.NewStagingArea(), model.VirtualBlockHash)
	if err != nil {
		h.t.Fatalf("Error getting virtual's parents: %+v", err)
	}
	for _, parent := range parents {
		if blockStatus(h.t, h.tc, parent) == externalapi.StatusDisqualifiedFromChain {
			h.t.Fatalf("%s: virtual parent %s is disqualified", step, parent)
		}
	}
}

// heavyAndLightBranches builds a common history, a lighter branch B (two blocks) and a heavier chain A (four blocks) on
// it, all resolved, and returns A's blocks and B's tip. Virtual is on A's tip.
func (h *staleRootHarness) heavyAndLightBranches() (aChain []*externalapi.DomainHash, bTip *externalapi.DomainHash) {
	h.t.Helper()
	common := h.chain(h.genesis, 4, true)
	fork := common[len(common)-1]
	bTip = h.chain(fork, 2, true)[1]
	aChain = h.chain(fork, 4, true)
	h.expectSelectedParent("A and B resolved", aChain[len(aChain)-1])
	h.check("A and B resolved")
	return aChain, bTip
}

// TestAddBlockKeepsVirtualOnHeavyChainOverDisqualifiedChild is the AddBlock path: a child of A's tip arrives with
// updateVirtual and is disqualified. calculateNewTips dropped the disqualified block and A's tip with it, so virtual
// moved onto B's lighter tip and A's tip was no longer a tip at all. Virtual must stay on A's tip, the disqualified block
// must not become a virtual parent, and the next valid block on A must extend virtual.
func TestAddBlockKeepsVirtualOnHeavyChainOverDisqualifiedChild(t *testing.T) {
	h := newStaleRootHarness(t, "TestDisqualifiedTipParentsAddBlock")
	aChain, bTip := h.heavyAndLightBranches()
	aTip := aChain[len(aChain)-1]

	disqualified := h.insert(h.build([]*externalapi.DomainHash{aTip}, nil, true), true)
	if status := blockStatus(t, h.tc, disqualified); status != externalapi.StatusDisqualifiedFromChain {
		t.Fatalf("the overpaying child of A's tip is %s, expected %s", status, externalapi.StatusDisqualifiedFromChain)
	}
	h.check("disqualified child of A's tip")
	h.expectSelectedParent("disqualified child of A's tip", aTip)
	h.expectNoDisqualifiedVirtualParent("disqualified child of A's tip")
	if parents, err := h.tc.DAGTopologyManager().Parents(model.NewStagingArea(), model.VirtualBlockHash); err != nil ||
		len(parents) != 2 {
		t.Fatalf("expected virtual to merge A's tip and B's tip %s, got %v (%v)", bTip, parents, err)
	}

	aNext := h.insert(h.build([]*externalapi.DomainHash{aTip}, nil, false), true)
	h.check("valid block on A's tip")
	h.expectSelectedParent("valid block on A's tip", aNext)
}

// TestAddBlockOnGenesisKeepsGenesisOverDisqualifiedChild is the same with nothing but genesis below the invalid block.
// calculateNewTips left no tip, fell back to FindHighestValidBlock, which never picks genesis (DAA score 0), and the
// insertion failed. Virtual must stay on genesis.
func TestAddBlockOnGenesisKeepsGenesisOverDisqualifiedChild(t *testing.T) {
	h := newStaleRootHarness(t, "TestDisqualifiedTipParentsGenesis")

	block, err := h.tryBuild([]*externalapi.DomainHash{h.genesis}, nil, true)
	if err != nil {
		t.Fatalf("Error building the invalid block: %+v", err)
	}
	err = h.tc.ValidateAndInsertBlock(block, true, true)
	if err != nil {
		t.Fatalf("Error inserting an invalid block on genesis: %+v", err)
	}
	h.check("disqualified child of genesis")
	h.expectSelectedParent("disqualified child of genesis", h.genesis)

	next := h.insert(h.build([]*externalapi.DomainHash{h.genesis}, nil, false), true)
	h.check("valid block on genesis")
	h.expectSelectedParent("valid block on genesis", next)
}

// TestResolveVirtualKeepsVirtualOnHeavyChainOverDisqualifiedChild is the ResolveVirtual path: A gets an invalid block
// and a child, left pending as IBD leaves them. Once both are disqualified, ResolveVirtual's next pending tip is B's
// lighter tip, and the completely resolved path picked virtual's parents only from tips no heavier than it. Virtual must
// end on A's tip, whole and in chunks, and the next valid block on A must extend virtual.
func TestResolveVirtualKeepsVirtualOnHeavyChainOverDisqualifiedChild(t *testing.T) {
	for _, chunk := range []uint64{0, 1} {
		t.Run(fmt.Sprintf("chunk=%d", chunk), func(t *testing.T) {
			h := newStaleRootHarness(t, fmt.Sprintf("TestDisqualifiedTipParentsResolve_%d", chunk))
			aChain, _ := h.heavyAndLightBranches()
			aTip := aChain[len(aChain)-1]

			aBad := h.insert(h.build([]*externalapi.DomainHash{aTip}, nil, true), false)
			aAbove := h.insert(h.build([]*externalapi.DomainHash{aBad}, nil, false), false)
			h.check("A's invalid block and its child pending")

			for call := 1; !h.resolve(chunk); call++ {
				if call > 8 {
					t.Fatalf("ResolveVirtual(%d) did not finish in %d calls", chunk, call)
				}
				h.check(fmt.Sprintf("ResolveVirtual(%d) call %d", chunk, call))
			}
			h.check("resolved")
			for _, hash := range []*externalapi.DomainHash{aBad, aAbove} {
				if status := blockStatus(t, h.tc, hash); status != externalapi.StatusDisqualifiedFromChain {
					t.Fatalf("status of %s is %s, expected %s", hash, status, externalapi.StatusDisqualifiedFromChain)
				}
			}
			h.expectSelectedParent("resolved", aTip)
			h.expectNoDisqualifiedVirtualParent("resolved")

			aNext := h.insert(h.build([]*externalapi.DomainHash{aTip}, nil, false), true)
			h.check("valid block on A's tip")
			h.expectSelectedParent("valid block on A's tip", aNext)
		})
	}
}

// TestVirtualSelectedParentIsHeaviestValidBlockOnRandomDAGs runs randomStaleRootScenario with checkSelectedParent, on
// seeds of its own (the database names differ from TestUTXODiffRootInvariantOnRandomDAGs).
func TestVirtualSelectedParentIsHeaviestValidBlockOnRandomDAGs(t *testing.T) {
	for _, seed := range []int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20} {
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			randomStaleRootScenario(t, seed, 50, true)
		})
	}
}

func (h *staleRootHarness) tips() []*externalapi.DomainHash {
	h.t.Helper()
	tips, err := h.tc.ConsensusStateStore().Tips(model.NewStagingArea(), h.tc.DatabaseContext())
	if err != nil {
		h.t.Fatalf("Error getting the tips: %+v", err)
	}
	return tips
}

// expectAntichain fails if a block in blocks is an ancestor of another one.
func (h *staleRootHarness) expectAntichain(step, what string, blocks []*externalapi.DomainHash) {
	h.t.Helper()
	for _, a := range blocks {
		for _, b := range blocks {
			if a.Equal(b) {
				continue
			}
			isAncestor, err := h.tc.DAGTopologyManager().IsAncestorOf(model.NewStagingArea(), a, b)
			if err != nil {
				h.t.Fatalf("Error checking ancestry: %+v", err)
			}
			if isAncestor {
				h.t.Fatalf("%s: %s %v are not an antichain: %s is an ancestor of %s", step, what, blocks, a, b)
			}
		}
	}
}

// TestConsensusResolveVirtualCompletesOverDisqualifiedTopOfOnlyChain goes through the consensus-level ResolveVirtual
// that IBD calls. The only chain gets an invalid block and a child, pending. After the resolve loop it re-reads the
// stored tips and fails with ErrVirtualHasNoUsableTip unless one is valid or pending, so the stored tips must hold the
// valid block below the disqualified ones, not only the disqualified tip.
func TestConsensusResolveVirtualCompletesOverDisqualifiedTopOfOnlyChain(t *testing.T) {
	h := newStaleRootHarness(t, "TestDisqualifiedTipParentsConsensusResolve")
	aChain := h.chain(h.genesis, 4, true)
	aTip := aChain[len(aChain)-1]
	aBad := h.insert(h.build([]*externalapi.DomainHash{aTip}, nil, true), false)
	h.insert(h.build([]*externalapi.DomainHash{aBad}, nil, false), false)
	h.check("A's invalid block and its child pending")

	if err := h.tc.ResolveVirtual(nil); err != nil {
		t.Fatalf("ResolveVirtual: %+v", err)
	}
	h.check("resolved")
	h.expectSelectedParent("resolved", aTip)
	if tips := h.tips(); len(tips) != 1 || !tips[0].Equal(aTip) {
		t.Fatalf("stored tips are %v, expected only A's tip %s", tips, aTip)
	}

	aNext := h.insert(h.build([]*externalapi.DomainHash{aTip}, nil, false), true)
	h.check("valid block on A's tip")
	h.expectSelectedParent("valid block on A's tip", aNext)
}

// TestRestoredTipLeavesTipsInPastOfAnotherTip pins that a block kept as a tip for its disqualified child p leaves the
// tips once another tip has it in its past, in both orders: restored first and then merged (the merger arrives), and
// merged first and then restored (a second disqualified child arrives). Otherwise virtual can take p and a tip in p's
// future as parents: pickVirtualParents skips a candidate only in the past of parents it already chose, and with at
// least maxBlockParents candidates (12 at version 6) it moves the lightest candidates, p among them, ahead of heavier
// ones. Such parents fail checkParentsIncest from ParentsIncestVersion on.
func TestRestoredTipLeavesTipsInPastOfAnotherTip(t *testing.T) {
	h := newStaleRootHarness(t, "TestDisqualifiedTipParentsAntichain")
	base := h.chain(h.genesis, 2, true)[1]
	p := h.insert(h.build([]*externalapi.DomainHash{base}, nil, false), true)
	c := h.insert(h.build([]*externalapi.DomainHash{p}, nil, true), true)
	if status := blockStatus(t, h.tc, c); status != externalapi.StatusDisqualifiedFromChain {
		t.Fatalf("the overpaying child of p is %s, expected %s", status, externalapi.StatusDisqualifiedFromChain)
	}
	x := h.chain(base, 3, true)
	merger := h.insert(h.build([]*externalapi.DomainHash{x[2], c}, nil, false), true) // merges c, so p is in its past
	// Twelve more tips around the merger's weight, so pickVirtualParents reorders: the heaviest is virtual's selected
	// parent, the merger is in the middle and p is the lightest.
	for _, length := range []int{10, 9, 9, 9, 9, 9, 8, 5, 4, 3, 2} {
		h.chain(base, length, true)
	}
	h.check("thirteen branches")
	for _, tip := range h.tips() {
		if tip.Equal(p) {
			t.Fatalf("p %s is still a tip although the tip %s merges its disqualified child", p, merger)
		}
	}
	h.expectAntichain("thirteen branches", "the tips", h.tips())
	parents, err := h.tc.DAGTopologyManager().Parents(model.NewStagingArea(), model.VirtualBlockHash)
	if err != nil {
		t.Fatalf("Error getting virtual's parents: %+v", err)
	}
	h.expectAntichain("thirteen branches", "virtual's parents", parents)

	// A disqualified child of virtual's selected parent that also points at p: the walk reaches both, keeps the
	// selected parent and leaves p out, since the merger still has it in its past.
	selectedParent := virtualSelectedParent(t, h.tc)
	d := h.insert(h.build([]*externalapi.DomainHash{selectedParent, p}, nil, true), true)
	if status := blockStatus(t, h.tc, d); status != externalapi.StatusDisqualifiedFromChain {
		t.Fatalf("the overpaying block on the selected parent and p is %s, expected %s", status,
			externalapi.StatusDisqualifiedFromChain)
	}
	h.check("disqualified block on the selected parent and p")
	h.expectSelectedParent("disqualified block on the selected parent and p", selectedParent)
	for _, tip := range h.tips() {
		if tip.Equal(p) || tip.Equal(d) {
			t.Fatalf("%s is a tip after the disqualified block on the selected parent and p", tip)
		}
	}
	h.expectAntichain("disqualified block on the selected parent and p", "the tips", h.tips())
	parents, err = h.tc.DAGTopologyManager().Parents(model.NewStagingArea(), model.VirtualBlockHash)
	if err != nil {
		t.Fatalf("Error getting virtual's parents: %+v", err)
	}
	h.expectAntichain("disqualified block on the selected parent and p", "virtual's parents", parents)
}

// TestResolveVirtualMovesOntoValidBranchTipWithDiffChild keeps #56's cross-branch move covered: virtual moves onto an
// already UTXO-valid block of another branch whose diff has a diff child. B is resolved and virtual is on B's tip. A,
// heavier, starts with an invalid block on the fork and arrives pending; the chunked resolve makes A's disqualified
// processing point virtual's selected parent (B's tip gets a diff child), and the completed resolve moves virtual back
// onto B's tip, now the heaviest valid block.
func TestResolveVirtualMovesOntoValidBranchTipWithDiffChild(t *testing.T) {
	h := newStaleRootHarness(t, "TestDisqualifiedTipParentsCrossBranch")
	common := h.chain(h.genesis, 4, true)
	fork := common[len(common)-1]
	coinTransaction, coinIndex := h.coinOf(common[2])
	spendCoin := h.spend(coinTransaction, coinIndex)
	bFirst := h.insert(h.build([]*externalapi.DomainHash{fork}, []*externalapi.DomainTransaction{spendCoin}, false), true)
	bTip := h.chain(bFirst, 1, true)[0]
	h.expectSelectedParent("B resolved", bTip)
	h.check("B resolved")

	aBad := h.insert(h.build([]*externalapi.DomainHash{fork}, nil, true), false)
	h.chain(aBad, 3, false)
	h.check("A pending")

	sawDiffChild := false
	for call := 1; !h.resolve(1); call++ {
		if call > 8 {
			t.Fatalf("ResolveVirtual(1) did not finish in %d calls", call)
		}
		h.check(fmt.Sprintf("ResolveVirtual(1) call %d", call))
		if !virtualSelectedParent(t, h.tc).Equal(bTip) && h.hasDiffChild(bTip) {
			sawDiffChild = true
		}
	}
	if !sawDiffChild {
		t.Fatalf("B's tip never had a diff child while virtual was on A")
	}
	h.check("resolved")
	h.expectSelectedParent("resolved", bTip)

	bNext := h.insert(h.build([]*externalapi.DomainHash{bTip}, nil, false), true)
	h.check("valid block on B's tip")
	h.expectSelectedParent("valid block on B's tip", bNext)
}
