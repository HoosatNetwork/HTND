package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/keys"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/libhtnwallet"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
)

func localCosignerWallet(t *testing.T) (file *keys.File, localKey, mnemonic string) {
	t.Helper()
	params := &dagconfig.MainnetParams
	encrypted, localKeys, _, err := keys.CreateMnemonics(params, 1, "password", true)
	if err != nil {
		t.Fatal(err)
	}
	peerMnemonic, err := libhtnwallet.CreateMnemonic()
	if err != nil {
		t.Fatal(err)
	}
	peerKey, err := libhtnwallet.MasterPublicKeyFromMnemonic(params, peerMnemonic, true)
	if err != nil {
		t.Fatal(err)
	}
	localKey = localKeys[0]
	file = &keys.File{
		Version: keys.LastVersion, EncryptedMnemonics: encrypted,
		ExtendedPublicKeys: []string{localKey, peerKey}, MinimumSignatures: 2,
	}
	file.CosignerIndex, err = libhtnwallet.MinimumCosignerIndex(localKeys, file.ExtendedPublicKeys)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.SetPath(params, filepath.Join(t.TempDir(), "keys.json"), true); err != nil {
		t.Fatal(err)
	}
	mnemonics, err := file.DecryptMnemonics("password")
	if err != nil {
		t.Fatal(err)
	}
	return file, localKey, mnemonics[0]
}

func TestGenerateMLDSA44RebuildsDifferingLocalPool(t *testing.T) {
	file, localKey, mnemonic := localCosignerWallet(t)
	valid, err := keys.NewMLDSA44KeyPool(mnemonic, 8, true)
	if err != nil {
		t.Fatal(err)
	}
	// Store a pool that differs from the mnemonic at one index of the chain
	// that is already longer than the requested count.
	badReceive := append([][]byte(nil), valid.ExternalPublicKeyHashes...)
	badReceive[6] = bytes.Repeat([]byte{0x42}, 32)
	file.MLDSA44Cosigners = map[string]*keys.MLDSA44KeyPool{localKey: {
		ExternalPublicKeyHashes: badReceive,
		InternalPublicKeyHashes: valid.InternalPublicKeyHashes[:2],
	}}
	if err := file.Save(); err != nil {
		t.Fatal(err)
	}
	if err := runGenerateMLDSA44CLI(t, file.Path(), "--count", "4"); err != nil {
		t.Fatal(err)
	}
	read, err := keys.ReadKeysFile(&dagconfig.MainnetParams, file.Path())
	if err != nil {
		t.Fatal(err)
	}
	pool := read.MLDSA44Cosigners[localKey]
	for _, chain := range []struct {
		name      string
		got, want [][]byte
	}{
		{"receive", pool.ExternalPublicKeyHashes, valid.ExternalPublicKeyHashes},
		{"change", pool.InternalPublicKeyHashes, valid.InternalPublicKeyHashes[:4]},
	} {
		if len(chain.got) != len(chain.want) {
			t.Errorf("%s coverage = %d, want %d", chain.name, len(chain.got), len(chain.want))
		}
		for i := range min(len(chain.got), len(chain.want)) {
			if !bytes.Equal(chain.got[i], chain.want[i]) {
				t.Errorf("%s hash at index %d does not match the mnemonic", chain.name, i)
			}
		}
	}
	// Repeat from the original file for an omitted-count run.
	if err := file.Save(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(file.Path())
	if err != nil {
		t.Fatal(err)
	}
	if err := runGenerateMLDSA44CLI(t, file.Path()); err == nil || !strings.Contains(err.Error(), "--count 500 to rebuild it") {
		t.Errorf("expected implicit generation to refuse and explain how to rebuild the pool, got %v", err)
	}
	after, err := os.ReadFile(file.Path())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("implicit generation changed a mismatched local pool")
	}
}

func TestGenerateMLDSA44ImplicitBudgetIncludesLocalPrefix(t *testing.T) {
	file, localKey, _ := localCosignerWallet(t)
	receive := make([][]byte, 20_000)
	for i := range receive {
		receive[i] = make([]byte, 32)
	}
	file.MLDSA44Cosigners = map[string]*keys.MLDSA44KeyPool{localKey: {
		ExternalPublicKeyHashes: receive,
		InternalPublicKeyHashes: receive[:2],
	}}
	if err := file.Save(); err != nil {
		t.Fatal(err)
	}
	err := runGenerateMLDSA44CLI(t, file.Path())
	if err == nil || !strings.Contains(err.Error(), "default generation would derive 20500 hashes") {
		t.Fatalf("expected an explicit-count error including the local prefix, got %v", err)
	}
}
