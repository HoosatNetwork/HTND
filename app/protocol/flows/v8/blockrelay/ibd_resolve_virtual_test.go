package blockrelay

import (
	stderrors "errors"
	"testing"

	"github.com/HoosatNetwork/HTND/app/protocol/protocolerrors"
	"github.com/HoosatNetwork/HTND/domain/consensus/ruleerrors"
)

// A rule error out of ResolveVirtual is a verdict on blocks this node had already validated and
// stored - its own statuses and UTXO state - not on anything the peer sent in this round, so it ends
// the IBD round without a ban.
func TestWrapResolveVirtualErrorRuleError(t *testing.T) {
	err := wrapResolveVirtualError(ruleerrors.ErrBadMerkleRoot)

	var protocolErr protocolerrors.ProtocolError
	if !stderrors.As(err, &protocolErr) {
		t.Fatalf("expected ProtocolError, got %T", err)
	}
	if protocolErr.ShouldBan {
		t.Fatalf("expected a rule error from resolving local state not to ban the peer")
	}
}

func TestWrapResolveVirtualErrorUnexpectedError(t *testing.T) {
	err := wrapResolveVirtualError(stderrors.New("diffFrom: outpoint both in this.toAdd, other.toAdd, and only one of this.toRemove and other.toRemove"))

	var protocolErr protocolerrors.ProtocolError
	if !stderrors.As(err, &protocolErr) {
		t.Fatalf("expected ProtocolError, got %T", err)
	}
	if protocolErr.ShouldBan {
		t.Fatalf("expected unexpected resolve virtual error to be non-bannable")
	}
}
