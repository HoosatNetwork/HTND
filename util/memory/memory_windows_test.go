//go:build windows

package memory

import (
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

const windowsMemFree = 0x10000

func TestFreeReleasesWindowsPages(t *testing.T) {
	b := Malloc[byte](4096)
	if b == nil {
		t.Fatal("expected block, got nil")
	}

	base := uintptr(unsafe.Pointer(unsafe.SliceData(b.mem)))
	var before windows.MemoryBasicInformation
	err := windows.VirtualQuery(base, &before, unsafe.Sizeof(before))
	if err != nil {
		t.Fatalf("VirtualQuery before free: %v", err)
	}
	if before.State != windows.MEM_COMMIT {
		t.Fatalf("expected committed pages before free, got state %#x", before.State)
	}

	allocationBase := before.AllocationBase
	if allocationBase == 0 {
		t.Fatal("expected non-zero allocation base")
	}

	Free(b)

	var after windows.MemoryBasicInformation
	err = windows.VirtualQuery(allocationBase, &after, unsafe.Sizeof(after))
	if err != nil {
		t.Fatalf("VirtualQuery after free: %v", err)
	}
	if after.State != windowsMemFree {
		t.Fatalf("expected MEM_FREE after free, got state %#x", after.State)
	}
	if b.mem != nil || b.ptr != nil || b.len != 0 {
		t.Fatal("expected block to be zeroed after free")
	}
}
