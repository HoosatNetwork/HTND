package blockprocessor_test

import (
	"math"
	"sort"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/testapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/testutils"
)

// HTN-204 end to end.
//
// The unit tests in dagtraversalmanager stage a pruned block's shape by hand. This does a real
// headers-proof sync - pruning point proof, trusted data for the pruning point and its anticone,
// headers, the pruning point UTXO set, then bodies - and asks the question the operator actually
// sees: does the synced node compute the same difficulty as the node it synced from?
//
// It exists because the earlier "TestIBDWithPruning passes 3/3" check could not answer that. That
// test runs on simnet, which sets DisableDifficultyAdjustment, so it never computes a difficulty at
// all. It proves the serving path still works; it says nothing about the difficulty window.
//
// The configuration is chosen so the question is not vacuous: the difficulty window is SMALLER than
// the syncer's history, so the syncer's window fills from real blocks, and LARGER than the blocks
// the syncee has above its pruning point, so the syncee can only fill it from the trusted window.

const htn204WindowSize = 20

func htn204Config(consensusConfig *consensus.Config) {
	consensusConfig.DisableDifficultyAdjustment = false
	consensusConfig.POWScores = []uint64{math.MaxUint64} // every block is version 1
	consensusConfig.DifficultyAdjustmentWindowSize = []int{htn204WindowSize}
	finalityDepth := 5
	consensusConfig.FinalityDuration = []time.Duration{time.Duration(finalityDepth) * consensusConfig.TargetTimePerBlock[0]}
	consensusConfig.K[0] = 0
	consensusConfig.PruningProofM = 1
}

