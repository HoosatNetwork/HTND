package grpcserver

import "testing"

// restoreMessageSizes puts the package-level ceilings back after a test. They are process-global, so
// these tests must not run in parallel with each other.
func restoreMessageSizes(t *testing.T) {
	t.Helper()
	previousP2P, previousRPC := p2pMaxMessageSize, RPCMaxMessageSize
	t.Cleanup(func() {
		p2pMaxMessageSize = previousP2P
		RPCMaxMessageSize = previousRPC
	})
}

// TestZeroLeavesTheBuiltInDefaults is the property that makes HTN-166's change safe to ship: with no
// flags set - which is every existing deployment - the ceilings are exactly what they were.
//
// The defaults are deliberately not lowered. A P2P ceiling below the largest legitimate message
// rejects honest peers and stalls IBD, and nobody has measured what that maximum actually is on this
// chain, so a smaller number picked here would be a guess with a network-split failure mode.
func TestZeroLeavesTheBuiltInDefaults(t *testing.T) {
	restoreMessageSizes(t)

	// Pretend something already changed them, so a no-op would be visible.
	p2pMaxMessageSize = 123
	RPCMaxMessageSize = 456

	if err := SetMaxMessageSizes(0, 0); err != nil {
		t.Fatalf("SetMaxMessageSizes(0, 0): %+v", err)
	}
	if p2pMaxMessageSize != 123 || RPCMaxMessageSize != 456 {
		t.Fatalf("zero overwrote an existing value: p2p %d, rpc %d", p2pMaxMessageSize, RPCMaxMessageSize)
	}

	// And from a clean start, zero must leave the built-in constants in place.
	p2pMaxMessageSize = DefaultP2PMaxMessageSize
	RPCMaxMessageSize = DefaultRPCMaxMessageSize
	if err := SetMaxMessageSizes(0, 0); err != nil {
		t.Fatalf("SetMaxMessageSizes(0, 0): %+v", err)
	}
	if p2pMaxMessageSize != DefaultP2PMaxMessageSize {
		t.Errorf("P2P ceiling is %d, want the default %d", p2pMaxMessageSize, DefaultP2PMaxMessageSize)
	}
	if RPCMaxMessageSize != DefaultRPCMaxMessageSize {
		t.Errorf("RPC ceiling is %d, want the default %d", RPCMaxMessageSize, DefaultRPCMaxMessageSize)
	}
}

func TestOverridesApplyIndependently(t *testing.T) {
	restoreMessageSizes(t)

	p2pMaxMessageSize = DefaultP2PMaxMessageSize
	RPCMaxMessageSize = DefaultRPCMaxMessageSize

	const rpcOverride = 64 * 1024 * 1024
	if err := SetMaxMessageSizes(0, rpcOverride); err != nil {
		t.Fatalf("SetMaxMessageSizes: %+v", err)
	}
	if RPCMaxMessageSize != rpcOverride {
		t.Errorf("RPC ceiling is %d, want %d", RPCMaxMessageSize, rpcOverride)
	}
	if p2pMaxMessageSize != DefaultP2PMaxMessageSize {
		t.Errorf("setting only the RPC ceiling changed the P2P one to %d", p2pMaxMessageSize)
	}

	const p2pOverride = 512 * 1024 * 1024
	if err := SetMaxMessageSizes(p2pOverride, 0); err != nil {
		t.Fatalf("SetMaxMessageSizes: %+v", err)
	}
	if p2pMaxMessageSize != p2pOverride {
		t.Errorf("P2P ceiling is %d, want %d", p2pMaxMessageSize, p2pOverride)
	}
	if RPCMaxMessageSize != rpcOverride {
		t.Errorf("setting only the P2P ceiling changed the RPC one to %d", RPCMaxMessageSize)
	}
}

// TestNegativeIsRejected - a negative limit would reach gRPC and mean something unintended there.
// Failing at startup is better than a node that silently accepts nothing.
func TestNegativeIsRejected(t *testing.T) {
	restoreMessageSizes(t)

	if err := SetMaxMessageSizes(-1, 0); err == nil {
		t.Error("a negative P2P limit was accepted")
	}
	if err := SetMaxMessageSizes(0, -1); err == nil {
		t.Error("a negative RPC limit was accepted")
	}
}

// TestASmallLimitIsAllowedButWarned pins that the sanity threshold is advisory. An operator who has
// measured their traffic may have a good reason for a small ceiling, and a hard floor invented here
// would be the same kind of guess the defaults deliberately avoid.
func TestASmallLimitIsAllowedButWarned(t *testing.T) {
	restoreMessageSizes(t)

	small := minimumSaneMaxMessageSize / 2
	if err := SetMaxMessageSizes(small, small); err != nil {
		t.Fatalf("a small but positive limit was rejected: %+v", err)
	}
	if p2pMaxMessageSize != small || RPCMaxMessageSize != small {
		t.Fatalf("small limits were not applied: p2p %d, rpc %d", p2pMaxMessageSize, RPCMaxMessageSize)
	}
}
