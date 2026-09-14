package consensus

import (
	"math"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/domain/dagconfig"
	"github.com/HoosatNetwork/HTND/domain/prefixmanager/prefix"
	"github.com/HoosatNetwork/HTND/infrastructure/db/database/ldb"
)

// TestPruningAndFinalityDoNotDependOnConstructionTime pins that the finality and pruning depths a consensus uses come
// from the blocks it evaluates, not from the process-global block version at the moment the consensus object was
// built. They were read once at construction: a node started fresh (global version 1) and a node that finished a
// headers-proof IBD without restarting (the global is raised to the tip version before its staging consensus is
// built) held different depths, and so chose different pruning points and finality points from identical blocks.
func TestPruningAndFinalityDoNotDependOnConstructionTime(t *testing.T) {
	defer constants.ForceSetBlockVersion(1)

	config := &Config{Params: dagconfig.MainnetParams}
	config.SkipProofOfWork = true
	// Every block below is version 1.
	config.DisableDifficultyAdjustment = true
	config.POWScores = []uint64{math.MaxUint64}
	// Finality is 10 blocks for versions 1-4 and 20 blocks from version 5, so the two constructions differ.
	targetTime := config.TargetTimePerBlock[0]
	config.TargetTimePerBlock = []time.Duration{targetTime}
	config.FinalityDuration = []time.Duration{10 * targetTime, 10 * targetTime, 10 * targetTime, 10 * targetTime, 20 * targetTime}
	// Keep the pruning depth small so the pruning point moves within a short chain.
	config.K = []externalapi.KType{2}
	config.MergeSetSizeLimit = 2
	config.PruningMultiplier = []uint64{1}

	factory := NewFactory()
	freshNode, teardown, err := factory.NewTestConsensus(config, "TestDepthConstructionTimeFresh")
	if err != nil {
		t.Fatalf("NewTestConsensus: %+v", err)
	}
	defer teardown(false)

	// A node finishing headers-proof IBD builds its consensus after the global was raised to the tip version.
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

	freshPruningPoint, err := freshNode.PruningPoint()
	if err != nil {
		t.Fatalf("PruningPoint: %+v", err)
	}
	if freshPruningPoint.Equal(config.GenesisHash) {
		t.Fatalf("the pruning point never moved in %d blocks, so the test does not exercise the depths", chainLength)
	}
	ibdPruningPoint, err := ibdNode.PruningPoint()
	if err != nil {
		t.Fatalf("PruningPoint: %+v", err)
	}
	if !freshPruningPoint.Equal(ibdPruningPoint) {
		freshInfo, _ := freshNode.GetBlockInfo(freshPruningPoint)
		ibdInfo, _ := ibdNode.GetBlockInfo(ibdPruningPoint)
		t.Errorf("identical blocks gave different pruning points: blue score %d (built at version 1) vs %d (built "+
			"at version 9)", freshInfo.BlueScore, ibdInfo.BlueScore)
	}

	freshFinalityPoint, err := freshNode.FinalityManager().VirtualFinalityPoint(model.NewStagingArea())
	if err != nil {
		t.Fatalf("VirtualFinalityPoint: %+v", err)
	}
	ibdFinalityPoint, err := ibdNode.finalityManager.VirtualFinalityPoint(model.NewStagingArea())
	if err != nil {
		t.Fatalf("VirtualFinalityPoint: %+v", err)
	}
	if !freshFinalityPoint.Equal(ibdFinalityPoint) {
		t.Errorf("identical blocks gave different finality points: %s (built at version 1) vs %s (built at version 9)",
			freshFinalityPoint, ibdFinalityPoint)
	}
}

// TestPruningFollowsTheChainsCurrentBlockVersion pins that the pruning point is selected with the depths of the
// chain's current block version. A node's consensus is built while the process-global version is still 1; the depths
// used to be captured then, so a node on a chain already past the version-5 activation kept selecting with the
// version-1 depths.
func TestPruningFollowsTheChainsCurrentBlockVersion(t *testing.T) {
	defer constants.ForceSetBlockVersion(1)

	config := &Config{Params: dagconfig.MainnetParams}
	config.SkipProofOfWork = true
	config.DisableDifficultyAdjustment = true
	// Versions 2-5 activate at DAA score 1, so every block from DAA score 1 is version 5. (Activation scores start at
	// 1 as on testnet: header validation treats DAA score 0 as version 1 regardless of the activation table.)
	config.POWScores = []uint64{1, 1, 1, 1}
	// Per-version parameter slices keep their full length: several rules index them by block version directly.
	targetTime := config.TargetTimePerBlock[0]
	versions := len(config.TargetTimePerBlock)
	config.TargetTimePerBlock = make([]time.Duration, versions)
	config.FinalityDuration = make([]time.Duration, versions)
	config.K = make([]externalapi.KType, versions)
	config.PruningMultiplier = make([]uint64, versions)
	for i := range versions {
		config.TargetTimePerBlock[i] = targetTime
		// Finality is 10 blocks for versions 1-4 and 20 blocks from version 5.
		config.FinalityDuration[i] = 10 * targetTime
		if i >= 4 {
			config.FinalityDuration[i] = 20 * targetTime
		}
		config.K[i] = 2
		config.PruningMultiplier[i] = 1
	}
	config.MergeSetSizeLimit = 2

	node, teardown, err := NewFactory().NewTestConsensus(config, "TestPruningFollowsCurrentVersion")
	if err != nil {
		t.Fatalf("NewTestConsensus: %+v", err)
	}
	defer teardown(false)

	const chainLength = 200
	tip := config.GenesisHash
	for i := range chainLength {
		tip, _, err = node.AddBlock([]*externalapi.DomainHash{tip}, nil, nil)
		if err != nil {
			t.Fatalf("AddBlock #%d: %+v", i, err)
		}
	}

	pruningPoint, err := node.PruningPoint()
	if err != nil {
		t.Fatalf("PruningPoint: %+v", err)
	}
	if pruningPoint.Equal(config.GenesisHash) {
		t.Fatalf("the pruning point never moved in %d blocks", chainLength)
	}
	pruningPointInfo, err := node.GetBlockInfo(pruningPoint)
	if err != nil {
		t.Fatalf("GetBlockInfo: %+v", err)
	}
	tipInfo, err := node.GetBlockInfo(tip)
	if err != nil {
		t.Fatalf("GetBlockInfo: %+v", err)
	}
	finalityInterval := config.FinalityDepthForBlockVersion(5)
	pruningDepth := config.PruningDepthForBlockVersion(5)
	if pruningPointInfo.BlueScore%finalityInterval != 0 {
		t.Errorf("pruning point blue score %d is not on the version-5 finality interval %d", pruningPointInfo.BlueScore,
			finalityInterval)
	}
	if tipInfo.BlueScore-pruningPointInfo.BlueScore < pruningDepth {
		t.Errorf("pruning point blue score %d is less than the version-5 pruning depth %d below the tip (%d)",
			pruningPointInfo.BlueScore, pruningDepth, tipInfo.BlueScore)
	}
}
