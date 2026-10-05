//go:build windows

package memory

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

func sysAlloc(size int) ([]byte, error) {
	ptr, err := windows.VirtualAlloc(0, uintptr(size), windows.MEM_COMMIT|windows.MEM_RESERVE, windows.PAGE_READWRITE)
	if err != nil {
		return nil, err
	}
	return unsafe.Slice((*byte)(unsafe.Pointer(ptr)), size), nil
}

func sysFree(b []byte) error {
	if len(b) == 0 {
		return nil
	}
	return windows.VirtualFree(uintptr(unsafe.Pointer(&b[0])), 0, windows.MEM_RELEASE)
}
