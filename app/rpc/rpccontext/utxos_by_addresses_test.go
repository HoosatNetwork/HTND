package rpccontext

import (
	"math"
	"reflect"
	"testing"
	"unsafe"
)

func TestEncodeHexStringRejectsOversizedSlice(t *testing.T) {
	var value []byte
	header := (*reflect.SliceHeader)(unsafe.Pointer(&value))
	header.Len = math.MaxInt/2 + 1
	header.Cap = header.Len

	buffer, encoded := encodeHexString(nil, value)
	if encoded != "" {
		t.Fatalf("expected empty encoding for oversized slice, got %q", encoded)
	}
	if len(buffer) != 0 {
		t.Fatalf("expected empty buffer for oversized slice, got length %d", len(buffer))
	}
}
