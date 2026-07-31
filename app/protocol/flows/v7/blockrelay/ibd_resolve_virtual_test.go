package blockrelay

import (
	stderrors "errors"
	"testing"

	"github.com/HoosatNetwork/HTND/app/protocol/protocolerrors"
	"github.com/HoosatNetwork/HTND/domain/consensus/ruleerrors"
)

func TestWrapResolveVirtualErrorRuleError(t *testing.T) {
	err := wrapResolveVirtualError(ruleerrors.ErrBadMerkleRoot)

	var protocolErr protocolerrors.ProtocolError
	if !stderrors.As(err, &protocolErr) {
		t.Fatalf("expected ProtocolError, got %T", err)
	}
	if !protocolErr.ShouldBan {
		t.Fatalf("expected rule error to be bannable")
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
