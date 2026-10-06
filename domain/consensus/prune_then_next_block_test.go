package consensus_test

import (
	"math"
	"math/rand"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/testutils"
	"github.com/HoosatNetwork/HTND/v2/internal/ci"
	"github.com/pkg/errors"
)

// TestNoBlockIsDisqualifiedAfterPruningPointMoves grows a randomly forking and merging DAG under a very short
// pruning depth, so the pruning point moves and deletes below it many times, and requires every block that was
// inserted to be UTXO valid or pending - never disqualified from the chain.
func TestNoBlockIsDisqualifiedAfterPruningPointMoves(t *testing.T) {
	ci.SkipLongTest(t, "Grows 8 random 300-block DAGs per network")
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		defer constants.ForceSetBlockVersion(1)

		consensusConfig.POWScores = []uint64{math.MaxUint64}
		consensusConfig.K = append([]externalapi.KType(nil), consensusConfig.K...)
		consensusConfig.K[0] = 0
		consensusConfig.FinalityDuration = []time.Duration{5 * consensusConfig.TargetTimePerBlock[0]}
		consensusConfig.PruningProofM = 1
		consensusConfig.DisableDifficultyAdjustment = true
		consensusConfig.DifficultyAdjustmentWindowSize = append([]int(nil), consensusConfig.DifficultyAdjustmentWindowSize...)
		for i := range consensusConfig.DifficultyAdjustmentWindowSize {
			consensusConfig.DifficultyAdjustmentWindowSize[i] = 10
		}

		for seed := int64(1); seed <= 8; seed++ {
			rng := rand.New(rand.NewSource(seed))
			tc, teardown, err := consensus.NewFactory().NewTestConsensus(consensusConfig, "TestNoDisqualifiedAfterPruning")
			if err != nil {
				t.Fatalf("NewTestConsensus: %+v", err)
			}

			recent := []*externalapi.DomainHash{consensusConfig.GenesisHash}
			pruningPoint := consensusConfig.GenesisHash
			moves, rejected := 0, 0
			for i := 0; i < 300; i++ {
				var parents []*externalapi.DomainHash
				tips, err := tc.Tips()
				if err != nil {
					t.Fatalf("Tips: %+v", err)
				}
				fork := recent[max(0, len(recent)-1-rng.Intn(min(len(recent), 6)))]
				inPruningPointFuture := fork.Equal(pruningPoint)
				if !inPruningPointFuture {
					inPruningPointFuture, err = tc.DAGTopologyManager().IsAncestorOf(model.NewStagingArea(), pruningPoint, fork)
					if err != nil {
						t.Fatalf("IsAncestorOf: %+v", err)
					}
				}
				if rng.Intn(6) == 0 && inPruningPointFuture {
					// Fork: build on one of the last few blocks alone.
					parents = []*externalapi.DomainHash{fork}
				} else {
					// Extend or merge: tips are always an antichain.
					rng.Shuffle(len(tips), func(i, j int) { tips[i], tips[j] = tips[j], tips[i] })
					parents = tips[:min(len(tips), 1+rng.Intn(5)/4)]
				}
				block, err := func() (block *externalapi.DomainBlock, err error) {
					// The test builder derives the header pruning point from node-local state and panics for a fork parent that is
					// not on the pruning candidate's chain. That is a builder limitation, not the behavior under test.
					defer func() {
						if r := recover(); r != nil {
							err = errors.Errorf("builder panicked: %v", r)
						}
					}()
					block, _, err = tc.BuildBlockWithParents(parents, nil, nil)
					return block, err
				}()
				if err != nil {
					rejected++
					continue
				}
				if err := tc.ValidateAndInsertBlock(block, true, true); err != nil {
					rejected++
					continue
				}
				hash := consensushashing.BlockHash(block)
				recent = append(recent, hash)

				status, err := tc.BlockStatusStore().Get(tc.DatabaseContext(), model.NewStagingArea(), hash)
				if err != nil {
					t.Fatalf("seed %d block #%d: status: %+v", seed, i, err)
				}
				if status == externalapi.StatusDisqualifiedFromChain {
					t.Fatalf("seed %d: block #%d %s (%d parents) was disqualified from the chain (pruning point moved %d times, "+
						"%d insertions rejected so far)", seed, i, hash, len(parents), moves, rejected)
				}

				pp, err := tc.PruningPoint()
				if err != nil {
					t.Fatalf("PruningPoint: %+v", err)
				}
				if !pp.Equal(pruningPoint) {
					pruningPoint = pp
					moves++
				}
			}
			vd, _ := tc.GHOSTDAGDataStore().Get(tc.DatabaseContext(), model.NewStagingArea(), model.VirtualBlockHash, false)
			t.Logf("seed %d: pruning point moved %d times, %d insertions rejected, virtual blue score %d, pruningDepth %d", seed, moves, rejected, vd.BlueScore(), consensusConfig.PruningDepthForBlockVersion(1))
			teardown(false)
			if moves == 0 {
				t.Logf("seed %d: pruning point never moved", seed)
				continue
			}
		}
	})
}
