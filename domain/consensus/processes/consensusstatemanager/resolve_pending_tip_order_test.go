package consensusstatemanager_test

import (
	"fmt"
	"sort"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
)

// TestResolveVirtualDoesNotStopAtLowerHashLighterTip pins that ResolveVirtual leaves virtual on the heaviest valid tip
// when the first pending tip in findNextPendingTip's order is lighter than virtual's UTXO-valid selected parent.
//
// Shape: virtual's selected parent M is valid and no longer a tip, because the
// heavier chain H above it was inserted without updating virtual (IBD). A lighter pending tip L has a smaller hash
// than H's tip. From block version 6 findNextPendingTip used to order tips by DAGKnight, which on a tip set is ascending
// hash, so L was tried first, lost to M (isNewSelectedTip) and ResolveVirtual reported "completely resolved" without
// ever looking at H. Every later call took the same order and returned the same answer.
func TestResolveVirtualDoesNotStopAtLowerHashLighterTip(t *testing.T) {
	previous := constants.GetBlockVersion()
	t.Cleanup(func() { constants.ForceSetBlockVersion(uint(previous)) })

	params := dagconfig.MainnetParams
	params.POWScores = []uint64{1, 1, 1, 1, 1} // every block past genesis is version 6
	factory := consensus.NewFactory()
	config := &consensus.Config{Params: params}
	config.SkipProofOfWork = true
	tc, teardown, err := factory.NewTestConsensus(config, "TestResolveVirtualDoesNotStopAtLowerHashLighterTip")
	if err != nil {
		t.Fatalf("Error setting up consensus: %+v", err)
	}
	defer teardown(false)
	builderConfig := &consensus.Config{Params: params}
	builderConfig.SkipProofOfWork = true
	builder, teardownBuilder, err := factory.NewTestConsensus(builderConfig,
		"TestResolveVirtualDoesNotStopAtLowerHashLighterTip_builder")
	if err != nil {
		t.Fatalf("Error setting up builder consensus: %+v", err)
	}
	defer teardownBuilder(false)
	// NewTestConsensus resets the process-wide block version; pin it after the last consensus exists.
	constants.ForceSetBlockVersion(6)

	scriptPublicKey := &externalapi.ScriptPublicKey{Script: nil, Version: 0}
	build := func(parent *externalapi.DomainHash, extraData string) (*externalapi.DomainHash, *externalapi.DomainBlock) {
		hash, _, err := builder.AddBlock([]*externalapi.DomainHash{parent},
			&externalapi.DomainCoinbaseData{ScriptPublicKey: scriptPublicKey, ExtraData: []byte(extraData)}, nil)
		if err != nil {
			t.Fatalf("Error building %q: %+v", extraData, err)
		}
		block, found, err := builder.GetBlock(hash)
		if err != nil || !found {
			t.Fatalf("Error getting %q from the builder: found=%t err=%+v", extraData, found, err)
		}
		return hash, block
	}
	insert := func(block *externalapi.DomainBlock, updateVirtual bool) {
		if err := tc.ValidateAndInsertBlock(block, updateVirtual, true); err != nil {
			t.Fatalf("Error inserting a block: %+v", err)
		}
	}

	// M: three blocks, resolved on arrival, so M's tip is virtual's UTXO-valid selected parent.
	selectedParent := params.GenesisHash
	for i := range 3 {
		var block *externalapi.DomainBlock
		selectedParent, block = build(selectedParent, fmt.Sprintf("main %d", i))
		insert(block, true)
	}
	// H: two more blocks on M, inserted as IBD does (pending, virtual not updated).
	heavierTip := selectedParent
	for i := range 2 {
		var block *externalapi.DomainBlock
		heavierTip, block = build(heavierTip, fmt.Sprintf("heavier %d", i))
		insert(block, false)
	}
	// L: one block on genesis whose hash is below H's tip. Candidates only live in the builder until one qualifies.
	var lighterTip *externalapi.DomainHash
	for i := 0; i < 256 && lighterTip == nil; i++ {
		hash, block := build(params.GenesisHash, fmt.Sprintf("lighter %d", i))
		if hash.Less(heavierTip) {
			lighterTip = hash
			insert(block, false)
		}
	}
	if lighterTip == nil {
		t.Fatalf("no lighter candidate hashed below the heavier tip %s", heavierTip)
	}

	// Preconditions: L is lighter than M, H's tip is the heaviest tip, both pending, M valid and not a tip, and the
	// unpatched order (tipsInDecreasingDAGKnightOrder, reproduced here) tries L before H.
	stagingArea := model.NewStagingArea()
	if winner, err := tc.GHOSTDAGManager().ChooseSelectedParent(stagingArea, lighterTip, selectedParent); err != nil ||
		!winner.Equal(selectedParent) {
		t.Fatalf("the lighter tip must lose to virtual's selected parent: winner=%s err=%v", winner, err)
	}
	if winner, err := tc.GHOSTDAGManager().ChooseSelectedParent(stagingArea, heavierTip, selectedParent); err != nil ||
		!winner.Equal(heavierTip) {
		t.Fatalf("the heavier tip must win virtual's selected parent: winner=%s err=%v", winner, err)
	}
	if sp := virtualSelectedParent(t, tc); !sp.Equal(selectedParent) {
		t.Fatalf("virtual's selected parent is %s, expected %s", sp, selectedParent)
	}
	for hash, want := range map[*externalapi.DomainHash]externalapi.BlockStatus{
		selectedParent: externalapi.StatusUTXOValid,
		heavierTip:     externalapi.StatusUTXOPendingVerification,
		lighterTip:     externalapi.StatusUTXOPendingVerification,
	} {
		if got := blockStatus(t, tc, hash); got != want {
			t.Fatalf("status of %s is %s, expected %s", hash, got, want)
		}
	}
	tips, err := tc.ConsensusStateStore().Tips(stagingArea, tc.DatabaseContext())
	if err != nil {
		t.Fatalf("Error getting the tips: %+v", err)
	}
	if len(tips) != 2 {
		t.Fatalf("expected exactly the lighter and the heavier tip, got %s", tips)
	}
	_, ordering, err := tc.GHOSTDAGManager().OrderDAG(stagingArea, tips)
	if err != nil {
		t.Fatalf("Error ordering the tips: %+v", err)
	}
	position := make(map[externalapi.DomainHash]int)
	for i, hash := range ordering {
		position[*hash] = i
	}
	sort.Slice(tips, func(i, j int) bool { return position[*tips[i]] < position[*tips[j]] })
	if !tips[0].Equal(lighterTip) {
		t.Fatalf("DAGKnight order puts %s first, expected the lower-hash lighter tip %s", tips[0], lighterTip)
	}

	t.Logf("virtual selected parent %s (valid), lighter tip %s, heavier tip %s (both pending)",
		selectedParent, lighterTip, heavierTip)
	// Three calls: a stop at the lighter tip repeats identically, progress ends the loop.
	for call := 1; call <= 3; call++ {
		_, isCompletelyResolved, err := tc.ResolveVirtualWithMaxParam(0)
		if err != nil {
			t.Fatalf("ResolveVirtual call %d: %+v", call, err)
		}
		sp := virtualSelectedParent(t, tc)
		t.Logf("ResolveVirtual call %d: complete=%t, virtual selected parent %s, heavier tip %s",
			call, isCompletelyResolved, sp, blockStatus(t, tc, heavierTip))
		if isCompletelyResolved && sp.Equal(heavierTip) {
			break
		}
	}
	if sp := virtualSelectedParent(t, tc); !sp.Equal(heavierTip) {
		t.Fatalf("ResolveVirtual left virtual's selected parent on %s while the heavier tip %s is %s", sp, heavierTip,
			blockStatus(t, tc, heavierTip))
	}
	if status := blockStatus(t, tc, heavierTip); status != externalapi.StatusUTXOValid {
		t.Fatalf("the heavier tip is %s, expected %s", status, externalapi.StatusUTXOValid)
	}
}
