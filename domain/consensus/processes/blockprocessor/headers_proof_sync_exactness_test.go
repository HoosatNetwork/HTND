package blockprocessor_test

import (
	"fmt"
	"math"
	"math/big"
	"math/rand"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/testapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/blockheader"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/testutils"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
)

// HTN-196: a node that syncs through the pruning point proof rebuilds GHOSTDAG for every header above
// the imported pruning point. That rebuild used to disagree with the syncer's own GHOSTDAG, because
// header-only proof blocks were treated as pruned (the imported pruning point lost its selected
// parent and merge set) and because the pruning point anticone could arrive out of topological
// order. The syncee then colored some blocks blue that the network colored red, its blue work above
// the pruning point came out higher, selected parents flipped, and sometimes the tip's selected chain
// missed the pruning point entirely ("only share virtual genesis"). On mainnet the same thing showed
// up as a group of miners whose headers claimed blue scores exactly 5 higher than everyone else's.

// headersProofTestConfig is mainnet with every block past genesis at version 10 - dynamic K and the
// enlarged anticone bound, as mainnet runs today - and depths small enough to prune quickly.
func headersProofTestConfig(cfg *consensus.Config) {
	const finalityDepth = 15
	n := len(cfg.TargetTimePerBlock)
	cfg.FinalityDuration = make([]time.Duration, n)
	cfg.K = make([]externalapi.KType, n)
	for i := 0; i < n; i++ {
		cfg.FinalityDuration[i] = time.Duration(finalityDepth) * cfg.TargetTimePerBlock[i]
		cfg.K[i] = 6
	}
	cfg.MergeSetSizeLimit = 10
	cfg.POWScores = make([]uint64, len(cfg.POWScores))
	for i := range cfg.POWScores {
		cfg.POWScores[i] = 1
	}
}

// buildWideDAG mines rounds of up to width parallel blocks on random subsets of the tips until the
// pruning point has moved for 40 rounds. rewriteHeader, when set, may replace each block's header
// before it is inserted.
func buildWideDAG(t *testing.T, tc testapi.TestConsensus, genesis *externalapi.DomainHash, seed int64, width int,
	rewriteHeader func(round int, header externalapi.BlockHeader) externalapi.BlockHeader,
) {
	rng := rand.New(rand.NewSource(seed))
	movedRounds := 0
	for round := 0; movedRounds < 40; round++ {
		if round > 3000 {
			t.Fatalf("seed %d: the pruning point never moved", seed)
		}
		tips, err := tc.Tips()
		if err != nil {
			t.Fatalf("Tips: %+v", err)
		}
		blocks := 1 + rng.Intn(width)
		for i := 0; i < blocks; i++ {
			perm := rng.Perm(len(tips))
			parents := make([]*externalapi.DomainHash, 0, width)
			for _, index := range perm[:1+rng.Intn(min(width, len(tips)))] {
				parents = append(parents, tips[index])
			}
			block, _, err := tc.BuildBlockWithParents(parents, nil, nil)
			if err != nil {
				t.Fatalf("BuildBlockWithParents: %+v", err)
			}
			if rewriteHeader != nil {
				block.Header = rewriteHeader(round, block.Header)
			}
			err = tc.ValidateAndInsertBlock(block, true, true)
			if err != nil {
				t.Fatalf("ValidateAndInsertBlock: %+v", err)
			}
		}
		err = tc.ResolveVirtual(nil)
		if err != nil {
			t.Fatalf("ResolveVirtual: %+v", err)
		}
		pruningPoint, err := tc.PruningPoint()
		if err != nil {
			t.Fatalf("PruningPoint: %+v", err)
		}
		if !pruningPoint.Equal(genesis) {
			movedRounds++
		}
	}
}

