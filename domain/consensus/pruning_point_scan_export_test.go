package consensus

import (
	"context"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
)

// ScanPruningPointUTXOSetForTest runs one scan synchronously so tests can inspect
// its error. The background scanner must have been stopped with Close first.
func ScanPruningPointUTXOSetForTest(c externalapi.Consensus) error {
	s := c.(*consensus)
	return s.pruningManager.ScanPruningPointUTXOSet(context.Background(), s.lock)
}

// AfterPruningPointUTXOSetScanStopForTest runs afterStop after Close cancels the scan's context. Install it before
// calling Close, with the background scanner running. A test holding its cursor can then release it after cancellation
// while exercising the real Close, including its wait for the scanner.
func AfterPruningPointUTXOSetScanStopForTest(c externalapi.Consensus, afterStop func()) {
	scanner := &c.(*consensus).pruningPointUTXOSetScanner
	cancel := scanner.cancel
	scanner.cancel = func() {
		cancel()
		afterStop()
	}
}
