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
	"github.com/HoosatNetwork/HTND/v2/infrastructure/config"
	"github.com/gofrs/flock"
)

// TestGenerateMLDSA44KeysReachesTheIndexesInUse checks that the default adds exactly
// 500 future indexes on each chain and preserves hashes needed to scan older addresses.
func TestGenerateMLDSA44KeysReachesTheIndexesInUse(t *testing.T) {
	params := &dagconfig.MainnetParams
	const password = "password"
	mnemonic, err := libhtnwallet.CreateMnemonic()
	if err != nil {
		t.Fatalf("CreateMnemonic: %+v", err)
	}
	keysFile, err := keys.NewFileFromMnemonic(params, mnemonic, password)
	if err != nil {
		t.Fatalf("NewFileFromMnemonic: %+v", err)
	}
	keysFilePath := filepath.Join(t.TempDir(), "keys.json")
	if err := keysFile.SetPath(params, keysFilePath, true); err != nil {
		t.Fatalf("SetPath: %+v", err)
	}
	if err := keysFile.Save(); err != nil {
		t.Fatalf("Save: %+v", err)
	}
	// A wallet that has received on 40 addresses and sent 700 times.
	if err := keysFile.SetLastUsedExternalIndex(40); err != nil {
		t.Fatalf("SetLastUsedExternalIndex: %+v", err)
	}
	if err := keysFile.SetLastUsedInternalIndex(700); err != nil {
		t.Fatalf("SetLastUsedInternalIndex: %+v", err)
	}

	generate := func(count uint32) *keys.File {
		t.Helper()
		err := generateMLDSA44Keys(&generateMLDSA44KeysConfig{
			KeysFile:     keysFilePath,
			Password:     password,
			Count:        count,
			NetworkFlags: config.NetworkFlags{ActiveNetParams: params},
		})
		if err != nil {
			t.Fatalf("generateMLDSA44Keys: %+v", err)
		}
		read, err := keys.ReadKeysFile(params, keysFilePath)
		if err != nil {
			t.Fatalf("ReadKeysFile: %+v", err)
		}
		return read
	}

	const wantSize = 700 + 1 + libhtnwallet.DefaultMLDSA44KeyPoolSize
	generated := generate(0)
	if size := generated.MLDSA44.Size(); size != wantSize {
		t.Fatalf("the wallet at index 700 got ML-DSA-44 keys for %d indexes, want %d", size, wantSize)
	}
	for _, chain := range []uint8{libhtnwallet.ExternalKeychain, libhtnwallet.InternalKeychain} {
		if _, ok := generated.MLDSA44.PublicKeyHash(chain, wantSize-1); !ok {
			t.Errorf("chain %d is missing the 500th future index", chain)
		}
		if _, ok := generated.MLDSA44.PublicKeyHash(chain, wantSize); ok {
			t.Errorf("chain %d covers the first index past the default target", chain)
		}
	}
	// The keys at the indexes it had before are the same keys.
	for index := uint32(0); index < libhtnwallet.DefaultMLDSA44KeyPoolSize; index += 99 {
		before, _ := keysFile.MLDSA44.PublicKeyHash(libhtnwallet.ExternalKeychain, index)
		after, _ := generated.MLDSA44.PublicKeyHash(libhtnwallet.ExternalKeychain, index)
		if string(before) != string(after) {
			t.Fatalf("the ML-DSA-44 key at index %d changed", index)
		}
	}

	// Running it again, or asking for fewer, changes nothing.
	for _, count := range []uint32{0, 50} {
		if size := generate(count).MLDSA44.Size(); size != wantSize {
			t.Fatalf("generate-mldsa44-keys --count %d left keys for %d indexes, want %d", count, size, wantSize)
		}
	}
}

