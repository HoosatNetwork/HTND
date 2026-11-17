//go:build cgo

package app

/*
#include <stdlib.h>
#include <string.h>
#ifdef __GLIBC__
#include <malloc.h>
#endif

// The limit the constructor below set, or 0 if it left the allocator's default alone.
static int htndMallocArenaMax = 0;

// glibc gives every thread that mallocs its own arena, up to 8 per core. Pebble allocates its block
// cache with C.calloc from whichever thread runs the read, so without a limit the cache is spread over
// dozens of 64 MiB arena heaps that glibc can only trim from the top, and RSS outside the Go heap
// grows with the thread count. This runs from the C start-up code, before the Go runtime starts any
// thread, so only the main arena exists yet and the limit applies to all later ones.
__attribute__((constructor)) static void htndLimitMallocArenas(void) {
#ifdef __GLIBC__
	const char *tunables = getenv("GLIBC_TUNABLES");
	if (getenv("MALLOC_ARENA_MAX") != NULL || (tunables != NULL && strstr(tunables, "arena_max") != NULL)) {
		return; // the operator chose a value and glibc has applied it already
	}
	if (mallopt(M_ARENA_MAX, 2) == 1) {
		htndMallocArenaMax = 2;
	}
#endif
}

static int htndGetMallocArenaMax(void) { return htndMallocArenaMax; }
*/
import "C"

import (
	"os"
	"strconv"
	"strings"
)

// MallocArenaMax reports the glibc malloc arena limit htnd runs with, for the startup log.
func MallocArenaMax() string {
	if n := C.htndGetMallocArenaMax(); n > 0 {
		return strconv.Itoa(int(n)) + " (set by htnd)"
	}
	if v, ok := os.LookupEnv("MALLOC_ARENA_MAX"); ok {
		return v + " (environment)"
	}
	if strings.Contains(os.Getenv("GLIBC_TUNABLES"), "arena_max") {
		return "set in GLIBC_TUNABLES"
	}
	return "allocator default"
}