func trustedBlock(t *testing.T, syncer testapi.TestConsensus, blockHash *externalapi.DomainHash) *externalapi.BlockWithTrustedData {
	block, _, err := syncer.GetBlock(blockHash)
	if err != nil {
		t.Fatalf("GetBlock: %+v", err)
	}
	daaWindowHashes, err := syncer.BlockDAAWindowHashes(blockHash)
	if err != nil {
		t.Fatalf("BlockDAAWindowHashes: %+v", err)
	}
	ghostdagDataHashes, err := syncer.TrustedBlockAssociatedGHOSTDAGDataBlockHashes(blockHash)
	if err != nil {
		t.Fatalf("TrustedBlockAssociatedGHOSTDAGDataBlockHashes: %+v", err)
	}
	blockWithTrustedData := &externalapi.BlockWithTrustedData{Block: block}
	for i, daaBlockHash := range daaWindowHashes {
		header, err := syncer.TrustedDataDataDAAHeader(blockHash, daaBlockHash, uint64(uint(i)))
		if err != nil {
			t.Fatalf("TrustedDataDataDAAHeader: %+v", err)
		}
		blockWithTrustedData.DAAWindow = append(blockWithTrustedData.DAAWindow, header)
	}
	for _, ghostdagDataHash := range ghostdagDataHashes {
		data, err := syncer.TrustedGHOSTDAGData(ghostdagDataHash)
		if err != nil {
			t.Fatalf("TrustedGHOSTDAGData: %+v", err)
		}
		blockWithTrustedData.GHOSTDAGData = append(blockWithTrustedData.GHOSTDAGData,
			&externalapi.BlockGHOSTDAGDataHashPair{Hash: ghostdagDataHash, GHOSTDAGData: data})
	}
	return blockWithTrustedData
}

// syncThroughHeadersProof does what a headers-proof IBD does up to the UTXO set: apply the proof,
// import the pruning points, insert the pruning point and its anticone in the order the syncer
// serves them, then insert every header from the pruning point to the syncer's headers selected tip.
func syncThroughHeadersProof(t *testing.T, factory consensus.Factory, cfg *consensus.Config, syncer testapi.TestConsensus,
	name string,
) (testapi.TestConsensus, func(bool)) {
	proof, err := syncer.BuildPruningPointProof()
	if err != nil {
		t.Fatalf("BuildPruningPointProof: %+v", err)
	}
	stagingConfig := *cfg
	stagingConfig.SkipAddingGenesis = true
	syncee, teardown, err := factory.NewTestConsensus(&stagingConfig, name)
	if err != nil {
		t.Fatalf("NewTestConsensus: %+v", err)
	}
	constants.ForceSetBlockVersion(10)

	err = syncee.ApplyPruningPointProof(proof)
	if err != nil {
		t.Fatalf("ApplyPruningPointProof: %+v", err)
	}
	pruningPointHeaders, err := syncer.PruningPointHeaders()
	if err != nil {
		t.Fatalf("PruningPointHeaders: %+v", err)
	}
	err = syncee.ImportPruningPoints(pruningPointHeaders)
	if err != nil {
		t.Fatalf("ImportPruningPoints: %+v", err)
	}
	pruningPointAndItsAnticone, err := syncer.PruningPointAndItsAnticone()
	if err != nil {
		t.Fatalf("PruningPointAndItsAnticone: %+v", err)
	}
	for _, blockHash := range pruningPointAndItsAnticone {
		err = syncee.ValidateAndInsertBlockWithTrustedData(trustedBlock(t, syncer, blockHash), false)
		if err != nil {
			t.Fatalf("ValidateAndInsertBlockWithTrustedData: %+v", err)
		}
	}

	pruningPoint, err := syncer.PruningPoint()
	if err != nil {
		t.Fatalf("PruningPoint: %+v", err)
	}
	headersSelectedTip, err := syncer.GetHeadersSelectedTip()
	if err != nil {
		t.Fatalf("GetHeadersSelectedTip: %+v", err)
	}
	hashes, _, err := syncer.GetHashesBetween(pruningPoint, headersSelectedTip, math.MaxUint64, false)
	if err != nil {
		t.Fatalf("GetHashesBetween: %+v", err)
	}
	for _, blockHash := range hashes {
		info, err := syncee.GetBlockInfo(blockHash)
		if err != nil {
			t.Fatalf("GetBlockInfo: %+v", err)
		}
		if info.Exists {
			continue
		}
		header, err := syncer.GetBlockHeader(blockHash)
		if err != nil {
			t.Fatalf("GetBlockHeader: %+v", err)
		}
		err = syncee.ValidateAndInsertBlock(&externalapi.DomainBlock{Header: header}, false, true)
		if err != nil {
			t.Fatalf("ValidateAndInsertBlock header %s: %+v", blockHash, err)
		}
	}
	return syncee, teardown
}

