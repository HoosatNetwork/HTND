package ldb

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/HoosatNetwork/HTND/infrastructure/db/database"
)

func writeKV(t *testing.T, db *LevelDB, m map[string]string) {
	t.Helper()
	root := database.MakeBucket(nil)
	for k, v := range m {
		if err := db.Put(root.Key([]byte(k)), []byte(v)); err != nil {
			t.Fatalf("put %s: %v", k, err)
		}
	}
}

func readVal(t *testing.T, db *LevelDB, key string) string {
	t.Helper()
	root := database.MakeBucket(nil)
	b, err := db.Get(root.Key([]byte(key)))
	if err != nil {
		t.Fatalf("get %s: %v", key, err)
	}
	return string(b)
}

func TestFuseOverwrite(t *testing.T) {
	dir := t.TempDir()
	src1Path := filepath.Join(dir, "src1")
	src2Path := filepath.Join(dir, "src2")
	destPath := filepath.Join(dir, "dest")

	src1, err := NewLevelDB(src1Path, 64)
	if err != nil {
		t.Fatal(err)
	}
	src2, err := NewLevelDB(src2Path, 64)
	if err != nil {
		t.Fatal(err)
	}

	writeKV(t, src1, map[string]string{"a": "1", "b": "1"})
	writeKV(t, src2, map[string]string{"b": "2", "c": "2"})

	_ = src1.Close()
	_ = src2.Close()

	if err := FuseLevelDB(destPath, []string{src1Path, src2Path}, FuseOptions{Strategy: Overwrite, CacheSizeMiB: 64, BatchSize: 10}); err != nil {
		t.Fatalf("fuse overwrite: %v", err)
	}

	dest, err := NewLevelDB(destPath, 64)
	if err != nil {
		t.Fatal(err)
	}
	defer dest.Close()

	if got := readVal(t, dest, "a"); got != "1" {
		t.Fatalf("a=%s want 1", got)
	}
	if got := readVal(t, dest, "b"); got != "2" {
		t.Fatalf("b=%s want 2", got)
	}
	if got := readVal(t, dest, "c"); got != "2" {
		t.Fatalf("c=%s want 2", got)
	}

	// sanity: underlying directories exist
	if _, err := os.Stat(destPath); err != nil {
		t.Fatalf("dest missing: %v", err)
	}
}

func TestFuseKeepExisting(t *testing.T) {
	dir := t.TempDir()
	src1Path := filepath.Join(dir, "src1")
	src2Path := filepath.Join(dir, "src2")
	destPath := filepath.Join(dir, "dest")

	// Preseed dest with b=dest
	dest, err := NewLevelDB(destPath, 64)
	if err != nil {
		t.Fatal(err)
	}
	writeKV(t, dest, map[string]string{"b": "dest"})
	_ = dest.Close()

	src1, err := NewLevelDB(src1Path, 64)
	if err != nil {
		t.Fatal(err)
	}
	src2, err := NewLevelDB(src2Path, 64)
	if err != nil {
		t.Fatal(err)
	}

	writeKV(t, src1, map[string]string{"a": "1", "b": "1"})
	writeKV(t, src2, map[string]string{"b": "2", "c": "2"})
	_ = src1.Close()
	_ = src2.Close()

	if err := FuseLevelDB(destPath, []string{src1Path, src2Path}, FuseOptions{Strategy: KeepExisting, CacheSizeMiB: 64, BatchSize: 5}); err != nil {
		t.Fatalf("fuse keep-existing: %v", err)
	}

	dest2, err := NewLevelDB(destPath, 64)
	if err != nil {
		t.Fatal(err)
	}
	defer dest2.Close()

	if got := readVal(t, dest2, "a"); got != "1" {
		t.Fatalf("a=%s want 1", got)
	}
	if got := readVal(t, dest2, "b"); got != "dest" {
		t.Fatalf("b=%s want dest", got)
	}
	if got := readVal(t, dest2, "c"); got != "2" {
		t.Fatalf("c=%s want 2", got)
	}
}
