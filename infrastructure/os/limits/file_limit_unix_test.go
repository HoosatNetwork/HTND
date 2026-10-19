//go:build !windows && !plan9

package limits

import (
	"syscall"
	"testing"
)

func TestFileLimit(t *testing.T) {
	var expected syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &expected); err != nil {
		t.Fatal(err)
	}
	got, err := FileLimit()
	if err != nil {
		t.Fatal(err)
	}
	if got != uint64(expected.Cur) {
		t.Fatalf("FileLimit = %d, want soft limit %d", got, expected.Cur)
	}
}