// assertSynceeMatchesSyncer checks that the syncee colored every header above the pruning point
// exactly as the syncer did, and that the pruning point is on the syncer tip's selected chain.
func assertSynceeMatchesSyncer(t *testing.T, seed int64, syncer, syncee testapi.TestConsensus) {
	pruningPoint, err := syncer.PruningPoint()
	if err != nil {
		t.Fatalf("PruningPoint: %+v", err)
	}
	headersSelectedTip, err := syncer.GetHeadersSelectedTip()
	if err != nil {
		t.Fatalf("GetHeadersSelectedTip: %+v", err)
	}
	pruningPointAndItsAnticone, err := syncer.PruningPointAndItsAnticone()
	if err != nil {
		t.Fatalf("PruningPointAndItsAnticone: %+v", err)
	}
	trusted := make(map[externalapi.DomainHash]struct{}, len(pruningPointAndItsAnticone))
	for _, blockHash := range pruningPointAndItsAnticone {
		trusted[*blockHash] = struct{}{}
	}
	hashes, _, err := syncer.GetHashesBetween(pruningPoint, headersSelectedTip, math.MaxUint64, false)
	if err != nil {
		t.Fatalf("GetHashesBetween: %+v", err)
	}

	syncerStagingArea, synceeStagingArea := model.NewStagingArea(), model.NewStagingArea()
	mismatches := 0
	for _, blockHash := range hashes {
		if _, isTrusted := trusted[*blockHash]; isTrusted {
			continue
		}
		expected, err := syncer.GHOSTDAGDataStore().Get(syncer.DatabaseContext(), syncerStagingArea, blockHash, false)
		if err != nil {
			t.Fatalf("syncer GHOSTDAG data of %s: %+v", blockHash, err)
		}
		actual, err := syncee.GHOSTDAGDataStore().Get(syncee.DatabaseContext(), synceeStagingArea, blockHash, false)
		if err != nil {
			t.Fatalf("syncee GHOSTDAG data of %s: %+v", blockHash, err)
		}
		if expected.BlueWork().Cmp(actual.BlueWork()) == 0 && expected.BlueScore() == actual.BlueScore() &&
			expected.SelectedParent().Equal(actual.SelectedParent()) {
			continue
		}
		mismatches++
		if mismatches == 1 {
			t.Errorf("seed %d: the syncee colored %s differently from the syncer: syncer blue score %d with %d "+
				"blues and %d reds, syncee blue score %d with %d blues and %d reds (selected parent %s vs %s)",
				seed, blockHash, expected.BlueScore(), len(expected.MergeSetBlues()), len(expected.MergeSetReds()),
				actual.BlueScore(), len(actual.MergeSetBlues()), len(actual.MergeSetReds()),
				expected.SelectedParent(), actual.SelectedParent())
		}
	}
	if mismatches > 0 {
		t.Errorf("seed %d: %d of %d headers above the pruning point have different GHOSTDAG data on the syncee",
			seed, mismatches, len(hashes))
	}

	isOnChain, err := syncee.IsInSelectedParentChainOf(pruningPoint, headersSelectedTip)
	if err != nil {
		t.Fatalf("IsInSelectedParentChainOf: %+v", err)
	}
	if !isOnChain {
		t.Errorf("seed %d: on the syncee the pruning point %s is not on the selected parent chain of the syncer's "+
			"headers selected tip %s", seed, pruningPoint, headersSelectedTip)
	}
}

