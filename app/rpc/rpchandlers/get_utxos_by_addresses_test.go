package rpchandlers

import (
	"testing"
)

func TestEncodeHexStringRejectsOversizedSlice(t *testing.T) {
	// We want to test the oversized-input fast-path without attempting
	// to construct a gigantic slice (which would either OOM or panic
	// under checkptr when using unsafe tricks).
	value := make([]byte, 11)

	buffer, encoded := encodeHexStringWithMaxValueLen(nil, value, 10)
	if encoded != "" {
		t.Fatalf("expected empty encoding for oversized slice, got %q", encoded)
	}
	if len(buffer) != 0 {
		t.Fatalf("expected empty buffer for oversized slice, got length %d", len(buffer))
	}
}
