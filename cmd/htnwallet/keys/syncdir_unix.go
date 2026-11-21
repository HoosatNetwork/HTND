//go:build !windows

package keys

import "os"

// Open before replacing the keys file so an open failure leaves the old file in place.
func openDirectoryForSync(dir string) (syncDirectory, error) {
	return os.Open(dir)
}
