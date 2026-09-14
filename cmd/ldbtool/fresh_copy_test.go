package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCheckFreshCopyKeepsSource pins that -fresh refuses a destination that is the source, under any spelling, or
// that contains it. Such a copy used to delete the source datadir before anything checked the paths.
func TestCheckFreshCopyKeepsSource(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "data", "db")
	if err := os.MkdirAll(src, 0o700); err != nil {
		t.Fatalf("MkdirAll: %s", err)
	}
	link := filepath.Join(root, "db-link")
	if err := os.Symlink(src, link); err != nil {
		t.Fatalf("Symlink: %s", err)
	}
	t.Chdir(filepath.Join(root, "data"))

	refused := map[string]string{
		"same path":          src,
		"trailing separator": src + string(filepath.Separator),
		"relative spelling":  "db",
		"unclean spelling":   filepath.Join(root, "data", "..", "data", "db"),
		"parent directory":   filepath.Join(root, "data"),
		"grandparent":        root,
		"symlink to source":  link,
	}
	for name, dest := range refused {
		if err := checkFreshCopyKeepsSource(src, dest); err == nil {
			t.Errorf("%s: -fresh into %q was allowed, which deletes the source", name, dest)
		}
	}

	allowed := map[string]string{
		"sibling":                    filepath.Join(root, "data", "db-copy"),
		"sibling with shared prefix": filepath.Join(root, "data", "db2"),
		"inside the source":          filepath.Join(src, "copy"),
		"elsewhere":                  filepath.Join(root, "other"),
	}
	for name, dest := range allowed {
		if err := checkFreshCopyKeepsSource(src, dest); err != nil {
			t.Errorf("%s: -fresh into %q was refused: %s", name, dest, err)
		}
	}
}