// TestDefaultMLDSA44KeyCount pins the count generate-mldsa44-keys uses when it is not given one.
func TestDefaultMLDSA44KeyCount(t *testing.T) {
	for _, test := range []struct {
		external, internal, want uint32
		limited                  bool
	}{
		{0, 0, 500, false},
		{40, 700, 1201, false},
		{900, 3, 1401, false},
		// An index no address can have counts as unknown.
		{1 << 31, 3, 504, false},
		{^uint32(0), ^uint32(0), 500, false},
		{0, 1<<31 - 501, 1 << 31, false},
		{0, 1<<31 - 500, 1 << 31, true},
		{0, 1<<31 - 1, 1 << 31, true},
	} {
		keysFile := &keys.File{}
		if err := keysFile.SetPath(&dagconfig.MainnetParams, filepath.Join(t.TempDir(), "keys.json"), true); err != nil {
			t.Fatalf("SetPath: %+v", err)
		}
		if err := keysFile.SetLastUsedExternalIndex(test.external); err != nil {
			t.Fatalf("SetLastUsedExternalIndex: %+v", err)
		}
		if err := keysFile.SetLastUsedInternalIndex(test.internal); err != nil {
			t.Fatalf("SetLastUsedInternalIndex: %+v", err)
		}
		if got, limited := keysFile.DefaultMLDSA44KeyCount(); got != test.want || limited != test.limited {
			t.Errorf("last used indexes %d and %d: got count %d, limited %t, want %d, limited %t",
				test.external, test.internal, got, limited, test.want, test.limited)
		}
	}
}

func generationTestWallet(t *testing.T) (*keys.File, string) {
	t.Helper()
	mnemonic, err := libhtnwallet.CreateMnemonic()
	if err != nil {
		t.Fatal(err)
	}
	params := &dagconfig.MainnetParams
	file, err := keys.NewFileFromMnemonic(params, mnemonic, "password")
	if err != nil {
		t.Fatal(err)
	}
	if err := file.SetPath(params, filepath.Join(t.TempDir(), "keys.json"), true); err != nil {
		t.Fatal(err)
	}
	if err := file.Save(); err != nil {
		t.Fatal(err)
	}
	return file, mnemonic
}

func runGenerateMLDSA44CLI(t *testing.T, path string, args ...string) error {
	t.Helper()
	oldArgs := os.Args
	os.Args = append([]string{oldArgs[0], generateMLDSA44KeysSubCmd, "--keys-file", path, "--password", "password"}, args...)
	defer func() { os.Args = oldArgs }()
	command, conf := parseCommandLine()
	if command != generateMLDSA44KeysSubCmd {
		t.Fatalf("parsed command %q", command)
	}
	return generateMLDSA44Keys(conf.(*generateMLDSA44KeysConfig))
}

