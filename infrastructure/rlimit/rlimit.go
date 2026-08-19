package rlimit

import (
	"runtime"
	"syscall"
)

func RaiseFileLimit() error {
	if runtime.GOOS == "windows" {
		return nil
	}

	var rLimit syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rLimit); err != nil {
		return err
	}
	rLimit.Cur = rLimit.Max
	return syscall.Setrlimit(syscall.RLIMIT_NOFILE, &rLimit)
}
