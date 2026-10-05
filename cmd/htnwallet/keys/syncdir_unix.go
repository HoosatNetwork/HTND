//go:build !windows

package keys

import (
	"os"
)

// syncDir flushes the directory itself, so that the rename Save just performed is durable and not
// only the replacement file's contents.
func syncDir(dir string) error {
	dirFile, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer dirFile.Close()

	return dirFile.Sync()
}
