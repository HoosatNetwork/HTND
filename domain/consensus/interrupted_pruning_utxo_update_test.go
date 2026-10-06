package consensus_test

import (
	"math"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/multiset"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/testutils"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/utxo"
	"github.com/HoosatNetwork/HTND/v2/util/staging"
)

// TestInterruptedPruningPointUTXOSetUpdateResumes pins recovery from a pruning point UTXO set update that was
// interrupted after it began writing the set: on restart the update runs again with the diff method it recorded,
// finishes, clears its records, and leaves the set the chain commits to. Re-choosing the method against a
// half-written set could apply a different diff than an uninterrupted node; on a healthy test DAG both methods yield
// the same diff, so this guards the recovery path rather than reproducing that divergence.
func TestInterruptedPruningPointUTXOSetUpdateResumes(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		defer constants.ForceSetBlockVersion(1)

		consensusConfig.POWScores = []uint64{math.MaxUint64}
		consensusConfig.K = append([]externalapi.KType(nil), consensusConfig.K...)
		consensusConfig.K[0] = 0
		consensusConfig.FinalityDuration = []time.Duration{5 * consensusConfig.TargetTimePerBlock[0]}
		consensusConfig.PruningProofM = 1

		tc, teardown, err := consensus.NewFactory().NewTestConsensus(consensusConfig, "TestInterruptedPruningUTXOUpdate")
		if err != nil {
			t.Fatalf("NewTestConsensus: %+v", err)
		}
		defer teardown(false)

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

		bucketHash := func() *externalapi.DomainHash {
			iterator, err := tc.PruningStore().PruningPointUTXOIterator(tc.DatabaseContext())
			if err != nil {
				t.Fatalf("PruningPointUTXOIterator: %+v", err)
			}
			defer iterator.Close()
			set := multiset.New()
			for ok := iterator.First(); ok; ok = iterator.Next() {
				outpoint, entry, err := iterator.Get()
				if err != nil {
					t.Fatalf("Get: %+v", err)
				}
				serialized, err := utxo.SerializeUTXO(entry, outpoint)
				if err != nil {
					t.Fatalf("SerializeUTXO: %+v", err)
				}
				set.Add(serialized)
			}
			return set.Hash()
		}
		uninterrupted := bucketHash()

		// The state a node restarts into when it stopped after writing the set but before finishing the update.
		stagingArea := model.NewStagingArea()
		tc.PruningStore().StageStartUpdatingPruningPointUTXOSet(stagingArea)
		if err := staging.CommitAllChanges(tc.DatabaseContext(), stagingArea); err != nil {
			t.Fatalf("CommitAllChanges: %+v", err)
		}
		if err := tc.PruningStore().StorePruningPointUTXOSetUpdateMethod(tc.DatabaseContext(), pruningPoint, "acceptance-data"); err != nil {
			t.Fatalf("StorePruningPointUTXOSetUpdateMethod: %+v", err)
		}

		if err := tc.PruningManager().UpdatePruningPointIfRequired(); err != nil {
			t.Fatalf("resuming the interrupted update failed: %+v", err)
		}

		started, err := tc.PruningStore().HadStartedUpdatingPruningPointUTXOSet(tc.DatabaseContext())
		if err != nil {
			t.Fatalf("HadStartedUpdatingPruningPointUTXOSet: %+v", err)
		}
		if started {
			t.Fatalf("the resumed update did not finish")
		}
		if _, found, err := tc.PruningStore().PruningPointUTXOSetUpdateMethod(tc.DatabaseContext(), pruningPoint); err != nil || found {
			t.Fatalf("the resumed update left its method record behind (found %t, err %v)", found, err)
		}
		if resumed := bucketHash(); !resumed.Equal(uninterrupted) {
			t.Fatalf("the resumed update changed the served set: %s, uninterrupted %s", resumed, uninterrupted)
		}
	})
}
