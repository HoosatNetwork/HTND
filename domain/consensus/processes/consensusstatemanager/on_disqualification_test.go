package consensusstatemanager_test

import (
	"strings"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/testutils"
	"github.com/HoosatNetwork/HTND/v2/util/staging"
)

// TestOnDisqualificationReportsInheritedDisqualification pins that a block disqualified by inheritance
// - through ResolveBlockStatus's cascade branch, which never runs verifyUTXO - is still reported to
// Config.OnDisqualification, and that the reason names the root disqualification. The node wires this
// callback to a panic, so a cascade that skipped it would disqualify blocks silently.
func TestOnDisqualificationReportsInheritedDisqualification(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		factory := consensus.NewFactory()

		builder, teardownBuilder, err := factory.NewTestConsensus(consensusConfig,
			"TestOnDisqualificationReportsInheritedDisqualification_builder")
		if err != nil {
			t.Fatalf("Error setting up builder consensus: %+v", err)
		}
		defer teardownBuilder(false)

		const chainLength = 6
		chain := make([]*externalapi.DomainHash, 0, chainLength)
		tipHash := consensusConfig.GenesisHash
		for i := range chainLength {
			tipHash, _, err = builder.AddBlock([]*externalapi.DomainHash{tipHash}, nil, nil)
			if err != nil {
				t.Fatalf("Error adding block %d to the builder: %+v", i, err)
			}
			chain = append(chain, tipHash)
		}

		type report struct {
			blockHash *externalapi.DomainHash
			reason    string
		}
		var reports []report
		consensusConfig.OnDisqualification = func(blockHash *externalapi.DomainHash, reason string) {
			reports = append(reports, report{blockHash, reason})
		}
		tc, teardown, err := factory.NewTestConsensus(consensusConfig, "TestOnDisqualificationReportsInheritedDisqualification")
		if err != nil {
			t.Fatalf("Error setting up consensus: %+v", err)
		}
		defer teardown(false)

		for i, blockHash := range chain {
			block, found, err := builder.GetBlock(blockHash)
			if err != nil || !found {
				t.Fatalf("Error getting block %d from the builder: found=%t err=%+v", i, found, err)
			}
			err = tc.ValidateAndInsertBlock(block, i == 0, true)
			if err != nil {
				t.Fatalf("Error inserting block %d: %+v", i, err)
			}
		}
		if len(reports) != 0 {
			t.Fatalf("expected no disqualification reports for a valid chain, got %d: %+v", len(reports), reports)
		}

		stagingArea := model.NewStagingArea()
		tc.BlockStatusStore().Stage(stagingArea, chain[0], externalapi.StatusDisqualifiedFromChain)
		err = staging.CommitAllChanges(tc.DatabaseContext(), stagingArea)
		if err != nil {
			t.Fatalf("Error disqualifying the root: %+v", err)
		}

		_, _, err = tc.ResolveVirtualWithMaxParam(chainLength)
		if err != nil {
			t.Fatalf("Error resolving virtual: %+v", err)
		}

		if len(reports) == 0 {
			t.Fatalf("expected the inherited disqualification to be reported, got no reports")
		}
		for _, r := range reports {
			if !strings.Contains(r.reason, "root disqualification is block "+chain[0].String()) {
				t.Errorf("report for %s does not name root %s: %s", r.blockHash, chain[0], r.reason)
			}
		}
		last := reports[len(reports)-1]
		if !last.blockHash.Equal(tipHash) {
			t.Errorf("expected the last report to be for the chain tip %s, got %s", tipHash, last.blockHash)
		}
	})
}
