package app

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCheckDatabaseVersionEmptyFile pins that an empty version file is reported as an error. The file is created
// before the version is written, so a crash or a full disk in between leaves it empty; checkDatabaseVersion used to
// index the first byte without a length check and panicked on every start instead.
func TestCheckDatabaseVersionEmptyFile(t *testing.T) {
	dbPath := t.TempDir()
	if err := os.WriteFile(versionFilePath(dbPath), nil, 0o600); err != nil {
		t.Fatalf("WriteFile: %s", err)
	}

	if err := checkDatabaseVersion(dbPath); err == nil {
		t.Fatalf("checkDatabaseVersion accepted an empty version file")
	}
}

// TestCheckDatabaseVersionCreateFailure pins that a version file that cannot be created is reported. The create
// error used to be swallowed, so the node started without ever recording the database version.
func TestCheckDatabaseVersionCreateFailure(t *testing.T) {
	dbPath := t.TempDir()
	// A directory where the version file should be makes the create fail regardless of the user's privileges.
	if err := os.Mkdir(filepath.Join(dbPath, "version"), 0o700); err != nil {
		t.Fatalf("Mkdir: %s", err)
	}
	// ReadFile on a directory fails with a non-not-exist error, so drive the create path directly.
	if err := createDatabaseVersionFile(dbPath, versionFilePath(dbPath)); err == nil {
		t.Fatalf("createDatabaseVersionFile reported success although the version file could not be created")
	}
}

func TestCheckDatabaseVersionRoundTrip(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "db")
	if err := checkDatabaseVersion(dbPath); err != nil {
		t.Fatalf("creating the version file: %s", err)
	}
	if err := checkDatabaseVersion(dbPath); err != nil {
		t.Fatalf("reading back the version file: %s", err)
	}
}
