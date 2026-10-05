package keys

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
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

// TestAFailedSaveLeavesTheExistingKeysFileIntact is the crash-safety property HTN-153 exists for,
// and the one that actually protects funds: a save that does not complete must leave the previous
// keys file exactly as it was.
//
// The pre-HTN-153 Save encoded straight over the live file, so an interruption anywhere in the
// write - a crash, a full disk, a killed process - left the seed file truncated or half-rewritten,
// with no copy of the original anywhere. Writing to a temp file and renaming means the destination
// is only ever replaced by a complete file, in one atomic step.
//
// The failure is injected by making the directory unwritable, so os.CreateTemp fails. That is the
// earliest point Save can fail: even a save that got nowhere must not have touched the original.
// TestASaveFailingAtTheRenameLeavesTheExistingKeysFileIntact covers the latest point.
func TestAFailedSaveLeavesTheExistingKeysFileIntact(t *testing.T) {
	if runtime.GOOS == "windows" {
		// Chmod on a Windows directory only sets its read-only attribute, which does not stop files
		// being created in it, so there is no failure to inject this way.
		t.Skip("directory permissions do not prevent creating files on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root: directory permissions would not prevent the write")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "keys.json")
	before := seedKeysFile(t, path)

	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("making the directory read-only: %+v", err)
	}
	// Restore permissions no matter how this test exits, or t.TempDir's cleanup fails too.
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	replacement := newTestFile(t, path, 2)
	if err := replacement.Save(); err == nil {
		t.Fatal("Save reported success although the directory is not writable")
	}

	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("restoring directory permissions: %+v", err)
	}

	requireKeysFileUnchanged(t, path, before)
}

// TestASaveFailingAtTheRenameLeavesTheExistingKeysFileIntact fails Save at its last step, the rename
// into place, after the replacement has been written out and synced in full. Nothing short of the
// rename may touch the original, and the finished temp file must not be left behind either. Unlike
// the directory-permissions test above, this injection works on every platform.
func TestASaveFailingAtTheRenameLeavesTheExistingKeysFileIntact(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "keys.json")
	before := seedKeysFile(t, path)

	injected := errors.New("injected rename failure")
	renameFile = func(string, string) error { return injected }
	t.Cleanup(func() { renameFile = os.Rename })

	replacement := newTestFile(t, path, 2)
	if err := replacement.Save(); !errors.Is(err, injected) {
		t.Fatalf("Save returned %v, want the injected rename failure", err)
	}

	requireKeysFileUnchanged(t, path, before)

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %+v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "keys.json" {
		t.Fatalf("a failed save left files behind: expected exactly [keys.json] in %s, got %v", dir, entries)
	}
}

// seedKeysFile saves a keys file with MinimumSignatures 1 at path and returns its bytes.
func seedKeysFile(t *testing.T, path string) []byte {
	t.Helper()
	original := newTestFile(t, path, 1)
	if err := original.Save(); err != nil {
		t.Fatalf("seeding the original keys file: %+v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the original keys file: %+v", err)
	}
	return before
}

// requireKeysFileUnchanged checks that the keys file at path still holds exactly before, and still
// reads as the seeded file.
func requireKeysFileUnchanged(t *testing.T, path string, before []byte) {
	t.Helper()
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the keys file is unreadable after a failed save: %+v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("a failed save modified the existing keys file.\nbefore: %s\nafter:  %s", before, after)
	}

	// Intact is not enough - it has to still be usable.
	read, err := ReadKeysFile(&dagconfig.MainnetParams, path)
	if err != nil {
		t.Fatalf("the keys file no longer parses after a failed save: %+v", err)
	}
	if read.MinimumSignatures != 1 {
		t.Fatalf("the failed save's content leaked into the file: MinimumSignatures is %d, want 1",
			read.MinimumSignatures)
	}
}

// TestALeftoverTempFileDoesNotBreakReadingOrSaving covers the state a real crash leaves behind: the
// temp file from the interrupted save is still sitting in the directory. It must not be mistaken
// for the keys file, and it must not stop the next save from succeeding.
func TestALeftoverTempFileDoesNotBreakReadingOrSaving(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "keys.json")

	file := newTestFile(t, path, 1)
	if err := file.Save(); err != nil {
		t.Fatalf("Save: %+v", err)
	}

	// What a crash between CreateTemp and Rename leaves: a partial, unparseable temp file.
	leftover := filepath.Join(dir, "keys.json.tmp-123456")
	if err := os.WriteFile(leftover, []byte(`{"Version":4,"Encrypted`), 0o600); err != nil {
		t.Fatalf("writing the leftover temp file: %+v", err)
	}

	if _, err := ReadKeysFile(&dagconfig.MainnetParams, path); err != nil {
		t.Fatalf("a leftover temp file broke reading the real keys file: %+v", err)
	}

	next := newTestFile(t, path, 2)
	if err := next.Save(); err != nil {
		t.Fatalf("a leftover temp file broke the next save: %+v", err)
	}
	read, err := ReadKeysFile(&dagconfig.MainnetParams, path)
	if err != nil {
		t.Fatalf("ReadKeysFile after the next save: %+v", err)
	}
	if read.MinimumSignatures != 2 {
		t.Fatalf("the next save did not take effect: MinimumSignatures is %d, want 2",
			read.MinimumSignatures)
	}
}
