package consensus_test

import (
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/testutils"
)

// TestUTXOSetHealthNeverClaimsUnearnedHealth pins the one property that matters for a signal
// callers use to decide whether to trust a node: it must never report a verified baseline it has
// not actually verified.
//
// A node still on genesis has no imported baseline to be wrong about, so there is nothing to
// verify rather than something that failed. That state reports Checked=false, and - deliberately -
// BaselineVerified=false too, so a caller reading only the boolean errs toward distrust. The two
// are kept apart because consensus needs the opposite default: an unchecked node must NOT have
// per-block commitment toleration switched on, or a node that simply could not read its own
// pruning point would stop verifying commitments entirely.
func TestUTXOSetHealthNeverClaimsUnearnedHealth(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		tc, teardown, err := consensus.NewFactory().NewTestConsensus(consensusConfig, "TestUTXOSetHealth")
		if err != nil {
			t.Fatalf("NewTestConsensus: %+v", err)
		}
		defer teardown(false)

		assertConsistent := func(stage string, health *externalapi.UTXOSetHealth) {
			t.Helper()
			if health.BaselineVerified && !health.Checked {
				t.Errorf("%s: reported a verified baseline without having checked one", stage)
			}
			if health.Checked && health.PruningPoint == nil {
				t.Errorf("%s: a checked result must carry the values it was derived from", stage)
			}
			if !health.Checked && health.BaselineVerified {
				t.Errorf("%s: an unchecked result must not read as verified", stage)
			}
		}

		health, err := tc.UTXOSetHealth()
		if err != nil {
			t.Fatalf("UTXOSetHealth: %+v", err)
		}
		assertConsistent("at genesis", health)
		// A fresh consensus is still on genesis, so this is the "nothing to verify yet" state and
		// not a claim of health. If this ever starts reporting Checked, the assertions below stop
		// being about genesis and the test needs revisiting rather than silently passing.
		if health.Checked {
			t.Fatalf("expected a fresh consensus to have nothing to verify, got a checked result "+
				"for pruning point %s", health.PruningPoint)
		}
		if health.BaselineVerified {
			t.Error("a node that has verified nothing must not report a verified baseline")
		}

		tip := consensusConfig.GenesisHash
		for i := 0; i < 5; i++ {
			tip, _, err = tc.AddBlock([]*externalapi.DomainHash{tip}, nil, nil)
			if err != nil {
				t.Fatalf("AddBlock: %+v", err)
			}
		}

		health, err = tc.UTXOSetHealth()
		if err != nil {
			t.Fatalf("UTXOSetHealth after building a chain: %+v", err)
		}
		assertConsistent("after building a chain", health)
		if health.Checked && !health.BaselineVerified {
			t.Errorf("a self-validated chain reported an offset baseline: pruning point %s, "+
				"stored multiset %s, header commitment %s",
				health.PruningPoint, health.StoredMultiset, health.HeaderCommitment)
		}
	})
}