// headersProofSync syncs a brand-new consensus from syncer the way IBD with a headers proof does,
// and returns it. It mirrors TestValidateAndInsertImportedPruningPoint's syncConsensuses, reduced
// to the steps that matter for the DAA window.
func headersProofSync(t *testing.T, factory consensus.Factory, consensusConfig *consensus.Config,
	syncer testapi.TestConsensus, name string,
) (testapi.TestConsensus, func(bool)) {
	t.Helper()

	pruningPointProof, err := syncer.BuildPruningPointProof()
	if err != nil {
		t.Fatalf("%s: BuildPruningPointProof: %+v", name, err)
	}

	stagingConfig := *consensusConfig
	stagingConfig.SkipAddingGenesis = true
	syncee, teardown, err := factory.NewTestConsensus(&stagingConfig, name)
	if err != nil {
		t.Fatalf("%s: NewTestConsensus: %+v", name, err)
	}

	if err := syncee.ApplyPruningPointProof(pruningPointProof); err != nil {
		t.Fatalf("%s: ApplyPruningPointProof: %+v", name, err)
	}

	pruningPointHeaders, err := syncer.PruningPointHeaders()
	if err != nil {
		t.Fatalf("%s: PruningPointHeaders: %+v", name, err)
	}
	if err := syncee.ImportPruningPoints(pruningPointHeaders); err != nil {
		t.Fatalf("%s: ImportPruningPoints: %+v", name, err)
	}

	pruningPointAndItsAnticone, err := syncer.PruningPointAndItsAnticone()
	if err != nil {
		t.Fatalf("%s: PruningPointAndItsAnticone: %+v", name, err)
	}
	sort.Slice(pruningPointAndItsAnticone, func(i, j int) bool {
		iHeader, _ := syncer.GetBlockHeader(pruningPointAndItsAnticone[i])
		jHeader, _ := syncer.GetBlockHeader(pruningPointAndItsAnticone[j])
		return iHeader.BlueScore() < jHeader.BlueScore()
	})

	for _, blockHash := range pruningPointAndItsAnticone {
		block, _, err := syncer.GetBlock(blockHash)
		if err != nil {
			t.Fatalf("%s: GetBlock: %+v", name, err)
		}
		// This is exactly what the serving node hands over: its OWN DAA window for the block. If
		// the serving node's window is truncated, this is truncated too, and the syncee inherits it.
		blockDAAWindowHashes, err := syncer.BlockDAAWindowHashes(blockHash)
		if err != nil {
			t.Fatalf("%s: BlockDAAWindowHashes: %+v", name, err)
		}
		ghostdagDataBlockHashes, err := syncer.TrustedBlockAssociatedGHOSTDAGDataBlockHashes(blockHash)
		if err != nil {
			t.Fatalf("%s: TrustedBlockAssociatedGHOSTDAGDataBlockHashes: %+v", name, err)
		}

		withTrustedData := &externalapi.BlockWithTrustedData{Block: block}
		for i, daaBlockHash := range blockDAAWindowHashes {
			header, err := syncer.TrustedDataDataDAAHeader(blockHash, daaBlockHash, uint64(i))
			if err != nil {
				t.Fatalf("%s: TrustedDataDataDAAHeader: %+v", name, err)
			}
			withTrustedData.DAAWindow = append(withTrustedData.DAAWindow, header)
		}
		for _, ghostdagDataBlockHash := range ghostdagDataBlockHashes {
			data, err := syncer.TrustedGHOSTDAGData(ghostdagDataBlockHash)
			if err != nil {
				t.Fatalf("%s: TrustedGHOSTDAGData: %+v", name, err)
			}
			withTrustedData.GHOSTDAGData = append(withTrustedData.GHOSTDAGData,
				&externalapi.BlockGHOSTDAGDataHashPair{Hash: ghostdagDataBlockHash, GHOSTDAGData: data})
		}

		if err := syncee.ValidateAndInsertBlockWithTrustedData(withTrustedData, false); err != nil {
			t.Fatalf("%s: ValidateAndInsertBlockWithTrustedData: %+v", name, err)
		}
	}

	syncerVirtualSelectedParent, err := syncer.GetVirtualSelectedParent()
	if err != nil {
		t.Fatalf("%s: GetVirtualSelectedParent: %+v", name, err)
	}
	pruningPoint, err := syncer.PruningPoint()
	if err != nil {
		t.Fatalf("%s: PruningPoint: %+v", name, err)
	}
	missingHeaderHashes, _, err := syncer.GetHashesBetween(pruningPoint, syncerVirtualSelectedParent, math.MaxUint64, false)
	if err != nil {
		t.Fatalf("%s: GetHashesBetween: %+v", name, err)
	}
	for _, blockHash := range missingHeaderHashes {
		info, err := syncee.GetBlockInfo(blockHash)
		if err != nil {
			t.Fatalf("%s: GetBlockInfo: %+v", name, err)
		}
		if info.Exists {
			continue
		}
		header, err := syncer.GetBlockHeader(blockHash)
		if err != nil {
			t.Fatalf("%s: GetBlockHeader: %+v", name, err)
		}
		if err := syncee.ValidateAndInsertBlock(&externalapi.DomainBlock{Header: header}, false, false); err != nil {
			t.Fatalf("%s: ValidateAndInsertBlock header: %+v", name, err)
		}
	}

	var fromOutpoint *externalapi.DomainOutpoint
	var pruningPointUTXOs []*externalapi.OutpointAndUTXOEntryPair
	for {
		pairs, err := syncer.GetPruningPointUTXOs(pruningPoint, fromOutpoint, 100_000)
		if err != nil {
			t.Fatalf("%s: GetPruningPointUTXOs: %+v", name, err)
		}
		originalLen := len(pairs)
		if fromOutpoint != nil && originalLen > 0 && pairs[0].Outpoint.Equal(fromOutpoint) {
			pairs = pairs[1:]
		}
		if len(pairs) == 0 {
			break
		}
		fromOutpoint = pairs[len(pairs)-1].Outpoint
		pruningPointUTXOs = append(pruningPointUTXOs, pairs...)
		if originalLen < 100_000 {
			break
		}
	}
	if err := syncee.AppendImportedPruningPointUTXOs(pruningPointUTXOs); err != nil {
		t.Fatalf("%s: AppendImportedPruningPointUTXOs: %+v", name, err)
	}
	if err := syncee.ValidateAndInsertImportedPruningPoint(pruningPoint); err != nil {
		t.Fatalf("%s: ValidateAndInsertImportedPruningPoint: %+v", name, err)
	}

	headersSelectedTip, err := syncee.GetHeadersSelectedTip()
	if err != nil {
		t.Fatalf("%s: GetHeadersSelectedTip: %+v", name, err)
	}
	missingBodies, err := syncee.GetMissingBlockBodyHashes(headersSelectedTip)
	if err != nil {
		t.Fatalf("%s: GetMissingBlockBodyHashes: %+v", name, err)
	}
	for _, blockHash := range missingBodies {
		block, _, err := syncer.GetBlock(blockHash)
		if err != nil {
			t.Fatalf("%s: GetBlock body: %+v", name, err)
		}
		if err := syncee.ValidateAndInsertBlock(block, true, false); err != nil {
			t.Fatalf("%s: ValidateAndInsertBlock body: %+v", name, err)
		}
	}

	return syncee, teardown
}

