package app

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
	"github.com/HoosatNetwork/HTND/v2/domain/miningmanager/mempool"
	"github.com/HoosatNetwork/HTND/v2/domain/prefixmanager"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database/pebble"
)

const prepareDowngradeChildEnv = "HTND_TEST_PREPARE_DOWNGRADE_DIR"

// htnd --prepare-downgrade, parsed from the command line like a real start, removes the
// pruning point UTXO journal of the active consensus, logs what it did and exits
// without starting the node (a started node would block until interrupted).
// Consensus integration tests cover interrupted normalization and journal recovery.
func TestPrepareDowngradeFlag(t *testing.T) {
	if dir := os.Getenv(prepareDowngradeChildEnv); dir != "" {
		os.Args = []string{"htnd", "--simnet", "--appdir=" + dir, "--nodnsseed", "--profile=6061", "--prepare-downgrade"}
		if err := StartApp(); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	t.Setenv("HTND_TEST_MODE", "0")
	t.Setenv("HTND_PEBBLE_DISABLE_WAL", "0")
	dir := t.TempDir()
	dbPath := filepath.Join(dir, dagconfig.SimnetParams.Name, defaultDataDirname)
	marker := func() bool {
		t.Helper()
		db, err := pebble.NewPebbleDB(dbPath, 8)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		active, exists, err := prefixmanager.ActivePrefix(db)
		if err != nil || !exists {
			t.Fatalf("active prefix %t %v", exists, err)
		}
		found, err := db.Has(database.MakeBucket(active.Serialize()).Key([]byte("pruning-point-utxo-set-journal-state")))
		if err != nil {
			t.Fatal(err)
		}
		return found
	}

	// A database written by this release: the journal marker is present.
	db, err := pebble.NewPebbleDB(dbPath, 8)
	if err != nil {
		t.Fatal(err)
	}
	params := dagconfig.SimnetParams
	d, err := domain.New(&consensus.Config{Params: params, DeferPruningPointUTXOSetUpdate: true}, mempool.DefaultConfig(&params), db)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	d.Consensus().Close()
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	if !marker() {
		t.Fatal("control: this release wrote no journal marker")
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestPrepareDowngradeFlag$", "-test.timeout=120s")
	cmd.Env = append(os.Environ(), prepareDowngradeChildEnv+"="+dir)
	output, err := cmd.CombinedOutput()
	logs := string(output)
	_ = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err == nil && !entry.IsDir() && strings.HasSuffix(path, ".log") {
			if data, readErr := os.ReadFile(path); readErr == nil {
				logs += string(data)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("htnd --prepare-downgrade: %v\n%s", err, logs)
	}
	for _, line := range strings.Split(logs, "\n") {
		if strings.Contains(line, "a downgrade") || strings.Contains(line, "Downgrade preparation") {
			t.Log(line)
		}
	}
	for _, want := range []string{"Preparing the database for a downgrade; found a pruning point UTXO journal at phase complete",
		"Downgrade preparation done:", "the previous release can open this database"} {
		if !strings.Contains(logs, want) {
			t.Fatalf("missing %q in:\n%s", want, logs)
		}
	}
	if marker() {
		t.Fatal("--prepare-downgrade left the journal marker")
	}
	if strings.Contains(logs, "Profile server listening on") {
		t.Fatal("--prepare-downgrade started the profiling listener")
	}
}
