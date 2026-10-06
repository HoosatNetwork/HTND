//go:build windows

package keys

// syncDir does nothing on Windows.
// There is no way to flush a directory there: opening one and calling Sync on the handle fails with
// "Access is denied". The rename in Save is still atomic - MoveFileEx replaces the destination in a
// single step - so only the extra durability barrier is missing, not the crash-safety of the swap.
func syncDir(_ string) error {
	return nil
}
