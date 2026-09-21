package main

import (
	"strings"
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/testutils"
)

// TestResolveBlockByDAAScoreFindsExactMatch confirms the happy path: walking backward from the
// tip locates a block whose header DAA score exactly matches the requested one.
func TestResolveBlockByDAAScoreFindsExactMatch(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		factory := consensus.NewFactory()
		tc, teardown, err := factory.NewTestConsensus(consensusConfig, "TestResolveBlockByDAAScoreFindsExactMatch")
		if err != nil {
			t.Fatalf("Error setting up consensus: %+v", err)
		}
		defer teardown(false)

		blockHash, _, err := tc.AddBlock([]*externalapi.DomainHash{consensusConfig.GenesisHash}, nil, nil)
		if err != nil {
			t.Fatalf("AddBlock: %+v", err)
		}

		header, err := tc.GetBlockHeader(blockHash)
		if err != nil {
			t.Fatalf("GetBlockHeader: %+v", err)
		}

		resolved, err := resolveBlockByDAAScore(tc, header.DAAScore())
		if err != nil {
			t.Fatalf("resolveBlockByDAAScore: %+v", err)
		}
		if !resolved.Equal(blockHash) {
			t.Fatalf("resolveBlockByDAAScore returned %s, expected %s", resolved, blockHash)
		}
	})
}

// TestResolveBlockByDAAScoreRejectsBeyondTip confirms that requesting a DAA score higher than the
// current tip fails with a clear, actionable error rather than an obscure lookup failure.
func TestResolveBlockByDAAScoreRejectsBeyondTip(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		factory := consensus.NewFactory()
		tc, teardown, err := factory.NewTestConsensus(consensusConfig, "TestResolveBlockByDAAScoreRejectsBeyondTip")
		if err != nil {
			t.Fatalf("Error setting up consensus: %+v", err)
		}
		defer teardown(false)

		_, err = resolveBlockByDAAScore(tc, 999_999_999)
		if err == nil {
			t.Fatalf("expected an error when requesting a DAA score beyond the tip, got nil")
		}
		if !strings.Contains(err.Error(), "beyond the node's current tip") {
			t.Fatalf("expected a 'beyond the node's current tip' error, got: %+v", err)
		}
	})
}

// TestResolveBlockByDAAScoreRejectsPrunedHistory confirms that requesting a DAA score older than
// the local pruning point fails fast with a clear, actionable error - instead of walking all the
// way back to the pruning point and following its synthetic "virtual genesis" GHOSTDAG
// selected-parent marker into a confusing low-level "block header ...fefe...fe does not exist"
// failure (the bug this test guards against).
func TestResolveBlockByDAAScoreRejectsPrunedHistory(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		factory := consensus.NewFactory()
		tc, teardown, err := factory.NewTestConsensus(consensusConfig, "TestResolveBlockByDAAScoreRejectsPrunedHistory")
		if err != nil {
			t.Fatalf("Error setting up consensus: %+v", err)
		}
		defer teardown(false)

		pruningPointHash, err := tc.PruningPoint()
		if err != nil {
			t.Fatalf("PruningPoint: %+v", err)
		}
		pruningPointHeader, err := tc.GetBlockHeader(pruningPointHash)
		if err != nil {
			t.Fatalf("GetBlockHeader: %+v", err)
		}
		if pruningPointHeader.DAAScore() == 0 {
			t.Skip("cannot request a DAA score below the pruning point's when the pruning point is at DAA score 0")
		}

		_, err = resolveBlockByDAAScore(tc, pruningPointHeader.DAAScore()-1)
		if err == nil {
			t.Fatalf("expected an error when requesting a DAA score older than the pruning point, got nil")
		}
		if !strings.Contains(err.Error(), "pruned") {
			t.Fatalf("expected a 'pruned' error, got: %+v", err)
		}
	})
}
