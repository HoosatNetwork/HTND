package blockbuilder_test

import (
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus"
	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/testutils"
)

// TestBuiltCoinbasePaysTheFeesOfTheMergeSet pins the one property that connects block creation to
// block validation: a block this node builds has to pass this node's own coinbase check.
//
// The coinbase pays each merged block its subsidy plus the fees of the transactions that block's
// acceptance data says were accepted, and the two sides read that from different places - the
// builder from virtual's acceptance data, the validator from acceptance data it recalculates for the
// block being validated. Nothing else in the suite makes them meet over a merge set that actually
// carries a fee, so a builder that credits only subsidies builds a block every validator rejects
// with ErrBadCoinbaseTransaction, and the only place it shows up is mainnet.
func TestBuiltCoinbasePaysTheFeesOfTheMergeSet(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		consensusConfig.BlockCoinbaseMaturity = 0
		factory := consensus.NewFactory()
		tc, teardown, err := factory.NewTestConsensus(consensusConfig, "TestBuiltCoinbasePaysTheFeesOfTheMergeSet")
		if err != nil {
			t.Fatalf("Error setting up consensus: %+v", err)
		}
		defer teardown(false)

		// A transaction to fund the fee-paying one. Its outputs are put straight into virtual's UTXO
		// set, which is what StageTransactionOutputsToVirtual is for, so the funding transaction
		// itself needs no history.
		bootstrapTransaction := testutils.CreateTransactionWithOutput(10000)
		err = testutils.StageTransactionOutputsToVirtual(tc, bootstrapTransaction, 0)
		if err != nil {
			t.Fatalf("StageTransactionOutputsToVirtual: %+v", err)
		}
		fundingTransaction, err := testutils.CreateTransaction(bootstrapTransaction, 0)
		if err != nil {
			t.Fatalf("Error creating the funding transaction: %+v", err)
		}
		fundingBlockHash, _, err := tc.AddBlock([]*externalapi.DomainHash{consensusConfig.GenesisHash}, nil,
			[]*externalapi.DomainTransaction{fundingTransaction})
		if err != nil {
			t.Fatalf("Error creating the funding block: %+v", err)
		}

		const fee = uint64(880)
		feePayingTransaction, err := testutils.CreateTransaction(fundingTransaction, fee)
		if err != nil {
			t.Fatalf("Error creating the fee paying transaction: %+v", err)
		}
		mergedBlockHash, _, err := tc.AddBlock([]*externalapi.DomainHash{fundingBlockHash}, nil,
			[]*externalapi.DomainTransaction{feePayingTransaction})
		if err != nil {
			t.Fatalf("Error creating the block that carries the fee: %+v", err)
		}

		// Virtual now merges the block that carries the fee. Read the fee back out of virtual's own
		// acceptance data rather than trusting the arithmetic above, so the test cannot pass by
		// building a coinbase for a merge set that carries nothing.
		stagingArea := model.NewStagingArea()
		virtualAcceptanceData, err := tc.AcceptanceDataStore().Get(tc.DatabaseContext(), stagingArea,
			model.VirtualBlockHash)
		if err != nil {
			t.Fatalf("Error getting virtual's acceptance data: %+v", err)
		}
		acceptedFeeTotal := uint64(0)
		for _, blockAcceptanceData := range virtualAcceptanceData {
			for _, txAcceptanceData := range blockAcceptanceData.TransactionAcceptanceData {
				if txAcceptanceData.IsAccepted {
					acceptedFeeTotal += txAcceptanceData.Fee
				}
			}
		}
		if acceptedFeeTotal != fee {
			t.Fatalf("virtual's acceptance data carries %d in fees, want %d - the merge set does not hold the "+
				"fee this test is about (merged block %s)", acceptedFeeTotal, fee, mergedBlockHash)
		}

		block, err := tc.BuildBlock(&externalapi.DomainCoinbaseData{
			ScriptPublicKey: &externalapi.ScriptPublicKey{Script: nil, Version: 0},
			ExtraData:       nil,
		}, nil)
		if err != nil {
			t.Fatalf("Error building the block: %+v", err)
		}

		// The validator recalculates the acceptance data for this block and rebuilds the coinbase it
		// expects from it. If the builder credited only subsidies, this is where it is caught.
		err = tc.ValidateAndInsertBlock(block, true, true)
		if err != nil {
			t.Fatalf("The block this node built was rejected by this node: %+v", err)
		}
	})
}
