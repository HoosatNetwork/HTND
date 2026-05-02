package rpccontext

import (
	"math"
	"testing"
	"unsafe"
)

func TestEncodeHexStringRejectsOversizedSlice(t *testing.T) {
	var value []byte
	// Use unsafe to create a slice with a huge length/capacity (Go 1.17+)
	data := unsafe.SliceData(value)
	value = unsafe.Slice(data, math.MaxInt/2+1)

	buffer, encoded := encodeHexString(nil, value)
	if encoded != "" {
		t.Fatalf("expected empty encoding for oversized slice, got %q", encoded)
	}
	if len(buffer) != 0 {
		t.Fatalf("expected empty buffer for oversized slice, got length %d", len(buffer))
	}
}
