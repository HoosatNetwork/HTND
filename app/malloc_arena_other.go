//go:build !linux || !cgo

package app

// MallocArenaMax reports the glibc malloc arena limit. glibc arenas only exist in Linux cgo builds.
func MallocArenaMax() string { return "n/a" }
