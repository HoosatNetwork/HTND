package autoupdate

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestInstallBinaryKeepsCurrentBinaryOnFailure pins that a failed install leaves the current binary in place.
// installBinary removed the current binary before copying the new one, so any failure after that point - here
// an unreadable replacement, in practice a full disk or a crash mid-copy - left the node without a binary.
func TestInstallBinaryKeepsCurrentBinaryOnFailure(t *testing.T) {
	dir := t.TempDir()
	current := filepath.Join(dir, "htnd")
	if err := os.WriteFile(current, []byte("current"), 0o755); err != nil {
		t.Fatalf("WriteFile: %+v", err)
	}

	if err := installBinary(filepath.Join(dir, "missing-replacement"), current); err == nil {
		t.Fatalf("expected installing a missing replacement to fail")
	}

	content, err := os.ReadFile(current)
	if err != nil {
		t.Fatalf("the current binary is gone after a failed install: %+v", err)
	}
	if string(content) != "current" {
		t.Fatalf("the current binary was changed by a failed install: %q", content)
	}
}

// TestInstallBinaryReplacesCurrentBinary pins that a successful install replaces the binary and leaves no
// temporary files behind.
func TestInstallBinaryReplacesCurrentBinary(t *testing.T) {
	dir := t.TempDir()
	current := filepath.Join(dir, "htnd")
	replacement := filepath.Join(t.TempDir(), "htnd")
	if err := os.WriteFile(current, []byte("current"), 0o755); err != nil {
		t.Fatalf("WriteFile: %+v", err)
	}
	if err := os.WriteFile(replacement, []byte("replacement"), 0o644); err != nil {
		t.Fatalf("WriteFile: %+v", err)
	}

	if err := installBinary(replacement, current); err != nil {
		t.Fatalf("installBinary: %+v", err)
	}

	content, err := os.ReadFile(current)
	if err != nil {
		t.Fatalf("ReadFile: %+v", err)
	}
	if string(content) != "replacement" {
		t.Fatalf("installed binary content %q, want %q", content, "replacement")
	}
	info, err := os.Stat(current)
	if err != nil {
		t.Fatalf("Stat: %+v", err)
	}
	// Windows has no execute permission bits: Chmod only toggles read-only, and a writable file always reports
	// -rw-rw-rw-, so the bits can only be checked elsewhere.
	if runtime.GOOS != "windows" && info.Mode()&0o111 == 0 {
		t.Fatalf("installed binary is not executable: %v", info.Mode())
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %+v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected only the installed binary in %s, found %d entries", dir, len(entries))
	}
}
