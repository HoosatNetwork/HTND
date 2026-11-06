package rejects

import (
	"strings"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
	"github.com/HoosatNetwork/HTND/v2/app/protocol/protocolerrors"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter/router"
	"github.com/pkg/errors"
)

func handleReject(t *testing.T, reason string) string {
	route := router.NewRouteWithCapacity("reject", 1)
	defer route.Close()
	err := route.Enqueue(appmessage.NewMsgReject(reason))
	if err != nil {
		t.Fatalf("Enqueue: %s", err)
	}
	err = HandleRejects(nil, route, nil)
	var protocolErr protocolerrors.ProtocolError
	if !errors.As(err, &protocolErr) {
		t.Fatalf("expected a protocol error for a reject message, got %v", err)
	}
	if protocolErr.ShouldBan {
		t.Fatalf("a reject message must not lead to a ban")
	}
	return err.Error()
}

func TestHandleRejectsBoundsReason(t *testing.T) {
	const maxReasonLength = 2048
	wrap := func(reason string) string { return "got reject message: `" + reason + "`" }

	tests := []struct {
		name, reason, kept string
	}{
		{name: "short", reason: "some reason", kept: "some reason"},
		{name: "exactly at the limit", reason: strings.Repeat("x", maxReasonLength), kept: strings.Repeat("x", maxReasonLength)},
		{name: "one byte over", reason: strings.Repeat("x", maxReasonLength+1), kept: strings.Repeat("x", maxReasonLength)},
		// 3-byte characters: the limit falls inside the 683rd one, so 682 whole characters are kept.
		{name: "long, limit inside a character", reason: strings.Repeat("€", 1<<20), kept: strings.Repeat("€", 682)},
	}
	for _, test := range tests {
		got := handleReject(t, test.reason)
		if got != wrap(test.kept) {
			t.Errorf("%s: error keeps %d bytes of a %d-byte reason, want %d", test.name,
				len(got), len(test.reason), len(wrap(test.kept)))
		}
	}
}
