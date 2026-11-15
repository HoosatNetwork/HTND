//go:build cgo

package app

/*
#include <stdlib.h>
#ifdef __GLIBC__
static int htndTestIsGlibc(void) { return 1; }
#else
static int htndTestIsGlibc(void) { return 0; }
#endif
*/
import "C"

import (
	"os"
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
	if C.htndTestIsGlibc() == 1 && os.Getenv("GLIBC_TUNABLES") == "" && got != "2 (set by htnd)" {
		t.Fatalf("MallocArenaMax() = %q, want %q", got, "2 (set by htnd)")
	}
}
