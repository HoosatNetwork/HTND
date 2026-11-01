package consensus

import (
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database"
)

func TestNewTestConsensusTeardownClosesScannerBeforeDatabase(t *testing.T) {
	factory := NewFactory()
	factory.SetTestDataDir(t.TempDir())
	cfg := &Config{Params: dagconfig.DevnetParams, DeferPruningPointUTXOSetUpdate: true}
	tc, teardown, err := factory.NewTestConsensus(cfg, "TestNewTestConsensusTeardownClosesScannerBeforeDatabase")
	if err != nil {
		t.Fatal(err)
	}
	c := tc.(*testConsensus).consensus
	defer c.Close()
	scanner := &c.pruningPointUTXOSetScanner
	cancel := scanner.cancel
	scanner.cancel = func() {
		// Close must run while its database is still usable.
		if _, err := tc.Database().Has(database.MakeBucket(nil).Key([]byte("teardown-probe"))); err != nil {
			t.Errorf("scanner stopped after its database was closed: %v", err)
		}
		cancel()
	}
	teardown(true)
	select {
	case <-scanner.done:
	default:
		t.Error("test teardown returned with the scanner still running")
	}
}
