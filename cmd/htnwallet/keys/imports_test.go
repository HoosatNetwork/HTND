package keys

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/libhtnwallet"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
)

const testPassword = "test password"

func webWalletPaths(numAddresses uint32) []string {
	var paths []string
	for _, keyChain := range []uint8{libhtnwallet.ExternalKeychain, libhtnwallet.InternalKeychain} {
		for index := range numAddresses {
			paths = append(paths, libhtnwallet.WebWalletPath(keyChain, index))
		}
	}
	return paths
}

func saveAndRead(t *testing.T, file *File) *File {
	t.Helper()
	file.path = filepath.Join(t.TempDir(), "keys.json")
	if err := file.Save(); err != nil {
		t.Fatalf("Save: %+v", err)
	}
	read, err := ReadKeysFile(&dagconfig.MainnetParams, file.path)
	if err != nil {
		t.Fatalf("ReadKeysFile: %+v", err)
	}
	return read
}

// TestImportedWalletsRoundTrip pins that an imported wallet survives a save and a read, holds nothing but
// the imported wallet, and decrypts to signing keys matching its stored public keys.
func TestImportedWalletsRoundTrip(t *testing.T) {
	params := &dagconfig.MainnetParams
	privateKey, _, err := libhtnwallet.CreateKeyPair(false)
	if err != nil {
		t.Fatalf("CreateKeyPair: %+v", err)
	}
	mnemonic, err := libhtnwallet.CreateMnemonic()
	if err != nil {
		t.Fatalf("CreateMnemonic: %+v", err)
	}

	tests := []struct {
		importType string
		secret     string
		paths      []string
		numKeys    int
		firstPath  string
	}{
		{libhtnwallet.ImportedKeyTypePrivateKey, hex.EncodeToString(privateKey), nil, 1, ""},
		{libhtnwallet.ImportedKeyTypeHTNWebWallet, mnemonic, webWalletPaths(3), 6, libhtnwallet.WebWalletPath(libhtnwallet.ExternalKeychain, 0)},
	}
	for _, test := range tests {
		file, err := NewImportedFile(params, test.importType, test.secret, test.paths, testPassword)
		if err != nil {
			t.Fatalf("%s: NewImportedFile: %+v", test.importType, err)
		}
		read := saveAndRead(t, file)

		if !read.IsImported() || read.Imported.Type != test.importType {
			t.Fatalf("%s: read back as %+v", test.importType, read.Imported)
		}
		if len(read.EncryptedMnemonics) != 0 || len(read.ExtendedPublicKeys) != 0 || read.MinimumSignatures != 1 || read.ECDSA {
			t.Fatalf("%s: an imported wallet holds htnwallet keys or settings: %+v", test.importType, read)
		}
		importedKeys := read.ImportedKeys()
		if len(importedKeys) != test.numKeys || importedKeys[0].Path != test.firstPath {
			t.Fatalf("%s: read %d keys, first at %q", test.importType, len(importedKeys), importedKeys[0].Path)
		}

		mnemonics, signingKeys, err := read.DecryptSigningKeys(params, testPassword)
		if err != nil {
			t.Fatalf("%s: DecryptSigningKeys: %+v", test.importType, err)
		}
		if len(mnemonics) != 0 || len(signingKeys) != test.numKeys {
			t.Fatalf("%s: decrypted %d mnemonics and %d imported keys", test.importType, len(mnemonics), len(signingKeys))
		}
		for _, importedKey := range importedKeys {
			if _, ok := signingKeys[importedKey.ExtendedPublicKey]; !ok {
				t.Fatalf("%s: no signing key for imported key %+v", test.importType, importedKey)
			}
		}

		secret, err := read.DecryptImportedSecret(testPassword)
		if err != nil || secret != test.secret {
			t.Fatalf("%s: decrypted secret %q, %v", test.importType, secret, err)
		}
		if _, _, err := read.DecryptSigningKeys(params, "wrong password"); err == nil {
			t.Fatalf("%s: decrypting with a wrong password succeeded", test.importType)
		}
	}
}

// TestHTNWalletFileIsUnchanged pins that an htnwallet wallet's keys file is not an imported wallet and is
// written exactly as before imported wallets existed, so older htnwallet versions - which reject unknown
// fields - still read it.
func TestHTNWalletFileIsUnchanged(t *testing.T) {
	mnemonic, err := libhtnwallet.CreateMnemonic()
	if err != nil {
		t.Fatalf("CreateMnemonic: %+v", err)
	}
	file, err := NewFileFromMnemonic(&dagconfig.MainnetParams, mnemonic, testPassword)
	if err != nil {
		t.Fatalf("NewFileFromMnemonic: %+v", err)
	}
	read := saveAndRead(t, file)
	if read.IsImported() || len(read.ImportedKeys()) != 0 {
		t.Fatalf("an htnwallet wallet reads back as an imported one")
	}

	content, err := os.ReadFile(read.Path())
	if err != nil {
		t.Fatalf("ReadFile: %+v", err)
	}
	if strings.Contains(string(content), "imported") {
		t.Fatalf("an htnwallet wallet's keys file has an imported field: %s", content)
	}

	mnemonics, importedKeys, err := read.DecryptSigningKeys(&dagconfig.MainnetParams, testPassword)
	if err != nil || len(mnemonics) != 1 || mnemonics[0] != mnemonic || importedKeys != nil {
		t.Fatalf("DecryptSigningKeys of an htnwallet wallet: %d mnemonics, %d imported keys, %v",
			len(mnemonics), len(importedKeys), err)
	}
}

// TestKeysFileHoldsOneWallet pins that a keys file mixing an imported wallet with htnwallet keys is
// refused rather than read as either.
func TestKeysFileHoldsOneWallet(t *testing.T) {
	privateKey, _, err := libhtnwallet.CreateKeyPair(false)
	if err != nil {
		t.Fatalf("CreateKeyPair: %+v", err)
	}
	file, err := NewImportedFile(&dagconfig.MainnetParams, libhtnwallet.ImportedKeyTypePrivateKey,
		hex.EncodeToString(privateKey), nil, testPassword)
	if err != nil {
		t.Fatalf("NewImportedFile: %+v", err)
	}
	file.ExtendedPublicKeys = []string{"xpub-test"}
	file.path = filepath.Join(t.TempDir(), "keys.json")
	if err := file.Save(); err != nil {
		t.Fatalf("Save: %+v", err)
	}

	_, err = ReadKeysFile(&dagconfig.MainnetParams, file.path)
	if err == nil || !strings.Contains(err.Error(), "one wallet") {
		t.Fatalf("reading a keys file with both kinds of wallet: got %v", err)
	}
}

func TestNewImportedFileRefusesBadSecrets(t *testing.T) {
	params := &dagconfig.MainnetParams
	for _, test := range []struct {
		importType string
		secret     string
		paths      []string
	}{
		{libhtnwallet.ImportedKeyTypePrivateKey, "abcd", nil},
		{libhtnwallet.ImportedKeyTypeHTNWebWallet, "not a mnemonic", webWalletPaths(1)},
		{"unknown", "abcd", nil},
	} {
		if _, err := NewImportedFile(params, test.importType, test.secret, test.paths, testPassword); err == nil {
			t.Fatalf("NewImportedFile(%s, %q) succeeded", test.importType, test.secret)
		}
	}
}
