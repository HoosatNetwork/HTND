package merkle

import (
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
)

// TestMerkleRootsOfNoTransactions pins that both merkle roots of an empty transaction list are the
// zero hash. CalculateIDMerkleRoot already returned it; CalculateHashMerkleRoot panicked in make().
func TestMerkleRootsOfNoTransactions(t *testing.T) {
	roots := map[string]func([]*externalapi.DomainTransaction) *externalapi.DomainHash{
		"CalculateHashMerkleRoot": CalculateHashMerkleRoot,
		"CalculateIDMerkleRoot":   CalculateIDMerkleRoot,
	}
	for name, root := range roots {
		t.Run(name, func(t *testing.T) {
			if got := root(nil); !got.Equal(&externalapi.DomainHash{}) {
				t.Fatalf("got %s, want the zero hash", got)
			}
		})
	}
}
