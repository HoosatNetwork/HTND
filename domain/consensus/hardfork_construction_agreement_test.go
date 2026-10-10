package consensus

import (
	"math"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/hardforks"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
	"github.com/HoosatNetwork/HTND/v2/domain/prefixmanager/prefix"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database/ldb"
)

// TestTwoConsensusesBuiltAtDifferentVersionsAgreeOnEverythingGated is Workstream C's agreement test.
//
// Two consensus objects are fed byte-identical blocks. One is constructed while the process-global
// block version is 1 - a node that just started - and the other while it is high, as it is on a node
// that finished a headers-proof IBD without restarting, since the global is raised to the tip
// version before the staging consensus is built.
//
// They must agree on everything the hardfork gates can reach. If construction time can still change
// any of these answers, then adding version-gated rules on top makes two nodes holding the same
// blocks disagree about which rules apply - which is HTN-001/003's defect reappearing at the level
// of the fork mechanism rather than at the level of a parameter.
//
// TestPruningAndFinalityDoNotDependOnConstructionTime covers the pruning point and the finality
// point. This covers the rest of what the plan names - the pruning point LIST, block status, and
// template bits - and re-checks those two so that a failure here is self-contained.
func TestTwoConsensusesBuiltAtDifferentVersionsAgreeOnEverythingGated(t *testing.T) {
	defer constants.ForceSetBlockVersion(1)

	config := &Config{Params: dagconfig.MainnetParams}
	config.SkipProofOfWork = true
	// Every block built below is version 1, whatever the global says at construction time.
	config.DisableDifficultyAdjustment = true
	config.POWScores = []uint64{math.MaxUint64}
	targetTime := config.TargetTimePerBlock[0]
	config.TargetTimePerBlock = []time.Duration{targetTime}
	// Finality is 10 blocks for versions 1-4 and 20 from version 5, so the two constructions would
	// differ if anything still froze a depth at construction time.
	config.FinalityDuration = []time.Duration{
		10 * targetTime, 10 * targetTime, 10 * targetTime, 10 * targetTime, 20 * targetTime,
	}
	config.K = []externalapi.KType{2}
	config.MergeSetSizeLimit = 2
	config.PruningMultiplier = []uint64{1}

	factory := NewFactory()
	freshNode, teardown, err := factory.NewTestConsensus(config, "TestGatedAgreementFresh")
	if err != nil {
		t.Fatalf("NewTestConsensus: %+v", err)
	}
	defer teardown(false)

	db, err := ldb.NewLevelDB(t.TempDir(), 8)
	if err != nil {
		t.Fatalf("NewLevelDB: %+v", err)
	}
	defer db.Close()

	constants.ForceSetBlockVersion(9)
	ibdNodeInterface, _, err := factory.NewConsensus(config, db, &prefix.Prefix{}, nil)
	constants.ForceSetBlockVersion(1)
	if err != nil {
		t.Fatalf("NewConsensus: %+v", err)
	}
	ibdNode := ibdNodeInterface.(*consensus)

	const chainLength = 200
	tip := config.GenesisHash
	for i := range chainLength {
		tip, _, err = freshNode.AddBlock([]*externalapi.DomainHash{tip}, nil, nil)
		if err != nil {
			t.Fatalf("AddBlock #%d: %+v", i, err)
		}
		block, found, err := freshNode.GetBlock(tip)
		if err != nil || !found {
			t.Fatalf("GetBlock #%d: found %t, %+v", i, found, err)
		}
		if err := ibdNode.ValidateAndInsertBlock(block, true, true); err != nil {
			t.Fatalf("ValidateAndInsertBlock #%d: %+v", i, err)
		}
	}

	freshConsensus := freshNode.(*testConsensus).consensus

	// 1. The pruning point.
	freshPruningPoint, err := freshConsensus.PruningPoint()
	if err != nil {
		t.Fatalf("PruningPoint: %+v", err)
	}
	if freshPruningPoint.Equal(config.GenesisHash) {
		t.Fatalf("the pruning point never moved in %d blocks, so this test proves nothing", chainLength)
	}
	ibdPruningPoint, err := ibdNode.PruningPoint()
	if err != nil {
		t.Fatalf("PruningPoint: %+v", err)
	}
	if !freshPruningPoint.Equal(ibdPruningPoint) {
		t.Errorf("pruning point differs by construction time: %s (built at version 1) vs %s (version 9)",
			freshPruningPoint, ibdPruningPoint)
	}

	// 2. The whole pruning point list, which PruningPointHeaders walks with PruningPointByIndex.
	freshPruningHeaders, err := freshConsensus.PruningPointHeaders()
	if err != nil {
		t.Fatalf("PruningPointHeaders: %+v", err)
	}
	ibdPruningHeaders, err := ibdNode.PruningPointHeaders()
	if err != nil {
		t.Fatalf("PruningPointHeaders: %+v", err)
	}
	if len(freshPruningHeaders) != len(ibdPruningHeaders) {
		t.Errorf("pruning point list length differs by construction time: %d (version 1) vs %d (version 9)",
			len(freshPruningHeaders), len(ibdPruningHeaders))
	} else {
		for i := range freshPruningHeaders {
			freshHash := freshPruningHeaders[i].BlockLevel(config.MaxBlockLevel)
			ibdHash := ibdPruningHeaders[i].BlockLevel(config.MaxBlockLevel)
			if freshPruningHeaders[i].DAAScore() != ibdPruningHeaders[i].DAAScore() || freshHash != ibdHash {
				t.Errorf("pruning point list entry %d differs by construction time: DAA %d vs %d",
					i, freshPruningHeaders[i].DAAScore(), ibdPruningHeaders[i].DAAScore())
			}
		}
	}

	// 3. The finality point.
	freshFinalityPoint, err := freshConsensus.finalityManager.VirtualFinalityPoint(model.NewStagingArea())
	if err != nil {
		t.Fatalf("VirtualFinalityPoint: %+v", err)
	}
	ibdFinalityPoint, err := ibdNode.finalityManager.VirtualFinalityPoint(model.NewStagingArea())
	if err != nil {
		t.Fatalf("VirtualFinalityPoint: %+v", err)
	}
	if !freshFinalityPoint.Equal(ibdFinalityPoint) {
		t.Errorf("finality point differs by construction time: %s (version 1) vs %s (version 9)",
			freshFinalityPoint, ibdFinalityPoint)
	}

	// 4. The status of the tip - the verdict the gated rules would change.
	freshStatus, err := freshConsensus.blockStatusStore.Get(
		freshConsensus.databaseContext, model.NewStagingArea(), tip)
	if err != nil {
		t.Fatalf("reading the tip status from the fresh node: %+v", err)
	}
	ibdStatus, err := ibdNode.blockStatusStore.Get(ibdNode.databaseContext, model.NewStagingArea(), tip)
	if err != nil {
		t.Fatalf("reading the tip status from the IBD node: %+v", err)
	}
	if freshStatus != ibdStatus {
		t.Errorf("the same block has status %s on a node built at version 1 and %s on one built at "+
			"version 9", freshStatus, ibdStatus)
	}

	// 5. Template bits - what each node would tell a miner to work against. HTN-007's gated rule
	// compares a header's bits against exactly this, so a disagreement here would mean each node
	// rejecting the other's blocks once that rule activates.
	emptyCoinbase := &externalapi.DomainCoinbaseData{
		ScriptPublicKey: &externalapi.ScriptPublicKey{Script: nil, Version: 0},
	}
	freshTemplate, err := freshConsensus.BuildBlockTemplate(emptyCoinbase, nil)
	if err != nil {
		t.Fatalf("BuildBlockTemplate on the fresh node: %+v", err)
	}
	ibdTemplate, err := ibdNode.BuildBlockTemplate(emptyCoinbase, nil)
	if err != nil {
		t.Fatalf("BuildBlockTemplate on the IBD node: %+v", err)
	}
	if freshTemplate.Block.Header.Bits() != ibdTemplate.Block.Header.Bits() {
		t.Errorf("template bits differ by construction time: %08x (version 1) vs %08x (version 9). "+
			"Once ValidateHeaderBitsVersion activates, each node would reject the other's blocks.",
			freshTemplate.Block.Header.Bits(), ibdTemplate.Block.Header.Bits())
	}

	// 6. And none of the gates may have been reachable during any of the above.
	for name, gate := range map[string]uint16{
		"StrictUTXOCommitmentVersion":   hardforks.StrictUTXOCommitmentVersion,
		"RefuseMismatchedImportVersion": hardforks.RefuseMismatchedImportVersion,
		"ValidateHeaderBitsVersion":     hardforks.ValidateHeaderBitsVersion,
		"ValidateIBDPruningListVersion": hardforks.ValidateIBDPruningListVersion,
	} {
		if hardforks.Active(gate, 9) {
			t.Errorf("%s was active at block version 9 during this run", name)
		}
	}
}
