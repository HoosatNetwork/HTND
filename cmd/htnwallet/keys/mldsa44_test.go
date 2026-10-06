package keys

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/libhtnwallet"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
)

// TestMLDSA44CosignerKeyExchange walks the multisig key exchange: each cosigner exports its pool, the
// other imports it, and imports that would put wrong keys into a wallet are refused.
func TestMLDSA44CosignerKeyExchange(t *testing.T) {
	params := &dagconfig.MainnetParams
	newCosigner := func() (string, *MLDSA44KeyPool) {
		mnemonic, err := libhtnwallet.CreateMnemonic()
		if err != nil {
			t.Fatalf("CreateMnemonic: %+v", err)
		}
		extendedPublicKey, err := libhtnwallet.MasterPublicKeyFromMnemonic(params, mnemonic, true)
		if err != nil {
			t.Fatalf("MasterPublicKeyFromMnemonic: %+v", err)
		}
		pool, err := NewMLDSA44KeyPool(mnemonic, 4, true)
		if err != nil {
			t.Fatalf("NewMLDSA44KeyPool: %+v", err)
		}
		return extendedPublicKey, pool
	}
	aliceKey, alicePool := newCosigner()
	bobKey, bobPool := newCosigner()
	extendedPublicKeys := []string{aliceKey, bobKey}

	dir := t.TempDir()
	alice := newTestFile(t, filepath.Join(dir, "alice.json"), 2)
	alice.ExtendedPublicKeys = extendedPublicKeys
	alice.MLDSA44Cosigners = map[string]*MLDSA44KeyPool{aliceKey: alicePool}
	bob := newTestFile(t, filepath.Join(dir, "bob.json"), 2)
	bob.ExtendedPublicKeys = extendedPublicKeys
	bob.MLDSA44Cosigners = map[string]*MLDSA44KeyPool{bobKey: bobPool}

	if _, err := alice.MLDSA44MultiSigPublicKeyHashes(libhtnwallet.ExternalKeychain, 1); err == nil {
		t.Fatalf("resolved a multisig address before the other cosigner's keys were imported")
	}

	aliceExport := filepath.Join(dir, "alice-mldsa44.json")
	if _, err := alice.ExportMLDSA44Cosigners(params, aliceExport); err != nil {
		t.Fatalf("ExportMLDSA44Cosigners: %+v", err)
	}
	bobExport := filepath.Join(dir, "bob-mldsa44.json")
	if _, err := bob.ExportMLDSA44Cosigners(params, bobExport); err != nil {
		t.Fatalf("ExportMLDSA44Cosigners: %+v", err)
	}
	if _, err := alice.ImportMLDSA44Cosigners(params, bobExport); err != nil {
		t.Fatalf("ImportMLDSA44Cosigners: %+v", err)
	}
	if _, err := bob.ImportMLDSA44Cosigners(params, aliceExport); err != nil {
		t.Fatalf("ImportMLDSA44Cosigners: %+v", err)
	}

	aliceHashes, err := alice.MLDSA44MultiSigPublicKeyHashes(libhtnwallet.ExternalKeychain, 1)
	if err != nil {
		t.Fatalf("MLDSA44MultiSigPublicKeyHashes: %+v", err)
	}
	bobHashes, err := bob.MLDSA44MultiSigPublicKeyHashes(libhtnwallet.ExternalKeychain, 1)
	if err != nil {
		t.Fatalf("MLDSA44MultiSigPublicKeyHashes: %+v", err)
	}
	for _, extendedPublicKey := range extendedPublicKeys {
		if !bytes.Equal(aliceHashes[extendedPublicKey], bobHashes[extendedPublicKey]) {
			t.Fatalf("the cosigners disagree on %s's key after the exchange", extendedPublicKey)
		}
	}

	// A file for another network, a key that is not a cosigner, and different keys for a known
	// cosigner are all refused.
	if _, err := alice.ImportMLDSA44Cosigners(&dagconfig.TestnetParams, bobExport); err == nil {
		t.Fatalf("imported a mainnet key file into a testnet wallet")
	}
	strangerKey, strangerPool := newCosigner()
	stranger := newTestFile(t, filepath.Join(dir, "stranger.json"), 2)
	stranger.ExtendedPublicKeys = []string{strangerKey, bobKey}
	stranger.MLDSA44Cosigners = map[string]*MLDSA44KeyPool{strangerKey: strangerPool}
	strangerExport := filepath.Join(dir, "stranger-mldsa44.json")
	if _, err := stranger.ExportMLDSA44Cosigners(params, strangerExport); err != nil {
		t.Fatalf("ExportMLDSA44Cosigners: %+v", err)
	}
	if _, err := alice.ImportMLDSA44Cosigners(params, strangerExport); err == nil {
		t.Fatalf("imported the keys of a key that is not a cosigner")
	}
	stranger.MLDSA44Cosigners = map[string]*MLDSA44KeyPool{bobKey: strangerPool}
	if _, err := stranger.ExportMLDSA44Cosigners(params, strangerExport); err != nil {
		t.Fatalf("ExportMLDSA44Cosigners: %+v", err)
	}
	if _, err := alice.ImportMLDSA44Cosigners(params, strangerExport); err == nil {
		t.Fatalf("replaced a cosigner's keys with different ones")
	}
}