// difficultyReport is what one node thinks about a block: how many blocks its difficulty window
// actually holds, and the difficulty it computes from them.
type difficultyReport struct {
	windowLength int
	bits         uint32
}

func reportFor(t *testing.T, tc testapi.TestConsensus, blockHash *externalapi.DomainHash) difficultyReport {
	t.Helper()
	stagingArea := model.NewStagingArea()
	window, err := tc.DAGTraversalManager().BlockWindowHeapSlice(stagingArea, blockHash, htn204WindowSize, true)
	if err != nil {
		t.Fatalf("BlockWindowHeapSlice: %+v", err)
	}
	bits, err := tc.DifficultyManager().RequiredDifficulty(stagingArea, blockHash)
	if err != nil {
		t.Fatalf("RequiredDifficulty: %+v", err)
	}
	return difficultyReport{windowLength: len(window), bits: bits}
}

// buildSyncerChain mines a chain long enough that the pruning point has moved and the syncer's
// difficulty window is full of real history.
func buildSyncerChain(t *testing.T, syncer testapi.TestConsensus, genesis *externalapi.DomainHash) {
	t.Helper()
	tip := genesis
	for range 3 * htn204WindowSize {
		tip = addBlock(syncer, []*externalapi.DomainHash{tip}, t)
	}
}

// TestHeadersProofNodeComputesTheSameDifficultyAsItsSyncer is the plan's required test for HTN-204:
// a node synced from a headers proof must compute the same difficulty as the node it synced from.
func TestHeadersProofNodeComputesTheSameDifficultyAsItsSyncer(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		htn204Config(consensusConfig)
		factory := consensus.NewFactory()

		syncer, teardownSyncer, err := factory.NewTestConsensus(consensusConfig, "HTN204OneHopSyncer")
		if err != nil {
			t.Fatalf("NewTestConsensus: %+v", err)
		}
		defer teardownSyncer(false)
		buildSyncerChain(t, syncer, consensusConfig.GenesisHash)

		syncee, teardownSyncee := headersProofSync(t, factory, consensusConfig, syncer, "HTN204OneHopSyncee")
		defer teardownSyncee(false)

		assertSameDifficulty(t, "one hop", syncer, syncee, consensusConfig)
	})
}

// TestTwoHopHeadersProofNodeComputesTheSameDifficulty is the hypothesis for why the fix still did not
// work on a live node. On a real network most peers were themselves headers-proof synced. If a
// headers-proof node serves a TRUNCATED trusted window for its pruning point, every node syncing from
// it inherits the truncation no matter how correctly it reads the trusted window.
func TestTwoHopHeadersProofNodeComputesTheSameDifficulty(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		htn204Config(consensusConfig)
		factory := consensus.NewFactory()

		origin, teardownOrigin, err := factory.NewTestConsensus(consensusConfig, "HTN204TwoHopOrigin")
		if err != nil {
			t.Fatalf("NewTestConsensus: %+v", err)
		}
		defer teardownOrigin(false)
		buildSyncerChain(t, origin, consensusConfig.GenesisHash)

		middle, teardownMiddle := headersProofSync(t, factory, consensusConfig, origin, "HTN204TwoHopMiddle")
		defer teardownMiddle(false)

		last, teardownLast := headersProofSync(t, factory, consensusConfig, middle, "HTN204TwoHopLast")
		defer teardownLast(false)

		assertSameDifficulty(t, "origin vs middle", origin, middle, consensusConfig)
		assertSameDifficulty(t, "origin vs last (two hops)", origin, last, consensusConfig)
	})
}

// TestThreeHopHeadersProofNodeComputesTheSameDifficulty makes sure two hops was not a coincidence of
// depth. If a served window were losing a little on every hop, two hops could still fit inside the
// window while three would not.
func TestThreeHopHeadersProofNodeComputesTheSameDifficulty(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		htn204Config(consensusConfig)
		factory := consensus.NewFactory()

		origin, teardownOrigin, err := factory.NewTestConsensus(consensusConfig, "HTN204ThreeHopOrigin")
		if err != nil {
			t.Fatalf("NewTestConsensus: %+v", err)
		}
		defer teardownOrigin(false)
		buildSyncerChain(t, origin, consensusConfig.GenesisHash)

		previous := origin
		for hop := 1; hop <= 3; hop++ {
			next, teardown := headersProofSync(t, factory, consensusConfig, previous,
				"HTN204ThreeHop"+string(rune('0'+hop)))
			defer teardown(false)
			assertSameDifficulty(t, "origin vs hop "+string(rune('0'+hop)), origin, next, consensusConfig)
			previous = next
		}
	})
}

