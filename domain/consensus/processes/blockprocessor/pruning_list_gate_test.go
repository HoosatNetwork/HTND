package blockprocessor_test

import (
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/testapi"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
)

// setPruningListGate moves ValidateIBDPruningListVersion to version
// and returns a function that restores it. The gate is a package
// variable on this branch, so tc is unused.
func setPruningListGate(_ testapi.TestConsensus, version uint16) (restore func()) {
	previous := dagconfig.ValidateIBDPruningListVersion
	dagconfig.ValidateIBDPruningListVersion = version
	return func() { dagconfig.ValidateIBDPruningListVersion = previous }
}
