package blockprocessor_test

import (
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/testapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/ruleerrors"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/testutils"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
	"github.com/pkg/errors"
)

// setImportGates moves ValidateIBDPruningPointVersion and ValidateIBDPruningListVersion for tc and returns a
// function that restores both.
func setImportGates(tc testapi.TestConsensus, pruningPointVersion, pruningListVersion uint16) (restore func()) {
	gates := tc.HardForkGates()
	previousPoint, previousList := gates.ValidateIBDPruningPointVersion, gates.ValidateIBDPruningListVersion
	gates.ValidateIBDPruningPointVersion, gates.ValidateIBDPruningListVersion = pruningPointVersion, pruningListVersion
	return func() {
		gates.ValidateIBDPruningPointVersion, gates.ValidateIBDPruningListVersion = previousPoint, previousList
	}
}

// TestImportedPruningPointGatesAreSeparate pins that IsValidPruningPoint and ArePruningPointsInValidChain each run
// behind their own gate. Each syncee holds a headers proof and the headers above the syncer's pruning point, and is
// asked to import its own headers selected tip as the pruning point - a block with no depth below the tip.
//
// With only ValidateIBDPruningPointVersion active, that import is refused with ErrUnexpectedPruningPoint, and the
// real pruning point then imports. With only ValidateIBDPruningListVersion active, IsValidPruningPoint does not run,
// so whatever stops the import, it is not ErrUnexpectedPruningPoint.
func TestImportedPruningPointGatesAreSeparate(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, cfg *consensus.Config) {
		if cfg.Name != dagconfig.MainnetParams.Name {
			return
		}
		headersProofTestConfig(cfg)
		factory := consensus.NewFactory()
		syncer, teardownSyncer, err := factory.NewTestConsensus(cfg, "TestImportedPruningPointGatesSyncer")
		if err != nil {
			t.Fatalf("NewTestConsensus: %+v", err)
		}
		defer teardownSyncer(false)
		constants.ForceSetBlockVersion(10)
		buildWideDAG(t, syncer, cfg.GenesisHash, 2, 5, nil)

		pruningPoint, err := syncer.PruningPoint()
		if err != nil {
			t.Fatalf("PruningPoint: %+v", err)
		}
		utxos := pruningPointUTXOs(t, syncer, pruningPoint)
		importAt := func(syncee testapi.TestConsensus, hash *externalapi.DomainHash) error {
			if err := syncee.ClearImportedPruningPointData(); err != nil {
				t.Fatalf("ClearImportedPruningPointData: %+v", err)
			}
			if err := syncee.AppendImportedPruningPointUTXOs(utxos); err != nil {
				t.Fatalf("AppendImportedPruningPointUTXOs: %+v", err)
			}
			return syncee.ValidateAndInsertImportedPruningPoint(hash)
		}
		headersSelectedTip := func(syncee testapi.TestConsensus) *externalapi.DomainHash {
			tip, err := syncee.GetHeadersSelectedTip()
			if err != nil {
				t.Fatalf("GetHeadersSelectedTip: %+v", err)
			}
			return tip
		}

		pointOnly, teardownPointOnly := syncThroughHeadersProof(t, factory, cfg, syncer,
			"TestImportedPruningPointGatesPointOnly")
		defer teardownPointOnly(false)
		restore := setImportGates(pointOnly, 1, ^uint16(0))
		err = importAt(pointOnly, headersSelectedTip(pointOnly))
		if !errors.Is(err, ruleerrors.ErrUnexpectedPruningPoint) {
			t.Fatalf("importing the headers selected tip with only the pruning point gate active: want "+
				"ErrUnexpectedPruningPoint, got %+v", err)
		}
		if err := importAt(pointOnly, pruningPoint); err != nil {
			t.Fatalf("importing the real pruning point with only the pruning point gate active: %+v", err)
		}
		restore()

		listOnly, teardownListOnly := syncThroughHeadersProof(t, factory, cfg, syncer,
			"TestImportedPruningPointGatesListOnly")
		defer teardownListOnly(false)
		restore = setImportGates(listOnly, ^uint16(0), 1)
		defer restore()
		err = importAt(listOnly, headersSelectedTip(listOnly))
		if errors.Is(err, ruleerrors.ErrUnexpectedPruningPoint) {
			t.Fatalf("IsValidPruningPoint ran with only the pruning point list gate active: %+v", err)
		}
	})
}
