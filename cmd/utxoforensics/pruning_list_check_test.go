package main

import (
	"math"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
	"github.com/HoosatNetwork/HTND/v2/domain/prefixmanager/prefix"
)

// TestPruningListCheck runs -pplistcheck against a TestConsensus that has advanced its pruning point several times,
// first as the consensus wrote it and then with a too-shallow pruning point staged on top. In both, each reproduced
// check must return what the node's own pruning manager returns on the same data, and the ungated header walk must
// pass the untouched list and fail the shallow one.
func TestPruningListCheck(t *testing.T) {
	config := &consensus.Config{Params: dagconfig.MainnetParams}
	config.SkipProofOfWork = true
	config.DisableDifficultyAdjustment = true
	config.POWScores = []uint64{math.MaxUint64}
	targetTime := config.TargetTimePerBlock[0]
	config.TargetTimePerBlock = []time.Duration{targetTime}
	config.FinalityDuration = []time.Duration{10 * targetTime}
	config.K = []externalapi.KType{2}
	config.MergeSetSizeLimit = 2
	config.PruningMultiplier = []uint64{1}

	tc, teardown, err := consensus.NewFactory().NewTestConsensus(config, "TestPruningListCheck")
	if err != nil {
		t.Fatalf("NewTestConsensus: %+v", err)
	}
	defer teardown(false)

	chain := []*externalapi.DomainHash{config.GenesisHash}
	for i := range 200 {
		tip, _, err := tc.AddBlock([]*externalapi.DomainHash{chain[len(chain)-1]}, nil, nil)
		if err != nil {
			t.Fatalf("AddBlock #%d: %+v", i, err)
		}
		chain = append(chain, tip)
	}

	s, err := newStores(tc.DatabaseContext(), (&prefix.Prefix{}).Serialize())
	if err != nil {
		t.Fatalf("newStores: %+v", err)
	}
	currentIndex, err := s.pruning.CurrentPruningPointIndex(s.db, model.NewStagingArea())
	if err != nil {
		t.Fatalf("CurrentPruningPointIndex: %+v", err)
	}
	if currentIndex < 2 {
		t.Fatalf("the pruning point advanced only %d time(s), too few to test a list", currentIndex)
	}

	nodeVerdict := func(ok bool, err error) string {
		switch {
		case err != nil:
			return "ERROR"
		case ok:
			return "PASS"
		default:
			return "FAIL"
		}
	}
	check := func(scenario string, stagingArea *model.StagingArea, wantHeaderWalk string) {
		verdicts, err := pruningListCheck(s, stagingArea, &config.Params)
		if err != nil {
			t.Fatalf("%s: pruningListCheck: %+v", scenario, err)
		}
		pruningPoint, err := tc.PruningStore().PruningPoint(tc.DatabaseContext(), stagingArea)
		if err != nil {
			t.Fatalf("%s: PruningPoint: %+v", scenario, err)
		}

		want := nodeVerdict(tc.PruningManager().IsValidPruningPoint(stagingArea, pruningPoint))
		if verdicts.validPruningPoint.outcome != want {
			t.Errorf("%s: IsValidPruningPoint: the node says %s, the tool %s: %s", scenario, want,
				verdicts.validPruningPoint.outcome, verdicts.validPruningPoint.detail)
		}
		want = nodeVerdict(tc.PruningManager().ArePruningPointsInValidChain(stagingArea))
		if verdicts.validChain.outcome != want {
			t.Errorf("%s: ArePruningPointsInValidChain: the node says %s, the tool %s: %s", scenario, want,
				verdicts.validChain.outcome, verdicts.validChain.detail)
		}
		if verdicts.headerWalk.outcome != wantHeaderWalk {
			t.Errorf("%s: header walk: want %s, got %s: %s", scenario, wantHeaderWalk,
				verdicts.headerWalk.outcome, verdicts.headerWalk.detail)
		}
	}

	check("untouched", model.NewStagingArea(), "PASS")

	// Staged, never committed, so the database stays as the consensus wrote it. Shards are per store instance, so
	// the tool's pruning store and the consensus' own each need it staged.
	shallow := model.NewStagingArea()
	if err := s.pruning.StagePruningPoint(s.db, shallow, chain[len(chain)-2]); err != nil {
		t.Fatalf("StagePruningPoint: %+v", err)
	}
	if err := tc.PruningStore().StagePruningPoint(tc.DatabaseContext(), shallow, chain[len(chain)-2]); err != nil {
		t.Fatalf("StagePruningPoint: %+v", err)
	}
	check("shallow pruning point", shallow, "FAIL")
}
