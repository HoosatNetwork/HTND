package rpchandlers

import (
	"math"
	"testing"
	"unsafe"
)

func TestEncodeHexStringRejectsOversizedSlice(t *testing.T) {
	// Use unsafe to create a slice with a huge length/capacity without
	// allocating that much memory. The slice must not be dereferenced.
	dummy := byte(0)
	value := unsafe.Slice(&dummy, math.MaxInt/2+1)

	buffer, encoded := encodeHexString(nil, value)
	if encoded != "" {
		t.Fatalf("expected empty encoding for oversized slice, got %q", encoded)
	}
	if len(buffer) != 0 {
		t.Fatalf("expected empty buffer for oversized slice, got length %d", len(buffer))
	}
}
