package binaryserializer

import (
	"testing"
	"unsafe"
)

func TestBinaryFreeList(t *testing.T) {

	expectedCapacity := 8
	expectedLength := 8

	first := Borrow()
	if cap(first) != expectedCapacity {
		t.Errorf("MsgTx.TestBinaryFreeList: Expected capacity for first %d, but got %d",
			expectedCapacity, cap(first))
	}
	if len(first) != expectedLength {
		t.Errorf("MsgTx.TestBinaryFreeList: Expected length for first %d, but got %d",
			expectedLength, len(first))
	}
	Return(first)

	// Borrow again, and check that the underlying array is re-used for second
	second := Borrow()
	if cap(second) != expectedCapacity {
		t.Errorf("TestBinaryFreeList: Expected capacity for second %d, but got %d",
			expectedCapacity, cap(second))
	}
	if len(second) != expectedLength {
		t.Errorf("TestBinaryFreeList: Expected length for second %d, but got %d",
			expectedLength, len(second))
	}

	firstArrayAddress := underlyingArrayAddress(first)
	secondArrayAddress := underlyingArrayAddress(second)

	if firstArrayAddress != secondArrayAddress {
		t.Errorf("First underlying array is at address %d and second at address %d, "+
			"which means memory was not re-used", firstArrayAddress, secondArrayAddress)
	}

	Return(second)

	// test there's no crash when channel is full because borrowed too much
	buffers := make([][]byte, maxItems+1)
	for i := range maxItems + 1 {
		buffers[i] = Borrow()
	}
	for i := range maxItems + 1 {
		Return(buffers[i])
	}
}

func underlyingArrayAddress(buf []byte) uint64 {
	return uint64(uintptr(unsafe.Pointer(unsafe.SliceData(buf))))
}
