package ping

import (
	"errors"
	"strings"
	"testing"

	"github.com/HoosatNetwork/HTND/app/appmessage"
	"github.com/HoosatNetwork/HTND/app/protocol/protocolerrors"
)

func TestUnwrapPongMessageRejectsUnexpectedMessage(t *testing.T) {
	erroneousMessage := &appmessage.MsgVersion{}

	_, err := unwrapPongMessage(erroneousMessage)
	if err == nil {
		t.Fatal("expected error for unexpected message type")
	}

	var protocolErr protocolerrors.ProtocolError
	if !errors.As(err, &protocolErr) {
		t.Fatalf("expected protocol error, got %T", err)
	}
	if !protocolErr.ShouldBan {
		t.Fatal("expected unexpected pong route message to be bannable")
	}
	if !strings.Contains(err.Error(), "expected: Pong") || !strings.Contains(err.Error(), "got: Version") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestUnwrapPingMessageRejectsUnexpectedMessage(t *testing.T) {
	erroneousMessage := &appmessage.MsgVersion{}

	_, err := unwrapPingMessage(erroneousMessage)
	if err == nil {
		t.Fatal("expected error for unexpected message type")
	}

	var protocolErr protocolerrors.ProtocolError
	if !errors.As(err, &protocolErr) {
		t.Fatalf("expected protocol error, got %T", err)
	}
	if !protocolErr.ShouldBan {
		t.Fatal("expected unexpected ping route message to be bannable")
	}
	if !strings.Contains(err.Error(), "expected: Ping") || !strings.Contains(err.Error(), "got: Version") {
		t.Fatalf("unexpected error: %v", err)
	}
}
