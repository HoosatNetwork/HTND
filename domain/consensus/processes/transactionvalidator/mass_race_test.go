package transactionvalidator_test

import (
	"sync"
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/testutils"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/transactionhelper"
)

// TestPopulateMassConcurrentCalls guards lazy mass population against data
// races. Transactions such as the dagconfig genesis coinbase are shared between
// consensus instances, whose block body validation populates their mass
// concurrently. Run it with -race for it to mean anything.
func TestPopulateMassConcurrentCalls(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		cfg := *consensusConfig

		factory := consensus.NewFactory()
		tc, teardown, err := factory.NewTestConsensus(&cfg, "TestPopulateMassConcurrentCalls")
		if err != nil {
			t.Fatalf("Error setting up consensus: %+v", err)
		}
		defer teardown(false)

		tx := transactionhelper.NewNativeTransaction(0, []*externalapi.DomainTransactionInput{},
			[]*externalapi.DomainTransactionOutput{{
				Value:           1,
				ScriptPublicKey: &externalapi.ScriptPublicKey{Script: []byte{1, 2, 3}, Version: 0},
			}})

		expected := tx.Clone()
		tc.PopulateMass(expected)
		expectedMass := expected.LoadMass()
		if expectedMass == 0 {
			t.Fatalf("PopulateMass left the mass unpopulated")
		}

		const goroutines = 8
		var wg sync.WaitGroup
		for i := range goroutines {
			wg.Go(func() {
				if i%2 == 0 {
					tc.PopulateMass(tx)
				} else {
					tc.PopulateMass(tx.Clone())
				}
			})
		}
		wg.Wait()

		if mass := tx.LoadMass(); mass != expectedMass {
			t.Fatalf("populated mass is %d, want %d", mass, expectedMass)
		}
	})
}