// TestServingStillWorksAfterThePruningPointAdvances is the live-node case: a node syncs by headers
// proof, keeps running, and its pruning point moves on past the one it synced to. It is then asked to
// serve a new node.
//
// This is the case the served window is most likely to get wrong. The new pruning point was validated
// normally rather than inserted with trusted data, so it has no trusted window of its own - but if it
// is less than a full window above the OLD trusted pruning point, its window still reaches into the
// old one's trusted data, which is stored under the old pruning point's hash, not the new one's.
func TestServingStillWorksAfterThePruningPointAdvances(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		htn204Config(consensusConfig)
		factory := consensus.NewFactory()

		origin, teardownOrigin, err := factory.NewTestConsensus(consensusConfig, "HTN204AdvanceOrigin")
		if err != nil {
			t.Fatalf("NewTestConsensus: %+v", err)
		}
		defer teardownOrigin(false)
		buildSyncerChain(t, origin, consensusConfig.GenesisHash)

		synced, teardownSynced := headersProofSync(t, factory, consensusConfig, origin, "HTN204AdvanceSynced")
		defer teardownSynced(false)

		pruningPointBefore, err := synced.PruningPoint()
		if err != nil {
			t.Fatalf("PruningPoint: %+v", err)
		}

		// Keep mining on origin and relaying to synced, so synced's pruning point moves on. Only a
		// few blocks - fewer than a full window - so the new pruning point's window still reaches
		// back into the old one's trusted data.
		tips, err := origin.Tips()
		if err != nil {
			t.Fatalf("Tips: %+v", err)
		}
		for range htn204WindowSize / 2 {
			tip := addBlock(origin, tips, t)
			tips = []*externalapi.DomainHash{tip}
			block, _, err := origin.GetBlock(tip)
			if err != nil {
				t.Fatalf("GetBlock: %+v", err)
			}
			if err := synced.ValidateAndInsertBlock(block, true, false); err != nil {
				t.Fatalf("relaying a block to the synced node: %+v", err)
			}
		}

		pruningPointAfter, err := synced.PruningPoint()
		if err != nil {
			t.Fatalf("PruningPoint: %+v", err)
		}
		if pruningPointAfter.Equal(pruningPointBefore) {
			t.Fatalf("the synced node's pruning point did not advance, so this test does not exercise " +
				"the case it exists for - relay more blocks")
		}

		assertSameDifficulty(t, "origin vs synced, after its pruning point advanced", origin, synced, consensusConfig)

		// And the synced node must still be able to SERVE a new node correctly.
		next, teardownNext := headersProofSync(t, factory, consensusConfig, synced, "HTN204AdvanceNext")
		defer teardownNext(false)
		assertSameDifficulty(t, "origin vs a node synced from it after the advance", origin, next, consensusConfig)
	})
}

func assertSameDifficulty(t *testing.T, label string, reference, synced testapi.TestConsensus,
	consensusConfig *consensus.Config,
) {
	t.Helper()

	tip, err := reference.GetVirtualSelectedParent()
	if err != nil {
		t.Fatalf("%s: GetVirtualSelectedParent: %+v", label, err)
	}

	want := reportFor(t, reference, tip)
	got := reportFor(t, synced, tip)

	// Guard against a vacuous pass: if the reference itself cannot fill its window, both nodes return
	// genesis bits and agree for the wrong reason.
	if want.windowLength < htn204WindowSize {
		t.Fatalf("%s: the reference node's window holds only %d of %d blocks, so this comparison "+
			"cannot distinguish a fixed node from a broken one - the test chain is too short",
			label, want.windowLength, htn204WindowSize)
	}

	t.Logf("%s: reference window %d bits %08x | synced window %d bits %08x | genesis bits %08x",
		label, want.windowLength, want.bits, got.windowLength, got.bits,
		consensusConfig.GenesisBlock.Header.Bits())

	if got.windowLength != want.windowLength {
		t.Errorf("%s: the synced node's difficulty window holds %d blocks, the reference's holds %d. "+
			"A short window is HTN-204.", label, got.windowLength, want.windowLength)
	}
	if got.bits != want.bits {
		t.Errorf("%s: the synced node computes bits %08x, the reference computes %08x - they would "+
			"mine at different difficulties.", label, got.bits, want.bits)
	}
}
