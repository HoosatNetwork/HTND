//go:build windows

package rlimit

func RaiseFileLimit() error {
	// Windows does not have RLIMIT_NOFILE.
	// The OS already allows a very high number of open handles,
	// so there is nothing to do.
	return nil
}
