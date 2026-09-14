package blockheader

import (
	"math/big"
	"sync"
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
)

// TestBlockLevelConcurrentCalls guards the lazy block level cache against data
// races. Immutable headers such as the dagconfig genesis are shared between
// consensus instances, which call BlockLevel on them concurrently. Run it with
// -race for it to mean anything.
func TestBlockLevelConcurrentCalls(t *testing.T) {
	const maxBlockLevel = 225

	header := NewImmutableBlockHeader(
		1,
		[]externalapi.BlockLevelParents{},
		&externalapi.DomainHash{},
		&externalapi.DomainHash{},
		&externalapi.DomainHash{},
		0,
		0,
		0,
		0,
		0,
		big.NewInt(0),
		&externalapi.DomainHash{},
	)

	const goroutines = 8
	var wg sync.WaitGroup
	levels := make([]int, goroutines)
	for i := range goroutines {
		wg.Go(func() {
			levels[i] = header.BlockLevel(maxBlockLevel)
		})
	}
	wg.Wait()

	for i, level := range levels {
		if level != maxBlockLevel {
			t.Fatalf("goroutine %d got block level %d, want %d", i, level, maxBlockLevel)
		}
	}
	if level := header.BlockLevel(maxBlockLevel); level != maxBlockLevel {
		t.Fatalf("cached block level is %d, want %d", level, maxBlockLevel)
	}
}
