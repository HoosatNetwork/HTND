package consensusstatemanager

import (
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/constants"
)

// TestAcceptedIDMerkleRootOrderFollowsTheBlocksOwnVersion pins that accepted transactions are sorted by ID for the
// accepted-ID merkle root exactly when the block's own version is below 5. The choice read the process-global block
// version, so the same block's root was computed in two different orders depending on the node's uptime or IBD.
func TestAcceptedIDMerkleRootOrderFollowsTheBlocksOwnVersion(t *testing.T) {
	defer constants.ForceSetBlockVersion(1)

	first := &externalapi.DomainTransaction{Payload: []byte{1}}
	second := &externalapi.DomainTransaction{Payload: []byte{2}}
	acceptanceData := func(order ...*externalapi.DomainTransaction) externalapi.AcceptanceData {
		data := &externalapi.BlockAcceptanceData{}
		for _, transaction := range order {
			data.TransactionAcceptanceData = append(data.TransactionAcceptanceData,
				&externalapi.TransactionAcceptanceData{Transaction: transaction, IsAccepted: true})
		}
		return externalapi.AcceptanceData{data}
	}
	forward, reversed := acceptanceData(first, second), acceptanceData(second, first)

	for _, globalVersion := range []uint{1, 9} {
		constants.ForceSetBlockVersion(globalVersion)
		// Version 1 sorts, so both arrival orders give the same root.
		if !calculateAcceptedIDMerkleRoot(forward, 1).Equal(calculateAcceptedIDMerkleRoot(reversed, 1)) {
			t.Errorf("global version %d: a version-1 root depended on acceptance order", globalVersion)
		}
		// Version 5 keeps acceptance order, so the two orders give different roots.
		if calculateAcceptedIDMerkleRoot(forward, 5).Equal(calculateAcceptedIDMerkleRoot(reversed, 5)) {
			t.Errorf("global version %d: a version-5 root was sorted", globalVersion)
		}
	}
}
