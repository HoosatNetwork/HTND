package consensus_test

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/testapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/ruleerrors"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/testutils"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/utxo"
	"github.com/HoosatNetwork/HTND/v2/util/staging"
)

// TestCheckUTXOHealthJudgesTheServedSet pins what gates serving the pruning point UTXO set: the
// multiset of the bucket GetPruningPointUTXOs reads from, against the pruning point's header. A clean
// bucket must pass, a bucket missing one coin must fail, a bucket mid-rewrite must be reported as not
// ready rather than as bad, and a request for a pruning point that is not the current one must be
// answered with ErrWrongPruningPointHash.
func TestCheckUTXOHealthJudgesTheServedSet(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		defer constants.ForceSetBlockVersion(1)

		consensusConfig.POWScores = []uint64{math.MaxUint64}
		consensusConfig.K = append([]externalapi.KType(nil), consensusConfig.K...)
		consensusConfig.K[0] = 0
		consensusConfig.FinalityDuration = []time.Duration{5 * consensusConfig.TargetTimePerBlock[0]}
		consensusConfig.PruningProofM = 1

		newPrunedConsensus := func(name string) (testapi.TestConsensus, *externalapi.DomainHash, func(bool)) {
			tc, teardown, err := consensus.NewFactory().NewTestConsensus(consensusConfig, name)
			if err != nil {
				t.Fatalf("NewTestConsensus: %+v", err)
			}
			tip := consensusConfig.GenesisHash
			for i := range 60 {
				tip, _, err = tc.AddBlock([]*externalapi.DomainHash{tip}, nil, nil)
				if err != nil {
					t.Fatalf("AddBlock #%d: %+v", i, err)
				}
			}
			pruningPoint, err := tc.PruningPoint()
			if err != nil {
				t.Fatalf("PruningPoint: %+v", err)
			}
			if pruningPoint.Equal(consensusConfig.GenesisHash) {
				t.Fatalf("setup: the pruning point never moved")
			}
			return tc, pruningPoint, teardown
		}

		t.Run("clean", func(t *testing.T) {
			tc, pruningPoint, teardown := newPrunedConsensus("TestCheckUTXOHealthClean")
			defer teardown(false)

			_, err := tc.CheckUTXOHealth(consensusConfig.GenesisHash)
			if !errors.Is(err, ruleerrors.ErrWrongPruningPointHash) {
				t.Fatalf("a stale pruning point must be ErrWrongPruningPointHash, got %v", err)
			}

			health, err := tc.CheckUTXOHealth(pruningPoint)
			if err != nil {
				t.Fatalf("CheckUTXOHealth: %+v", err)
			}
			if !health.Ready || !health.Verified {
				t.Fatalf("a clean served set must verify: %+v", health)
			}
			if health.EntryCount == 0 || !health.SetMultiset.Equal(health.HeaderCommitment) {
				t.Fatalf("a verified result must carry matching values: %+v", health)
			}
		})

		t.Run("rewriting", func(t *testing.T) {
			tc, pruningPoint, teardown := newPrunedConsensus("TestCheckUTXOHealthRewriting")
			defer teardown(false)

			stagingArea := model.NewStagingArea()
			tc.PruningStore().StageStartUpdatingPruningPointUTXOSet(stagingArea)
			if err := staging.CommitAllChanges(tc.DatabaseContext(), stagingArea); err != nil {
				t.Fatalf("CommitAllChanges: %+v", err)
			}

			health, err := tc.CheckUTXOHealth(pruningPoint)
			if err != nil {
				t.Fatalf("CheckUTXOHealth: %+v", err)
			}
			if health.Ready || health.Verified {
				t.Fatalf("a set being rewritten must be reported as not ready: %+v", health)
			}
		})

		t.Run("missing coin", func(t *testing.T) {
			tc, pruningPoint, teardown := newPrunedConsensus("TestCheckUTXOHealthMissingCoin")
			defer teardown(false)

			iterator, err := tc.PruningStore().PruningPointUTXOIterator(tc.DatabaseContext())
			if err != nil {
				t.Fatalf("PruningPointUTXOIterator: %+v", err)
			}
			if !iterator.First() {
				t.Fatalf("setup: the served set is empty")
			}
			outpoint, entry, err := iterator.Get()
			iterator.Close()
			if err != nil {
				t.Fatalf("Get: %+v", err)
			}
			diff, err := utxo.NewUTXODiffFromCollections(utxo.NewUTXOCollection(nil),
				utxo.NewUTXOCollection(map[externalapi.DomainOutpoint]externalapi.UTXOEntry{*outpoint: entry}))
			if err != nil {
				t.Fatalf("NewUTXODiffFromCollections: %+v", err)
			}
			if err := tc.PruningStore().UpdatePruningPointUTXOSet(tc.DatabaseContext(), diff); err != nil {
				t.Fatalf("UpdatePruningPointUTXOSet: %+v", err)
			}

			health, err := tc.CheckUTXOHealth(pruningPoint)
			if err != nil {
				t.Fatalf("CheckUTXOHealth: %+v", err)
			}
			if !health.Ready || health.Verified {
				t.Fatalf("a served set missing a coin must not verify: %+v", health)
			}
		})
	})
}
