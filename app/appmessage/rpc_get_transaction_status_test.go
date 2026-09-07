package appmessage

import "testing"

// TestTransactionStatusValuesMatchTheWireEnum pins the values against protowire.TransactionStatus.
//
// The two enums were declared independently and converted with a numeric cast, which is correct only
// while they happen to agree - and they did not. Invalid sat at 3, where the wire enum has ORPHAN, so
// every status from 3 upward was sent one place out of step: an invalid transaction was reported as
// an orphan, an orphan as accepted, an accepted as confirmed. A wallet asking whether it could spend
// an output was told "accepted" about a transaction that had been orphaned.
//
// The numbers are asserted literally rather than derived, because deriving them from the same
// constants they are meant to check would test nothing.
func TestTransactionStatusValuesMatchTheWireEnum(t *testing.T) {
	expected := map[TransactionStatus]struct {
		wireValue int
		name      string
	}{
		TransactionStatusUnknown:   {0, "unknown"},
		TransactionStatusNotFound:  {1, "not-found"},
		TransactionStatusPending:   {2, "pending"},
		TransactionStatusOrphan:    {3, "orphan"},
		TransactionStatusAccepted:  {4, "accepted"},
		TransactionStatusConfirmed: {5, "confirmed"},
		TransactionStatusInvalid:   {6, "invalid"},
	}

	for status, want := range expected {
		if int(status) != want.wireValue {
			t.Errorf("%s has value %d but the wire enum uses %d - a client would be told the wrong "+
				"status", want.name, int(status), want.wireValue)
		}
		if status.String() != want.name {
			t.Errorf("value %d renders as %q, expected %q", int(status), status.String(), want.name)
		}
	}
}

// Every status must render as itself. Invalid was missing from the map entirely and rendered as
// "unknown", so the one status that says "do not rely on this transaction" was indistinguishable
// from the one that says "no idea".
func TestEveryTransactionStatusHasItsOwnName(t *testing.T) {
	seen := map[string]TransactionStatus{}
	for status := TransactionStatusUnknown; status <= TransactionStatusInvalid; status++ {
		name := status.String()
		if name == "unknown" && status != TransactionStatusUnknown {
			t.Errorf("status %d renders as \"unknown\" - it has no name of its own", int(status))
		}
		if previous, duplicate := seen[name]; duplicate {
			t.Errorf("status %d and %d both render as %q", int(previous), int(status), name)
		}
		seen[name] = status
	}
}
