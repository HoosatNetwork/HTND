//go:build cgo

package app

import (
	"os"
	"strings"
	"testing"
)

func TestMallocArenaMax(t *testing.T) {
	got := MallocArenaMax()
	if v, ok := os.LookupEnv("MALLOC_ARENA_MAX"); ok {
		if want := v + " (environment)"; got != want {
			t.Fatalf("MallocArenaMax() = %q with MALLOC_ARENA_MAX=%s, want %q", got, v, want)
		}
		return
	}
	if isGlibc() && !strings.Contains(os.Getenv("GLIBC_TUNABLES"), "arena_max") && got != "2 (set by htnd)" {
		t.Fatalf("MallocArenaMax() = %q, want %q", got, "2 (set by htnd)")
	}
}
