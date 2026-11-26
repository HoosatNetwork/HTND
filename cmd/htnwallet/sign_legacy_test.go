package main

import (
	"path/filepath"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/keys"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/libhtnwallet"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
)

func TestLegacySigningSnapshotCannotUndoGeneration(t *testing.T) {
	params := &dagconfig.MainnetParams
	mnemonic, err := libhtnwallet.CreateMnemonic()
	if err != nil {
		t.Fatal(err)
	}
	file, err := keys.NewFileFromMnemonic(params, mnemonic, "password")
	if err != nil {
		t.Fatal(err)
	}
	if err := file.SetPath(params, filepath.Join(t.TempDir(), "keys.json"), true); err != nil {
		t.Fatal(err)
	}
	file.Version, file.NumThreads = 0, 8
	if err := file.Save(); err != nil {
		t.Fatal(err)
	}
	// Sign reads before asking for the password. Keep that snapshot while
	// the daemon issues addresses and then generation extends the pool.
	snapshot, err := keys.ReadKeysFile(params, file.Path())
	if err != nil {
		t.Fatal(err)
	}
	if err := file.SetLastUsedExternalIndex(9); err != nil {
		t.Fatal(err)
	}
	conf := &generateMLDSA44KeysConfig{KeysFile: file.Path(), Password: "password", Count: 501}
	conf.ActiveNetParams = params
	if err := generateMLDSA44Keys(conf); err != nil {
		t.Fatal(err)
	}
	if _, err := snapshot.DecryptMnemonics("password"); err != nil {
		t.Fatal(err)
	}
	reloaded, err := keys.ReadKeysFile(params, file.Path())
	if err != nil {
		t.Fatal(err)
	}
	if index, size := reloaded.LastUsedExternalIndex(), reloaded.MLDSA44.Size(); index != 9 || size != 501 {
		t.Fatalf("decryption left index %d and pool size %d, want 9 and 501", index, size)
	}
}
