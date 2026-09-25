package consensus

import (
	"fmt"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/ruleerrors"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
	"github.com/pkg/errors"
)

// TestValidatePruningPointProofRejectsEmptyLevel pins that a pruning point proof whose first level has
// no headers is rejected with ErrPruningProofEmpty. It used to index that level's last header anyway,
// and the panic - raised on a proof straight from an IBD peer - took the node down.
func TestValidatePruningPointProofRejectsEmptyLevel(t *testing.T) {
	config := &Config{Params: dagconfig.MainnetParams}
	config.SkipProofOfWork = true
	tc, teardown, err := NewFactory().NewTestConsensus(config, "TestValidatePruningPointProofRejectsEmptyLevel")
	if err != nil {
		t.Fatalf("NewTestConsensus: %+v", err)
	}
	defer teardown(false)

	proof := &externalapi.PruningPointProof{Headers: [][]externalapi.BlockHeader{{}}}

	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				err = fmt.Errorf("panic: %v", recovered)
			}
		}()
		err = tc.ValidatePruningPointProof(proof)
	}()

	if !errors.Is(err, ruleerrors.ErrPruningProofEmpty) {
		t.Fatalf("expected ErrPruningProofEmpty, got %v", err)
	}
}
