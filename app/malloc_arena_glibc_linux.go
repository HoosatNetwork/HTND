//go:build cgo

package app

/*
#ifdef __GLIBC__
static int htndIsGlibc(void) { return 1; }
#else
static int htndIsGlibc(void) { return 0; }
#endif
*/
import "C"

func isGlibc() bool { return C.htndIsGlibc() == 1 }