func TestGenerateMLDSA44CountFlag(t *testing.T) {
	for _, scenario := range []struct {
		name string
		args []string
		want uint32
	}{
		{"omitted", nil, 504},
		{"explicit zero", []string{"--count", "0"}, 0},
		{"explicit positive", []string{"-c", "7"}, 7},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			file, _ := generationTestWallet(t)
			file.MLDSA44 = nil
			if err := file.SetLastUsedExternalIndex(3); err != nil {
				t.Fatal(err)
			}
			err := runGenerateMLDSA44CLI(t, file.Path(), scenario.args...)
			if scenario.want == 0 {
				if err == nil || !strings.Contains(err.Error(), "--count must be positive") {
					t.Errorf("explicit zero should fail, got %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			generated, err := keys.ReadKeysFile(&dagconfig.MainnetParams, file.Path())
			if err != nil {
				t.Fatal(err)
			}
			if got := generated.MLDSA44.Size(); got != scenario.want {
				t.Errorf("got pool size %d, want %d", got, scenario.want)
			}
		})
	}
}

func TestGenerateMLDSA44KeepsLongerChain(t *testing.T) {
	file, mnemonic := generationTestWallet(t)
	file.MLDSA44.InternalPublicKeyHashes = file.MLDSA44.InternalPublicKeyHashes[:3]
	extension, err := libhtnwallet.MLDSA44PublicKeyHashes(mnemonic, libhtnwallet.ExternalKeychain, 500, 20, false)
	if err != nil {
		t.Fatal(err)
	}
	file.MLDSA44.ExternalPublicKeyHashes = append(file.MLDSA44.ExternalPublicKeyHashes, extension...)
	if err := file.SetLastUsedExternalIndex(3); err != nil {
		t.Fatal(err)
	}
	if err := runGenerateMLDSA44CLI(t, file.Path()); err != nil {
		t.Fatal(err)
	}
	generated, err := keys.ReadKeysFile(&dagconfig.MainnetParams, file.Path())
	if err != nil {
		t.Fatal(err)
	}
	for i, before := range file.MLDSA44.ExternalPublicKeyHashes {
		after, ok := generated.MLDSA44.PublicKeyHash(libhtnwallet.ExternalKeychain, uint32(i))
		if !ok || !bytes.Equal(before, after) {
			t.Fatalf("lost or changed existing receive hash at index %d", i)
		}
	}
	for i, before := range file.MLDSA44.InternalPublicKeyHashes {
		after, ok := generated.MLDSA44.PublicKeyHash(libhtnwallet.InternalKeychain, uint32(i))
		if !ok || !bytes.Equal(before, after) {
			t.Fatalf("lost or changed existing change hash at index %d", i)
		}
	}
	wantSuffix, err := libhtnwallet.MLDSA44PublicKeyHashes(mnemonic, libhtnwallet.InternalKeychain, 503, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := generated.MLDSA44.PublicKeyHash(libhtnwallet.InternalKeychain, 503); !ok || !bytes.Equal(got, wantSuffix[0]) {
		t.Fatal("the final change hash was not derived at its original index")
	}
}

func TestGenerateMLDSA44ImplicitWorkNeedsExplicitCount(t *testing.T) {
	file, _ := generationTestWallet(t)
	if err := file.SetLastUsedExternalIndex(75_000); err != nil {
		t.Fatal(err)
	}
	// An expensive default must be rejected before asking for or decrypting a password.
	// A wrong password keeps the pre-fix run from deriving 151,000 hashes.
	err := runGenerateMLDSA44CLI(t, file.Path(), "--password", "wrong")
	if err == nil || !strings.Contains(err.Error(), "--count 75501") {
		t.Fatalf("large default must require an explicit count, got %v", err)
	}
	if err := runGenerateMLDSA44CLI(t, file.Path(), "--count", "501"); err != nil {
		t.Fatalf("a small explicit extension must still work: %v", err)
	}
}

func TestGenerateMLDSA44ImplicitFileGrowthNeedsExplicitCount(t *testing.T) {
	file, _ := generationTestWallet(t)
	// Only two new hashes are needed, but the long receive chain is preserved and
	// the resulting file would exceed the implicit storage budget.
	hashes := make([][]byte, 32_000)
	for i := range hashes {
		hashes[i] = make([]byte, 32)
	}
	file.MLDSA44.ExternalPublicKeyHashes = hashes
	if err := file.SetLastUsedExternalIndex(1); err != nil {
		t.Fatal(err)
	}
	err := runGenerateMLDSA44CLI(t, file.Path(), "--password", "wrong")
	if err == nil || !strings.Contains(err.Error(), "derive 2 hashes") || !strings.Contains(err.Error(), "--count 502") {
		t.Fatalf("large resulting file must require an explicit count even for a short extension, got %v", err)
	}
}

func TestGenerateMLDSA44RejectsExplicitCountPastAddressRange(t *testing.T) {
	file, _ := generationTestWallet(t)
	err := runGenerateMLDSA44CLI(t, file.Path(), "--count", "2147483649", "--password", "wrong")
	if err == nil || !strings.Contains(err.Error(), "--count must be at most 2147483648") {
		t.Fatalf("want the address range error before decryption or generation, got %v", err)
	}
}

func TestGenerateMLDSA44ReadsUnderLock(t *testing.T) {
	file, _ := generationTestWallet(t)
	lock := flock.New(file.Path() + ".lock")
	if ok, err := lock.TryLock(); err != nil || !ok {
		t.Fatalf("acquiring a lock for another writer: %t, %v", ok, err)
	}
	defer lock.Close()
	if err := os.WriteFile(file.Path(), []byte("unreadable"), 0o600); err != nil {
		t.Fatal(err)
	}
	// An unlocked read would try to decode the other writer's snapshot and fail
	// before reporting contention. Generation must acquire the lock first.
	if err := runGenerateMLDSA44CLI(t, file.Path()); err == nil || !strings.Contains(err.Error(), "locked") {
		t.Fatalf("want lock contention before reading, got %v", err)
	}
	if err := file.SetLastUsedExternalIndex(700); err != nil {
		t.Fatal(err)
	}
	if err := lock.Unlock(); err != nil {
		t.Fatal(err)
	}
	if err := runGenerateMLDSA44CLI(t, file.Path()); err != nil {
		t.Fatal(err)
	}
	generated, err := keys.ReadKeysFile(&dagconfig.MainnetParams, file.Path())
	if err != nil {
		t.Fatal(err)
	}
	if got := generated.LastUsedExternalIndex(); got != 700 || generated.MLDSA44.Size() != 1201 {
		t.Fatalf("expected the latest index and matching target, got index %d and size %d", got, generated.MLDSA44.Size())
	}
}

func TestGenerateMLDSA44MultisigKeepsPeerRangeUntilExchange(t *testing.T) {
	params := &dagconfig.MainnetParams
	localMnemonics, localKeys, localPools, err := keys.CreateMnemonics(params, 1, "password", true)
	if err != nil {
		t.Fatal(err)
	}
	localKey := localKeys[0]
	localPool := localPools[localKey]
	localPool.InternalPublicKeyHashes = localPool.InternalPublicKeyHashes[:2]
	peerMnemonic, err := libhtnwallet.CreateMnemonic()
	if err != nil {
		t.Fatal(err)
	}
	peerKey, err := libhtnwallet.MasterPublicKeyFromMnemonic(params, peerMnemonic, true)
	if err != nil {
		t.Fatal(err)
	}
	file := &keys.File{
		Version: keys.LastVersion, EncryptedMnemonics: localMnemonics,
		ExtendedPublicKeys: []string{localKey, peerKey}, MinimumSignatures: 2,
		MLDSA44Cosigners: localPools,
	}
	file.CosignerIndex, err = libhtnwallet.MinimumCosignerIndex(localKeys, file.ExtendedPublicKeys)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := file.SetPath(params, filepath.Join(dir, "keys.json"), true); err != nil {
		t.Fatal(err)
	}
	if err := file.Save(); err != nil {
		t.Fatal(err)
	}
	exchange := filepath.Join(dir, "peer.json")
	importPeer := func(count uint32) {
		t.Helper()
		pool, err := keys.NewMLDSA44KeyPool(peerMnemonic, count, true)
		if err != nil {
			t.Fatal(err)
		}
		peer := &keys.File{ExtendedPublicKeys: file.ExtendedPublicKeys,
			MLDSA44Cosigners: map[string]*keys.MLDSA44KeyPool{peerKey: pool}}
		if _, err := peer.ExportMLDSA44Cosigners(params, exchange); err != nil {
			t.Fatal(err)
		}
		loaded, err := keys.ReadKeysFile(params, file.Path())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := loaded.ImportMLDSA44Cosigners(params, exchange); err != nil {
			t.Fatal(err)
		}
		if err := loaded.Save(); err != nil {
			t.Fatal(err)
		}
	}
	assertRange := func(covered uint32) {
		t.Helper()
		loaded, err := keys.ReadKeysFile(params, file.Path())
		if err != nil {
			t.Fatal(err)
		}
		if got := len(loaded.MLDSA44Cosigners[localKey].ExternalPublicKeyHashes); got != len(localPool.ExternalPublicKeyHashes) {
			t.Fatalf("local receive coverage shrank to %d", got)
		}
		for _, chain := range []uint8{libhtnwallet.ExternalKeychain, libhtnwallet.InternalKeychain} {
			if _, err := loaded.MLDSA44MultiSigPublicKeyHashes(chain, covered-1); err != nil {
				t.Fatalf("chain %d is missing the final covered index: %v", chain, err)
			}
			if _, err := loaded.MLDSA44MultiSigPublicKeyHashes(chain, covered); err == nil {
				t.Fatalf("chain %d covers the first index past the limiting peer pool", chain)
			}
		}
	}
	importPeer(3)
	if err := runGenerateMLDSA44CLI(t, file.Path(), "--count", "7"); err != nil {
		t.Fatal(err)
	}
	assertRange(3)

	// A legacy wallet must also skip saving if generation has no local work,
	// even though decrypting its mnemonic checks the legacy thread count.
	legacy, err := keys.ReadKeysFile(params, file.Path())
	if err != nil {
		t.Fatal(err)
	}
	legacy.Version, legacy.NumThreads = 0, 8
	if err := legacy.Save(); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(file.Path())
	if err != nil {
		t.Fatal(err)
	}
	if err := runGenerateMLDSA44CLI(t, file.Path(), "--count", "7"); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(file.Path())
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("generation rewrote the file when only the peer pool was short")
	}
	importPeer(7)
	assertRange(7)
}
