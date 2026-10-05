package consensushashing_test

import (
	"sync"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/subnetworks"
)

// TestTransactionIDConcurrentCalls guards the lazy transaction ID cache against
// data races. Transactions such as the dagconfig genesis coinbase are shared
// between consensus instances, which hash and clone them concurrently. Run it
// with -race for it to mean anything.
func TestTransactionIDConcurrentCalls(t *testing.T) {
	tx := &externalapi.DomainTransaction{
		Version: 0,
		Inputs:  []*externalapi.DomainTransactionInput{},
		Outputs: []*externalapi.DomainTransactionOutput{{
			Value:           1,
			ScriptPublicKey: &externalapi.ScriptPublicKey{Script: []byte{1, 2, 3}, Version: 0},
		}},
		SubnetworkID: subnetworks.SubnetworkIDNative,
		Payload:      []byte{},
	}
	expectedID := consensushashing.TransactionID(tx.Clone())
	if tx.CachedID() != nil {
		t.Fatalf("hashing a clone must not fill the original's ID cache")
	}

	const goroutines = 8
	var wg sync.WaitGroup
	ids := make([]*externalapi.DomainTransactionID, goroutines)
	for i := range goroutines {
		wg.Go(func() {
			if i%2 == 0 {
				ids[i] = consensushashing.TransactionID(tx)
			} else {
				ids[i] = consensushashing.TransactionID(tx.Clone())
			}
		})
	}
	wg.Wait()

	for i, id := range ids {
		if !id.Equal(expectedID) {
			t.Fatalf("goroutine %d got transaction ID %s, want %s", i, id, expectedID)
		}
	}
	if cached := tx.CachedID(); cached == nil || !cached.Equal(expectedID) {
		t.Fatalf("cached transaction ID is %s, want %s", cached, expectedID)
	}
}
