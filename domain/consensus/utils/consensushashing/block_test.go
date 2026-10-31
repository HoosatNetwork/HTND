package consensushashing_test

import (
	"math/big"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/blockheader"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/consensushashing"
)

func TestHeaderHashNegativeTimestampDoesNotPanic(t *testing.T) {
	header := blockheader.NewImmutableBlockHeader(1, nil, &externalapi.DomainHash{}, &externalapi.DomainHash{},
		&externalapi.DomainHash{}, -1, 0, 0, 0, 0, big.NewInt(0), &externalapi.DomainHash{})
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("HeaderHash panicked on a negative timestamp: %v", r)
		}
	}()
	if consensushashing.HeaderHash(header) == nil {
		t.Fatalf("HeaderHash returned nil")
	}
}
