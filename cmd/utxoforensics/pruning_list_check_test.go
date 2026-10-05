package main

import (
	"fmt"
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
// first as the consensus wrote it and then with a too-shallow pruning point staged on top, each with the anchor at
// block version 1 (follow commitments to genesis) and unreached. In each, both reproduced checks must return what the
// node's own pruning manager returns on the same data, the list check must pass the untouched list and fail the
// shallow one, and so must the ungated header walk.
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
	checkWithAnchor := func(scenario string, stagingArea *model.StagingArea, anchor uint16, want string) {
		scenario = fmt.Sprintf("%s, anchor %d", scenario, anchor)
		verdicts, err := pruningListCheck(s, stagingArea, &config.Params, anchor)
		if err != nil {
			t.Fatalf("%s: pruningListCheck: %+v", scenario, err)
		}
		pruningPoint, err := tc.PruningStore().PruningPoint(tc.DatabaseContext(), stagingArea)
		if err != nil {
			t.Fatalf("%s: PruningPoint: %+v", scenario, err)
		}

		node := nodeVerdict(tc.PruningManager().IsValidPruningPoint(stagingArea, pruningPoint))
		if verdicts.validPruningPoint.outcome != node {
			t.Errorf("%s: IsValidPruningPoint: the node says %s, the tool %s: %s", scenario, node,
				verdicts.validPruningPoint.outcome, verdicts.validPruningPoint.detail)
		}
		node = nodeVerdict(tc.PruningManager().ArePruningPointsInValidChain(stagingArea, anchor))
		if node != want {
			t.Errorf("%s: ArePruningPointsInValidChain: want %s, the node says %s", scenario, want, node)
		}
		if verdicts.validChain.outcome != node {
			t.Errorf("%s: ArePruningPointsInValidChain: the node says %s, the tool %s: %s", scenario, node,
				verdicts.validChain.outcome, verdicts.validChain.detail)
		}
		if verdicts.headerWalk.outcome != want {
			t.Errorf("%s: header walk: want %s, got %s: %s", scenario, want,
				verdicts.headerWalk.outcome, verdicts.headerWalk.detail)
		}
	}
	check := func(scenario string, stagingArea *model.StagingArea, want string) {
		for _, anchor := range []uint16{1, ^uint16(0)} {
			checkWithAnchor(scenario, stagingArea, anchor, want)
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
