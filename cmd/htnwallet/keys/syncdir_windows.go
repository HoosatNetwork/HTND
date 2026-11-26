//go:build windows

package keys

// Windows cannot sync an open directory handle. The replacement still uses a single atomic rename.
func openDirectoryForSync(_ string) (syncDirectory, error) {
	return noDirectorySync{}, nil
}

type noDirectorySync struct{}

func (noDirectorySync) Sync() error  { return nil }
func (noDirectorySync) Close() error { return nil }
