package appmessage

import "testing"

// TestStopNotifyingPruningPointUTXOSetOverrideCommands pins that the stop request and response carry their own
// commands. They returned the Notify commands, and the RPC server dispatches by command, so a request to stop these
// notifications ran the Notify handler and turned them on instead.
func TestStopNotifyingPruningPointUTXOSetOverrideCommands(t *testing.T) {
	if command := NewStopNotifyingPruningPointUTXOSetOverrideRequestMessage().Command(); command != CmdStopNotifyingPruningPointUTXOSetOverrideRequestMessage {
		t.Fatalf("stop request command is %s, want %s", command, CmdStopNotifyingPruningPointUTXOSetOverrideRequestMessage)
	}
	if command := NewStopNotifyingPruningPointUTXOSetOverrideResponseMessage().Command(); command != CmdStopNotifyingPruningPointUTXOSetOverrideResponseMessage {
		t.Fatalf("stop response command is %s, want %s", command, CmdStopNotifyingPruningPointUTXOSetOverrideResponseMessage)
	}
}
