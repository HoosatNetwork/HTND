package blockprocessor_test

import "github.com/HoosatNetwork/HTND/v2/domain/consensus/model/testapi"

// setPruningListGate moves ValidateIBDPruningListVersion for tc to version and returns a function that restores it.
//
// It has a file of its own because the way a gate is set differs between release branches, and the tests that call it
// do not.
func setPruningListGate(tc testapi.TestConsensus, version uint16) (restore func()) {
	gates := tc.HardForkGates()
	previous := gates.ValidateIBDPruningListVersion
	gates.ValidateIBDPruningListVersion = version
	return func() { gates.ValidateIBDPruningListVersion = previous }
}
