package pruningmanager

import (
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/datastructures/pruningstore"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
)

// The --enable-utxo-debug-diagnostics repair writes the legacy bucket per key, which a
// kill would leave without a redo manifest: it must refuse while a journal owns the set.
// The manager has no reconstruction dependency, so a repair that ran would panic.
func TestPP42DiagnosticRepairRefusedWhileJournalOwns(t *testing.T) {
	pm := &pruningManager{journal: &pruningstore.Journal{}}
	target := externalapi.NewDomainHashFromByteArray(&[32]byte{1})
	pm.repairPruningPointUTXOSet(model.NewStagingArea(), target, nil, target)
}