// TestMLDSA44KeyPoolRoundTrips pins that the ML-DSA-44 key pool survives Save and ReadKeysFile, and
// that a keys file written before ML-DSA-44 support - with no pool - still reads, as a wallet without one.
func TestMLDSA44KeyPoolRoundTrips(t *testing.T) {
	mnemonic, err := libhtnwallet.CreateMnemonic()
	if err != nil {
		t.Fatalf("CreateMnemonic: %+v", err)
	}
	pool, err := NewMLDSA44KeyPool(mnemonic, 3, false)
	if err != nil {
		t.Fatalf("NewMLDSA44KeyPool: %+v", err)
	}

	path := filepath.Join(t.TempDir(), "keys.json")
	file := newTestFile(t, path, 1)
	file.MLDSA44 = pool
	if err := file.Save(); err != nil {
		t.Fatalf("Save: %+v", err)
	}
	read, err := ReadKeysFile(&dagconfig.MainnetParams, path)
	if err != nil {
		t.Fatalf("ReadKeysFile: %+v", err)
	}
	if read.MLDSA44.Size() != 3 {
		t.Fatalf("read back a pool of %d indexes, want 3", read.MLDSA44.Size())
	}
	for _, keychain := range []uint8{libhtnwallet.ExternalKeychain, libhtnwallet.InternalKeychain} {
		for index := uint32(0); index < 3; index++ {
			want, _ := pool.PublicKeyHash(keychain, index)
			got, ok := read.MLDSA44.PublicKeyHash(keychain, index)
			if !ok || !bytes.Equal(got, want) {
				t.Fatalf("key chain %d index %d did not round-trip", keychain, index)
			}
		}
	}
	if _, ok := read.MLDSA44.PublicKeyHash(libhtnwallet.ExternalKeychain, 3); ok {
		t.Fatalf("the pool answered for an index past its size")
	}

	legacyPath := filepath.Join(t.TempDir(), "keys.json")
	if err := newTestFile(t, legacyPath, 1).Save(); err != nil {
		t.Fatalf("Save: %+v", err)
	}
	legacy, err := ReadKeysFile(&dagconfig.MainnetParams, legacyPath)
	if err != nil {
		t.Fatalf("ReadKeysFile of a file without an ML-DSA-44 pool: %+v", err)
	}
	if legacy.MLDSA44 != nil || legacy.MLDSA44.Size() != 0 {
		t.Fatalf("a file without an ML-DSA-44 pool read back with one")
	}
	if _, ok := legacy.MLDSA44.PublicKeyHash(libhtnwallet.ExternalKeychain, 0); ok {
		t.Fatalf("a missing pool answered for index 0")
	}
}
