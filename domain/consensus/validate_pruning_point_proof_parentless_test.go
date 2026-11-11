package consensus

import (
	"fmt"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/ruleerrors"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
	"github.com/pkg/errors"
)

// TestValidatePruningPointProofRejectsParentlessHeader pins that a proof header with no parents is
// rejected unless it is genesis. A parentless header gets the maximal block level without any proof
// of work being computed (pow.BlockLevel treats it as genesis), so it could stand in for a header at
// every level, including the pruning point itself.
func TestValidatePruningPointProofRejectsParentlessHeader(t *testing.T) {
	params := dagconfig.MainnetParams
	config := &Config{Params: params}
	tc, teardown, err := NewFactory().NewTestConsensus(config, "TestValidatePruningPointProofRejectsParentlessHeader")
	if err != nil {
		t.Fatalf("NewTestConsensus: %+v", err)
	}
	defer teardown(false)

	notGenesis := params.GenesisBlock.Header.ToMutable()
	notGenesis.SetNonce(params.GenesisBlock.Header.Nonce() + 1)
	parentless := notGenesis.ToImmutable()

	levels := make([][]externalapi.BlockHeader, params.MaxBlockLevel+1)
	for i := range levels {
		levels[i] = []externalapi.BlockHeader{parentless}
	}

	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				err = fmt.Errorf("panic: %v", recovered)
			}
		}()
		err = tc.ValidatePruningPointProof(&externalapi.PruningPointProof{Headers: levels})
	}()

	if !errors.Is(err, ruleerrors.ErrPruningProofParentlessHeader) {
		t.Fatalf("expected ErrPruningProofParentlessHeader, got %v", err)
	}
}
