package consensusstatemanager_test

import (
	"fmt"
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus"
	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/blockheader"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/domain/dagconfig"
)

// TestResolveVirtualKeepsValidSelectedParentOverLighterPendingTip pins that ResolveVirtual does not move virtual to a
// pending chain that does not overcome virtual's UTXO-valid selected parent.
//
// From block version 6 findNextPendingTip orders tips by DAGKnight, whose tie-breaking is by hash, while the resolve
// processing point has to win virtual's selected parent by blue work. Virtual's selected parent here is the valid tip of
// the heavier chain, and DAGKnight orders the tip of a lighter pending side chain, longer than one resolve chunk, ahead
// of it, so no block of that chain wins the selected parent. ResolveVirtual used to resolve the whole lighter chain and
// make its tip virtual's only parent, so virtual left the heavier valid chain for the lighter one.
func TestResolveVirtualKeepsValidSelectedParentOverLighterPendingTip(t *testing.T) {
	previous := constants.GetBlockVersion()
	t.Cleanup(func() { constants.ForceSetBlockVersion(uint(previous)) })

	for attempt := 0; attempt < 20; attempt++ {
		if lighterPendingTipScenario(t, attempt) {
			return
		}
	}
	t.Fatalf("in 20 attempts DAGKnight never ordered the lighter pending tip ahead of the heavier valid tip, so the " +
		"case this test is about never happened")
}

// lighterPendingTipScenario runs the scenario once and reports whether DAGKnight ordered the lighter pending tip first.
// The side chain's coinbase data changes with the attempt, which changes its hashes and so the tie-break.
func lighterPendingTipScenario(t *testing.T, attempt int) bool {
	params := dagconfig.MainnetParams
	params.POWScores = []uint64{1, 1, 1, 1, 1} // every block past genesis is version 6
	factory := consensus.NewFactory()
	config := &consensus.Config{Params: params}
	config.SkipProofOfWork = true
	tc, teardown, err := factory.NewTestConsensus(config, fmt.Sprintf("TestLighterPendingTip_%d", attempt))
	if err != nil {
		t.Fatalf("Error setting up consensus: %+v", err)
	}
	defer teardown(false)
	builderConfig := &consensus.Config{Params: params}
	builderConfig.SkipProofOfWork = true
	builder, teardownBuilder, err := factory.NewTestConsensus(builderConfig, fmt.Sprintf("TestLighterPendingTipBuilder_%d", attempt))
	if err != nil {
		t.Fatalf("Error setting up builder consensus: %+v", err)
	}
	defer teardownBuilder(false)
	// NewTestConsensus resets the process-wide block version; pin it after the last node exists.
	constants.ForceSetBlockVersion(6)

	genesisHash := params.GenesisHash
	scriptPublicKey := &externalapi.ScriptPublicKey{Script: nil, Version: 0}

	const heavierChainLength = 5
	heavierTip := genesisHash
	for i := range heavierChainLength {
		heavierTip, _, err = tc.AddBlock([]*externalapi.DomainHash{heavierTip},
			&externalapi.DomainCoinbaseData{ScriptPublicKey: scriptPublicKey, ExtraData: []byte("heavier")}, nil)
		if err != nil {
			t.Fatalf("Error adding block %d of the heavier chain: %+v", i, err)
		}
	}

	// A valid child of the heavier tip whose header commits to a UTXO set it does not have, so it is disqualified when
	// it is resolved, and the heavier tip stays virtual's selected parent and a DAG tip.
	child, _, err := tc.BuildBlockWithParents([]*externalapi.DomainHash{heavierTip},
		&externalapi.DomainCoinbaseData{ScriptPublicKey: scriptPublicKey, ExtraData: []byte("disqualified")}, nil)
	if err != nil {
		t.Fatalf("Error building the child of the heavier tip: %+v", err)
	}
	child.Header = blockheader.NewImmutableBlockHeader(
		child.Header.Version(),
		child.Header.Parents(),
		child.Header.HashMerkleRoot(),
		child.Header.AcceptedIDMerkleRoot(),
		externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{1}),
		child.Header.TimeInMilliseconds(),
		child.Header.Bits(),
		child.Header.Nonce(),
		child.Header.DAAScore(),
		child.Header.BlueScore(),
		child.Header.BlueWork(),
		child.Header.PruningPoint(),
	)
	err = tc.ValidateAndInsertBlock(child, true, true)
	if err != nil {
		t.Fatalf("Error inserting the child of the heavier tip: %+v", err)
	}
	status, err := tc.BlockStatusStore().Get(tc.DatabaseContext(), model.NewStagingArea(), consensushashing.BlockHash(child))
	if err != nil {
		t.Fatalf("Error getting the child's status: %+v", err)
	}
	if status != externalapi.StatusDisqualifiedFromChain {
		t.Fatalf("expected the child of the heavier tip to be disqualified, got %s", status)
	}
	if selectedParent := virtualSelectedParent(t, tc); !selectedParent.Equal(heavierTip) {
		t.Fatalf("expected virtual's selected parent to stay on the heavier tip %s, got %s", heavierTip, selectedParent)
	}

	const lighterChainLength = 3
	lighterTip := genesisHash
	for i := range lighterChainLength {
		lighterTip, _, err = builder.AddBlock([]*externalapi.DomainHash{lighterTip}, &externalapi.DomainCoinbaseData{
			ScriptPublicKey: scriptPublicKey, ExtraData: []byte(fmt.Sprintf("lighter %d", attempt)),
		}, nil)
		if err != nil {
			t.Fatalf("Error adding block %d of the lighter chain to the builder: %+v", i, err)
		}
		block, found, err := builder.GetBlock(lighterTip)
		if err != nil || !found {
			t.Fatalf("Error getting block %d of the lighter chain: found=%t err=%+v", i, found, err)
		}
		err = tc.ValidateAndInsertBlock(block, false, true)
		if err != nil {
			t.Fatalf("Error inserting block %d of the lighter chain: %+v", i, err)
		}
	}

	stagingArea := model.NewStagingArea()
	tips, err := tc.ConsensusStateStore().Tips(stagingArea, tc.DatabaseContext())
	if err != nil {
		t.Fatalf("Error getting the tips: %+v", err)
	}
	_, ordering, err := tc.GHOSTDAGManager().OrderDAG(stagingArea, tips)
	if err != nil {
		t.Fatalf("Error ordering the tips: %+v", err)
	}
	lighterFirst := false
	for _, hash := range ordering {
		if hash.Equal(heavierTip) {
			break
		}
		if hash.Equal(lighterTip) {
			lighterFirst = true
			break
		}
	}
	if !lighterFirst {
		return false
	}
	t.Logf("attempt %d: DAGKnight ordered the lighter pending tip %s ahead of the heavier valid tip %s", attempt,
		lighterTip, heavierTip)

	_, isCompletelyResolved, err := tc.ResolveVirtualWithMaxParam(2)
	if err != nil {
		t.Fatalf("Error resolving virtual: %+v", err)
	}
	if selectedParent := virtualSelectedParent(t, tc); !selectedParent.Equal(heavierTip) {
		t.Fatalf("virtual's selected parent moved from the UTXO-valid heavier tip %s to %s (lighter pending tip %s)",
			heavierTip, selectedParent, lighterTip)
	}
	if !isCompletelyResolved {
		t.Fatalf("expected ResolveVirtual to report virtual resolved when it keeps its UTXO-valid selected parent")
	}
	return true
}
