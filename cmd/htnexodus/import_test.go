package main

import (
	"strings"
	"testing"
)

// TestRunImportRequiresForce confirms the mandatory safety gate: `import` must refuse to run
// without --force, regardless of other flags, since it is the one command in this tool that
// mutates the node's own consensus state.
func TestRunImportRequiresForce(t *testing.T) {
	err := runImport([]string{"--bundle", "/nonexistent", "--db-path", "/nonexistent"})
	if err == nil {
		t.Fatalf("expected an error when --force is omitted, got nil")
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Fatalf("expected an error mentioning --force, got: %+v", err)
	}
}

// TestRunImportRequiresBundleAndDBPath confirms the other required flags are validated before
// any bundle/database I/O is attempted.
func TestRunImportRequiresBundleAndDBPath(t *testing.T) {
	err := runImport([]string{"--force", "--db-path", "/nonexistent"})
	if err == nil || !strings.Contains(err.Error(), "--bundle") {
		t.Fatalf("expected a --bundle required error, got: %+v", err)
	}

	err = runImport([]string{"--force", "--bundle", "/nonexistent"})
	if err == nil || !strings.Contains(err.Error(), "--db-path") {
		t.Fatalf("expected a --db-path required error, got: %+v", err)
	}
}
