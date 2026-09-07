package protowire

import (
	"testing"

	"github.com/HoosatNetwork/HTND/app/appmessage"
)

// TestTransactionStatusRoundTrips is the guard on the conversion that was a raw numeric cast between
// two independently declared enums.
//
// Every status must survive the trip to the wire and back unchanged. Before, appmessage's Invalid sat
// where the wire enum has ORPHAN and everything above it shifted by one, so a transaction's reported
// fate silently changed as it crossed the boundary - an orphan arriving at a wallet as accepted.
func TestTransactionStatusRoundTrips(t *testing.T) {
	all := []appmessage.TransactionStatus{
		appmessage.TransactionStatusUnknown,
		appmessage.TransactionStatusNotFound,
		appmessage.TransactionStatusPending,
		appmessage.TransactionStatusOrphan,
		appmessage.TransactionStatusAccepted,
		appmessage.TransactionStatusConfirmed,
		appmessage.TransactionStatusInvalid,
	}

	for _, status := range all {
		wire := toWireTransactionStatus(status)
		if got := fromWireTransactionStatus(wire); got != status {
			t.Errorf("%s became %s across the wire (wire value %d)", status, got, int32(wire))
		}
	}

	// Distinctness matters as much as round-tripping: two statuses collapsing onto one wire value
	// would round-trip for one of them and silently mis-report the other.
	seen := map[TransactionStatus]appmessage.TransactionStatus{}
	for _, status := range all {
		wire := toWireTransactionStatus(status)
		if previous, duplicate := seen[wire]; duplicate {
			t.Errorf("%s and %s both map to wire value %d", previous, status, int32(wire))
		}
		seen[wire] = status
	}
}

// An orphan must never be reported as accepted. That is the specific misreport the old cast
// produced, and it is the one that could make a wallet spend against a transaction that will not
// survive.
func TestOrphanIsNotReportedAsAccepted(t *testing.T) {
	wire := toWireTransactionStatus(appmessage.TransactionStatusOrphan)
	if wire == TransactionStatus_TRANSACTION_STATUS_ACCEPTED {
		t.Fatal("an orphaned transaction is being reported to clients as accepted")
	}
	if wire != TransactionStatus_TRANSACTION_STATUS_ORPHAN {
		t.Errorf("expected ORPHAN on the wire, got %d", int32(wire))
	}
}
