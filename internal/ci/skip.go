package ci

import "testing"

// SkipLongTest skips a test only when the CI build tag is enabled.
func SkipLongTest(t *testing.T, reason string) {
	t.Helper()
	if SkipLongTests {
		t.Skip(reason)
	}
}
