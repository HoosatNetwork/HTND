package keys

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/HoosatNetwork/HTND/domain/dagconfig"
)

func newTestFile(t *testing.T, path string, minSignatures uint32) *File {
	t.Helper()
	return &File{
		Version:            LastVersion,
		NumThreads:         defaultNumThreads,
		EncryptedMnemonics: []*EncryptedMnemonic{{cipher: []byte{1, 2, 3}, salt: []byte{4, 5, 6}}},
		ExtendedPublicKeys: []string{"xpub-test"},
		MinimumSignatures:  minSignatures,
		path:               path,
	}
}

// TestSaveRoundTrips pins the ordinary case: what Save writes, ReadKeysFile reads back identically.
func TestSaveRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.json")
	file := newTestFile(t, path, 1)

	if err := file.Save(); err != nil {
		t.Fatalf("Save: %+v", err)
	}

	read, err := ReadKeysFile(&dagconfig.MainnetParams, path)
	if err != nil {
		t.Fatalf("ReadKeysFile: %+v", err)
	}
	if read.MinimumSignatures != 1 || len(read.ExtendedPublicKeys) != 1 || read.ExtendedPublicKeys[0] != "xpub-test" {
		t.Fatalf("round-tripped file does not match what was saved: %+v", read)
	}
}

// TestSaveLeavesNoTempFileBehind pins that a successful Save cleans up after itself: only the real
// path exists afterward, no leftover ".tmp-*" file from the atomic-write staging step.
func TestSaveLeavesNoTempFileBehind(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "keys.json")
	file := newTestFile(t, path, 1)

	if err := file.Save(); err != nil {
		t.Fatalf("Save: %+v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %+v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "keys.json" {
		t.Fatalf("expected exactly [keys.json] in %s, got %v", dir, entries)
	}
}

// TestSaveOverwritingALongerFileLeavesNoTrailingBytes is HTN-153's regression test.
//
// Save used to open with O_WRONLY|O_CREATE (no O_TRUNC) and encode directly over the existing file's
// bytes. Encoding shorter content than what was already there left the old file's tail in place after
// the new JSON value - here reproduced by seeding the path with a long pre-existing file before Save
// writes much shorter content. The write-to-temp-then-rename approach can't have this failure mode:
// the destination is always a brand new file.
func TestSaveOverwritingALongerFileLeavesNoTrailingBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.json")
	longPadding := make([]byte, 4096)
	for i := range longPadding {
		longPadding[i] = ' '
	}
	if err := os.WriteFile(path, append([]byte(`{"padding":"`), append(longPadding, []byte(`"}`)...)...), 0o600); err != nil {
		t.Fatalf("seed WriteFile: %+v", err)
	}

	file := newTestFile(t, path, 1)
	if err := file.Save(); err != nil {
		t.Fatalf("Save: %+v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %+v", err)
	}
	if len(raw) > 4096 {
		t.Fatalf("saved file is %d bytes, suspiciously close to or larger than the seeded padding - "+
			"the old file's tail was not replaced", len(raw))
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("saved file is not valid standalone JSON (a leftover tail would still parse with "+
			"json.Decoder.Decode, which stops at the first value, but not with json.Unmarshal): %s", err)
	}
}
