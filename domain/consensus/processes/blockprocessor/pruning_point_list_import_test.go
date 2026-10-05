package blockprocessor_test

import (
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/testapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/testutils"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
)

// pruningPointUTXOs pages the syncer's pruning point UTXO set the way IBD does.
func pruningPointUTXOs(t *testing.T, syncer testapi.TestConsensus, pruningPoint *externalapi.DomainHash,
) []*externalapi.OutpointAndUTXOEntryPair {
	const step = 100_000
	var fromOutpoint *externalapi.DomainOutpoint
	var utxos []*externalapi.OutpointAndUTXOEntryPair
	for {
		page, err := syncer.GetPruningPointUTXOs(pruningPoint, fromOutpoint, step)
		if err != nil {
			t.Fatalf("GetPruningPointUTXOs: %+v", err)
		}
		fullPage := len(page) == step
		if fromOutpoint != nil && len(page) > 0 && page[0].Outpoint.Equal(fromOutpoint) {
			page = page[1:]
		}
		if len(page) == 0 {
			return utxos
		}
		utxos = append(utxos, page...)
		fromOutpoint = page[len(page)-1].Outpoint
		if !fullPage {
			return utxos
		}
	}
}

// TestImportedPruningPointListGateActive is HTN-006's activated path: a syncee that holds only a headers proof, the
// pruning point headers and the headers above the pruning point imports the pruning point with
// ValidateIBDPruningListVersion moved to version 1, so ArePruningPointsInValidChain runs (and, on a branch where that
// gate also covers it, IsValidPruningPoint). The selected-chain walk ArePruningPointsInValidChain used to do failed the
// import here with "Pruning point is not expected pruning point at index". The list is also checked with every pruning point's commitment followed (anchor at version 1),
// which on the syncee reads the headers of pruning points it holds nothing else for.
func TestImportedPruningPointListGateActive(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, cfg *consensus.Config) {
		if cfg.Name != dagconfig.MainnetParams.Name {
			return
		}
		headersProofTestConfig(cfg)
		factory := consensus.NewFactory()
		syncer, teardownSyncer, err := factory.NewTestConsensus(cfg, "TestImportedPruningPointListGateActiveSyncer")
		if err != nil {
			t.Fatalf("NewTestConsensus: %+v", err)
		}
		defer teardownSyncer(false)
		constants.ForceSetBlockVersion(10)
		buildWideDAG(t, syncer, cfg.GenesisHash, 2, 5, nil)

		syncee, teardownSyncee := syncThroughHeadersProof(t, factory, cfg, syncer,
			"TestImportedPruningPointListGateActiveSyncee")
		defer teardownSyncee(false)

		pruningPoint, err := syncer.PruningPoint()
		if err != nil {
			t.Fatalf("PruningPoint: %+v", err)
		}
		pruningPoints, err := syncer.PruningPointHeaders()
		if err != nil {
			t.Fatalf("PruningPointHeaders: %+v", err)
		}
		if len(pruningPoints) < 3 {
			t.Fatalf("the syncer's pruning point advanced only %d time(s), too few to test a list",
				len(pruningPoints)-1)
		}

		valid, err := syncee.PruningManager().ArePruningPointsInValidChain(model.NewStagingArea(), 1)
		if err != nil {
			t.Fatalf("ArePruningPointsInValidChain, every commitment followed: %+v", err)
		}
		if !valid {
			t.Fatal("ArePruningPointsInValidChain, every commitment followed: the syncer's own list is invalid")
		}

		restore := setPruningListGate(syncee, 1)
		defer restore()
		if err := syncee.AppendImportedPruningPointUTXOs(pruningPointUTXOs(t, syncer, pruningPoint)); err != nil {
			t.Fatalf("AppendImportedPruningPointUTXOs: %+v", err)
		}
		if err := syncee.ValidateAndInsertImportedPruningPoint(pruningPoint); err != nil {
			t.Fatalf("ValidateAndInsertImportedPruningPoint with the pruning point list gate active: %+v", err)
		}
	})
}