// TestHeadersProofSyncReproducesSyncerGHOSTDAG pins the first half of HTN-196: with the proof headers
// no longer counted as pruned, the syncee's GHOSTDAG above the imported pruning point equals the
// syncer's. Each seed here produced a different coloring before the fix.
func TestHeadersProofSyncReproducesSyncerGHOSTDAG(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, cfg *consensus.Config) {
		if cfg.Name != dagconfig.MainnetParams.Name {
			return
		}
		headersProofTestConfig(cfg)
		factory := consensus.NewFactory()
		for _, seed := range []int64{2, 3, 4, 6} {
			syncer, teardownSyncer, err := factory.NewTestConsensus(cfg, fmt.Sprintf("HeadersProofExactSyncer%d", seed))
			if err != nil {
				t.Fatalf("NewTestConsensus: %+v", err)
			}
			constants.ForceSetBlockVersion(10)
			buildWideDAG(t, syncer, cfg.GenesisHash, seed, 5, nil)
			syncee, teardownSyncee := syncThroughHeadersProof(t, factory, cfg, syncer, fmt.Sprintf("HeadersProofExactSyncee%d", seed))
			assertSynceeMatchesSyncer(t, seed, syncer, syncee)
			teardownSyncee(false)
			teardownSyncer(false)
		}
	})
}

// TestPruningPointAnticoneIsServedInTopologicalOrder pins the second half: the syncer orders the
// pruning point anticone by its own GHOSTDAG, not by the blue work written in each header. Here every
// header claims less blue work than the headers mined before it, the way a miner with a shifted view
// can claim anything on a network that does not validate the claim, so ordering by the claim puts
// children before their parents. The syncee inserts the anticone in the served order and must still
// end up with the syncer's coloring.
func TestPruningPointAnticoneIsServedInTopologicalOrder(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, cfg *consensus.Config) {
		if cfg.Name != dagconfig.MainnetParams.Name {
			return
		}
		headersProofTestConfig(cfg)
		factory := consensus.NewFactory()
		decreasingClaim := func(round int, header externalapi.BlockHeader) externalapi.BlockHeader {
			claim := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 64), big.NewInt(int64(round)))
			return blockheader.NewImmutableBlockHeader(header.Version(), header.Parents(), header.HashMerkleRoot(),
				header.AcceptedIDMerkleRoot(), header.UTXOCommitment(), header.TimeInMilliseconds(), header.Bits(),
				header.Nonce(), header.DAAScore(), header.BlueScore(), claim, header.PruningPoint())
		}

		checkedPairs := 0
		for _, seed := range []int64{2, 3, 4, 6, 8} {
			syncer, teardownSyncer, err := factory.NewTestConsensus(cfg, fmt.Sprintf("AnticoneOrderSyncer%d", seed))
			if err != nil {
				t.Fatalf("NewTestConsensus: %+v", err)
			}
			constants.ForceSetBlockVersion(10)
			buildWideDAG(t, syncer, cfg.GenesisHash, seed, 5, decreasingClaim)

			pruningPointAndItsAnticone, err := syncer.PruningPointAndItsAnticone()
			if err != nil {
				t.Fatalf("PruningPointAndItsAnticone: %+v", err)
			}
			position := make(map[externalapi.DomainHash]int, len(pruningPointAndItsAnticone))
			for i, blockHash := range pruningPointAndItsAnticone {
				position[*blockHash] = i
			}
			for i, blockHash := range pruningPointAndItsAnticone {
				header, err := syncer.GetBlockHeader(blockHash)
				if err != nil {
					t.Fatalf("GetBlockHeader: %+v", err)
				}
				for _, parent := range header.DirectParents() {
					parentPosition, isInSet := position[*parent]
					if !isInSet {
						continue
					}
					checkedPairs++
					if parentPosition > i {
						t.Errorf("seed %d: the syncer serves %s before its parent %s", seed,
							consensushashing.BlockHash(&externalapi.DomainBlock{Header: header}), parent)
					}
				}
			}

			syncee, teardownSyncee := syncThroughHeadersProof(t, factory, cfg, syncer, fmt.Sprintf("AnticoneOrderSyncee%d", seed))
			assertSynceeMatchesSyncer(t, seed, syncer, syncee)
			teardownSyncee(false)
			teardownSyncer(false)
		}
		if checkedPairs == 0 {
			t.Fatalf("no seed put a parent and its child in the pruning point anticone, so the order was never tested")
		}
	})
}
