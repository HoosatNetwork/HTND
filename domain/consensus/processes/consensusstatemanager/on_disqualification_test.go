package consensusstatemanager_test

import (
	"strings"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/merkle"
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

// TestOnDisqualificationReportsVerifyUTXOFailureDetails pins what the report handed to
// Config.OnDisqualification carries for a block that fails verifyUTXO itself: every check with its
// verdict, the toleration inputs, the miner, the selected parent, the pruning point and the merge set.
// It is what the node prints when it stops on a disqualification.
func TestOnDisqualificationReportsVerifyUTXOFailureDetails(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		var reasons []string
		consensusConfig.OnDisqualification = func(_ *externalapi.DomainHash, reason string) {
			reasons = append(reasons, reason)
		}
		tc, teardown, err := consensus.NewFactory().NewTestConsensus(consensusConfig,
			"TestOnDisqualificationReportsVerifyUTXOFailureDetails")
		if err != nil {
			t.Fatalf("Error setting up consensus: %+v", err)
		}
		defer teardown(false)

		// A block right above genesis merges nothing that pays, so its coinbase has no output to overpay.
		parent := consensusConfig.GenesisHash
		for i := 0; i < 2; i++ {
			parent, _, err = tc.AddBlock([]*externalapi.DomainHash{parent}, nil, nil)
			if err != nil {
				t.Fatalf("AddBlock %d: %+v", i, err)
			}
		}
		block, _, err := tc.BuildBlockWithParents([]*externalapi.DomainHash{parent},
			&externalapi.DomainCoinbaseData{ScriptPublicKey: &externalapi.ScriptPublicKey{}, ExtraData: []byte("test-miner-tag")}, nil)
		if err != nil {
			t.Fatalf("BuildBlockWithParents: %+v", err)
		}
		block.Transactions[0].Outputs[0].Value++
		mutableHeader := block.Header.ToMutable()
		mutableHeader.SetHashMerkleRoot(merkle.CalculateHashMerkleRoot(block.Transactions))
		block.Header = mutableHeader.ToImmutable()
		err = tc.ValidateAndInsertBlock(block, true, true)
		if err != nil {
			t.Fatalf("ValidateAndInsertBlock: %+v", err)
		}

		if len(reasons) != 1 {
			t.Fatalf("expected one disqualification report, got %d: %q", len(reasons), reasons)
		}
		t.Logf("disqualification report: %s", reasons[0])
		for _, want := range []string{
			"coinbase-transaction check failed",
			"miner: \"test-miner-tag\"",
			"check utxo-commitment: ",
			"check accepted-id-merkle-root: ",
			"check coinbase-transaction: FAIL",
			"<- differs",
			"check block-transactions-vs-past-utxo: pass",
			"toleration: offset baseline signal false",
			"selected parent " + parent.String(),
			"merge set: 1 blues, 0 reds",
		} {
			if !strings.Contains(reasons[0], want) {
				t.Errorf("report does not contain %q:\n%s", want, reasons[0])
			}
		}
	})
}
