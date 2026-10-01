package mempool

import (
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/testutils"
	"github.com/HoosatNetwork/HTND/v2/domain/consensusreference"
)

// TestGetTransactionsByAddressesListsEveryTransaction pins that every pending transaction of an address is
// reported, and that asking for orphans too does not replace the transaction pool's answer. The maps held
// one transaction per address, so all but one pending spend went unreported; and the orphan branch wrote its
// map into the transaction-pool slot, so with orphans included the pool's own spends disappeared - wallets
// that exclude pending spends through GetMempoolEntriesByAddresses then reused coins already being spent.
func TestGetTransactionsByAddressesListsEveryTransaction(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		tc, teardown, err := consensus.NewFactory().NewTestConsensus(consensusConfig, "TestGetTransactionsByAddressesListsEveryTransaction")
		if err != nil {
			t.Fatalf("Error setting up consensus: %+v", err)
		}
		defer teardown(false)

		tcAsConsensus := tc.(externalapi.Consensus)
		tcAsConsensusPointer := &tcAsConsensus
		mp := New(DefaultConfig(tc.DAGParams()), consensusreference.NewConsensusReference(&tcAsConsensusPointer)).(*mempool)

		var spends []*externalapi.DomainTransaction
		for i := range 2 {
			// Distinct values give the funding transactions distinct IDs; both pay the same script.
			funding := testutils.CreateTransactionWithOutput(100_000 + uint64(i))
			if err := testutils.StageTransactionOutputsToVirtual(tc, funding, 0); err != nil {
				t.Fatalf("StageTransactionOutputsToVirtual: %+v", err)
			}
			spend, err := testutils.CreateTransaction(funding, 1_000)
			if err != nil {
				t.Fatalf("CreateTransaction: %+v", err)
			}
			if _, err := mp.ValidateAndInsertTransaction(spend, false, false, true); err != nil {
				t.Fatalf("ValidateAndInsertTransaction: %+v", err)
			}
			spends = append(spends, spend)
		}
		script := spends[0].Inputs[0].UTXOEntry.ScriptPublicKey().String()

		for _, includeOrphanPool := range []bool{false, true} {
			sending, receiving, _, _, err := mp.GetTransactionsByAddresses(true, includeOrphanPool)
			if err != nil {
				t.Fatalf("GetTransactionsByAddresses: %+v", err)
			}
			for name, byAddress := range map[string]map[string][]*externalapi.DomainTransaction{"sending": sending, "receiving": receiving} {
				if got := len(byAddress[script]); got != len(spends) {
					t.Fatalf("includeOrphanPool=%t: %s lists %d transactions for the address, want %d",
						includeOrphanPool, name, got, len(spends))
				}
			}
			for _, spend := range spends {
				found := false
				for _, listed := range sending[script] {
					if consensushashing.TransactionID(listed).Equal(consensushashing.TransactionID(spend)) {
						found = true
					}
				}
				if !found {
					t.Fatalf("includeOrphanPool=%t: pending spend %s is not listed", includeOrphanPool, consensushashing.TransactionID(spend))
				}
			}
		}
	})
}
