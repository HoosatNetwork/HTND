package server

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/keys"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/libhtnwallet"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/logger"
)

// DAA scores on either side of mainnet ML-DSA-44 activation.
const (
	scanGapBeforeActivation = uint64(1_000)
	scanGapAfterActivation  = uint64(300_000_000)
)

type scanGapLogLines chan string

func (lines scanGapLogLines) Write(p []byte) (int, error) {
	lines <- string(p)
	return len(p), nil
}

func (lines scanGapLogLines) Close() error { return nil }

func TestMLDSA44ScanGapLogsStartupSeverityWithoutNode(t *testing.T) {
	backend := logger.NewBackend()
	lines := make(scanGapLogLines, 32)
	if err := backend.AddLogWriter(lines, logger.LevelTrace); err != nil {
		t.Fatal(err)
	}
	if err := backend.Run(); err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	oldLog := log
	log = backend.Logger("HSWD")
	log.SetLevel(logger.LevelTrace)
	defer func() { log = oldLog }()

	for _, scenario := range []struct {
		name  string
		score uint64
		level string
	}{
		{"active at startup", scanGapAfterActivation, "[WRN] HSWD:"},
		{"inactive at startup", scanGapBeforeActivation, "[INF] HSWD:"},
		{"later discovery", scanGapAfterActivation, "[INF] HSWD:"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			var w *feeWallet
			if scenario.name == "later discovery" {
				w = newMLWallet(t, 1, 1)
			} else {
				w = newMLWallet(t, 3, 2)
				delete(w.s.keysFile.MLDSA44Cosigners, w.s.keysFile.ExtendedPublicKeys[1])
			}
			w.s.mldsa44ActiveAtStartup = libhtnwallet.MLDSA44Active(w.params, scenario.score)
			// The server has no background connection. It must still log the correct
			// startup severity. Subsequent successful scan updates must not log again.
			w.s.lock.Lock()
			defer w.s.lock.Unlock()
			w.s.warnOfMLDSA44ScanGap()
			found := &walletAddress{index: 5, keyChain: libhtnwallet.ExternalKeychain}
			address := w.address(t, found).String()
			for range 3 {
				err := w.s.updateAddressesAndLastUsedIndexes(walletAddressSet{address: found},
					&appmessage.GetUsableAddressesResponseMessage{Addresses: []string{address}})
				if err != nil {
					t.Fatal(err)
				}
			}
			log.Tracef("end of scan gap test")
			var notices []string
			timer := time.NewTimer(time.Second)
			defer timer.Stop()
			for {
				select {
				case line := <-lines:
					if strings.Contains(line, "end of scan gap test") {
						if len(notices) != 1 || !strings.Contains(notices[0], scenario.level) {
							t.Fatalf("want a single %s scan-gap notice, got %q", scenario.level, notices)
						}
						return
					}
					for _, key := range w.s.keysFile.ExtendedPublicKeys {
						if strings.Contains(line, key) {
							t.Fatal("log contains a full extended public key")
						}
					}
					for _, mnemonic := range w.mnemonics {
						if strings.Contains(line, mnemonic) {
							t.Fatal("log contains a mnemonic")
						}
					}
					notices = append(notices, line)
				case <-timer.C:
					t.Fatal("logger did not flush the scan-gap messages")
				}
			}
		})
	}
}

func TestMLDSA44ScanGapAfterLegacyPeerImport(t *testing.T) {
	w := newMLWallet(t, 3, 2)
	file := w.s.keysFile
	// The first public key belongs to the one local mnemonic, as in create. Simulate
	// a pre-ML-DSA-44 keys file by keeping the mnemonic and clearing all the pools.
	mnemonics, localKeys, _, err := keys.CreateMnemonics(w.params, 1, "test password", true)
	if err != nil {
		t.Fatal(err)
	}
	file.ExtendedPublicKeys[0] = localKeys[0]
	file.CosignerIndex, err = libhtnwallet.MinimumCosignerIndex(localKeys, file.ExtendedPublicKeys)
	if err != nil {
		t.Fatal(err)
	}
	peer := file.ExtendedPublicKeys[1]
	peerWallet := keys.File{
		ExtendedPublicKeys: file.ExtendedPublicKeys,
		MLDSA44Cosigners:   map[string]*keys.MLDSA44KeyPool{peer: file.MLDSA44Cosigners[peer]},
	}
	exchange := filepath.Join(t.TempDir(), "peers.json")
	if _, err := peerWallet.ExportMLDSA44Cosigners(w.params, exchange); err != nil {
		t.Fatal(err)
	}

	file.EncryptedMnemonics = mnemonics
	file.MLDSA44Cosigners = nil
	if err := file.Save(); err != nil {
		t.Fatal(err)
	}
	legacy, err := keys.ReadKeysFile(w.params, file.Path())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.ImportMLDSA44Cosigners(w.params, exchange); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Save(); err != nil {
		t.Fatal(err)
	}
	w.s.keysFile, err = keys.ReadKeysFile(w.params, file.Path())
	if err != nil {
		t.Fatal(err)
	}
	if gap, partial := w.s.mldsa44ScanGap(); !partial || !strings.Contains(gap, "..."+file.ExtendedPublicKeys[0][len(file.ExtendedPublicKeys[0])-8:]) {
		t.Fatalf("a peer pool was imported before generating the local pool: partial=%t, gap=%q", partial, gap)
	}
}

func TestMLDSA44ScanGapUsesEachChainCoverage(t *testing.T) {
	for _, multisig := range []bool{false, true} {
		name, cosigners, required := "single-sig", 1, uint32(1)
		if multisig {
			name, cosigners, required = "multisig", 3, 2
		}
		t.Run(name, func(t *testing.T) {
			w := newMLWallet(t, cosigners, required)
			file := w.s.keysFile
			file.EncryptedMnemonics = []*keys.EncryptedMnemonic{{}}
			if multisig {
				// Different cosigners limit the two chains. Receive covers 0-2,
				// and change covers 0-1.
				receive := file.MLDSA44Cosigners[file.ExtendedPublicKeys[0]]
				receive.InternalPublicKeyHashes = append(receive.InternalPublicKeyHashes, make([]byte, 32))
				change := file.MLDSA44Cosigners[file.ExtendedPublicKeys[1]]
				change.InternalPublicKeyHashes = change.InternalPublicKeyHashes[:2]
				change.ExternalPublicKeyHashes = append(change.ExternalPublicKeyHashes, make([]byte, 32))
			} else {
				file.MLDSA44.InternalPublicKeyHashes = file.MLDSA44.InternalPublicKeyHashes[:2]
			}
			if err := file.SetLastUsedExternalIndex(2); err != nil {
				t.Fatal(err)
			}
			if err := file.SetLastUsedInternalIndex(1); err != nil {
				t.Fatal(err)
			}
			if gap, _ := w.s.mldsa44ScanGap(); gap != "" {
				t.Fatalf("both chains cover their used indexes, got %q", gap)
			}
			if err := file.SetLastUsedInternalIndex(2); err != nil {
				t.Fatal(err)
			}
			gap, partial := w.s.mldsa44ScanGap()
			if partial || !strings.Contains(gap, "change ML-DSA-44") || strings.Contains(gap, "receive ML-DSA-44") {
				t.Fatalf("only change is past coverage: partial=%t, gap=%q", partial, gap)
			}
			if !strings.Contains(gap, "generate-mldsa44-keys --count <n>") {
				t.Fatalf("missing explicit recovery count in %q", gap)
			}
		})
	}
}

func TestMLDSA44ScanGapReportsUsedWalletWithoutPools(t *testing.T) {
	w := newMLWallet(t, 3, 2)
	w.s.keysFile.MLDSA44Cosigners = nil
	if gap, _ := w.s.mldsa44ScanGap(); gap != "" {
		t.Fatalf("no recorded use, got %q", gap)
	}
	if err := w.s.keysFile.SetLastUsedExternalIndex(700); err != nil {
		t.Fatal(err)
	}
	gap, partial := w.s.mldsa44ScanGap()
	if partial || !strings.Contains(gap, "receive ML-DSA-44 multisig addresses from index 0") ||
		!strings.Contains(gap, "import-mldsa44-keys") {
		t.Fatalf("should advise watch-only wallets with a recorded index to import pools: partial=%t, gap=%q", partial, gap)
	}
	if strings.Contains(gap, "generate-mldsa44-keys") {
		t.Fatalf("watch-only wallet cannot generate pools: %q", gap)
	}
}

// TestMLDSA44ScanGap pins when the daemon has something to say about address indexes in use that its
// ML-DSA-44 keys do not reach, and when that is a fault rather than information.
//
// The scans ask about an ML-DSA-44 address only where the wallet holds its key hash, while the
// secp256k1 addresses at every index are scanned. A wallet restored from its mnemonic, or a multisig
// one missing a cosigner's keys, therefore showed its secp256k1 coins and silently left out the coins
// on ML-DSA-44 addresses past its keys.
//
// Ordinary secp256k1 activity can also advance the shared change index past the pool.
// A coverage notice in that case is informational.
func TestMLDSA44ScanGap(t *testing.T) {
	t.Run("single-sig", func(t *testing.T) {
		w := newMLWallet(t, 1, 1) // keys for indexes 0-2
		w.s.keysFile.EncryptedMnemonics = []*keys.EncryptedMnemonic{{}}
		if err := w.s.keysFile.SetLastUsedExternalIndex(2); err != nil {
			t.Fatalf("SetLastUsedExternalIndex: %+v", err)
		}
		if gap, _ := w.s.mldsa44ScanGap(); gap != "" {
			t.Fatalf("a wallet whose keys reach its last used index reports a gap: %s", gap)
		}

		// Sends alone take the internal index past the keys: no ML-DSA-44 address was ever involved.
		if err := w.s.keysFile.SetLastUsedInternalIndex(3); err != nil {
			t.Fatalf("SetLastUsedInternalIndex: %+v", err)
		}
		gap, misconfigured := w.s.mldsa44ScanGap()
		if !strings.Contains(gap, generateMLDSA44KeysSubCmdName) {
			t.Fatalf("a wallet using index 3 with keys for indexes 0-2 reports %q, want advice to run %s",
				gap, generateMLDSA44KeysSubCmdName)
		}
		if misconfigured {
			t.Fatalf("a wallet that passed its ML-DSA-44 keys by sending is reported as a fault, which is logged " +
				"as a warning: every wallet in ordinary use would get that warning")
		}

		w.s.keysFile.MLDSA44 = nil
		if gap, partial := w.s.mldsa44ScanGap(); !strings.Contains(gap, "receive ML-DSA-44 addresses from index 0") || partial {
			t.Fatalf("a used legacy wallet without pools should report conditional recovery guidance: %q", gap)
		}
	})

	t.Run("multisig", func(t *testing.T) {
		w := newMLWallet(t, 3, 2) // every cosigner's keys for indexes 0-2; the keys file holds no mnemonic
		// No line names a cosigner by its whole extended public key: that key gives away every address
		// of the cosigner's key chain, and logs are passed on.
		extendedPublicKeys := append([]string{}, w.s.keysFile.ExtendedPublicKeys...)
		withoutKeys := func(gap string) string {
			t.Helper()
			for _, extendedPublicKey := range extendedPublicKeys {
				if strings.Contains(gap, extendedPublicKey) {
					t.Fatalf("the line holds a cosigner's whole extended public key: %s", gap)
				}
			}
			return gap
		}
		if err := w.s.keysFile.SetLastUsedExternalIndex(2); err != nil {
			t.Fatalf("SetLastUsedExternalIndex: %+v", err)
		}
		if gap, _ := w.s.mldsa44ScanGap(); gap != "" {
			t.Fatalf("a wallet holding every cosigner's keys reports a gap: %s", gap)
		}

		// Every cosigner's keys, and use past them: information, as for a single-sig wallet.
		if err := w.s.keysFile.SetLastUsedInternalIndex(3); err != nil {
			t.Fatalf("SetLastUsedInternalIndex: %+v", err)
		}
		if gap, misconfigured := w.s.mldsa44ScanGap(); !strings.Contains(withoutKeys(gap), "from index 3 on are not scanned") || misconfigured {
			t.Fatalf("use past every cosigner's keys reports %q as a fault (%v); want a gap that is not a fault", gap, misconfigured)
		}
		if err := w.s.keysFile.SetLastUsedInternalIndex(2); err != nil {
			t.Fatalf("SetLastUsedInternalIndex: %+v", err)
		}

		// Some cosigners' keys imported and one cosigner's missing: the wallet was set up for
		// ML-DSA-44 and cannot build a single ML-DSA-44 address.
		removedKey := extendedPublicKeys[0]
		removedPool := w.s.keysFile.MLDSA44Cosigners[removedKey]
		delete(w.s.keysFile.MLDSA44Cosigners, removedKey)
		gap, misconfigured := w.s.mldsa44ScanGap()
		if !misconfigured || !strings.Contains(withoutKeys(gap), importMLDSA44KeysSubCmdName) {
			t.Fatalf("a wallet missing one cosigner's keys reports %q (a fault: %v), want a fault with advice to run %s",
				gap, misconfigured, importMLDSA44KeysSubCmdName)
		}
		// The cosigner is named by the end of its key, which is enough to tell which one it is.
		if !strings.Contains(gap, "1 of the wallet's 3 cosigners") || !strings.Contains(gap, "..."+removedKey[len(removedKey)-8:]) {
			t.Fatalf("the line does not say which cosigner's keys are missing: %s", gap)
		}

		// Only the wallet's own keys, as create makes it: the cosigners have exchanged nothing.
		w.s.keysFile.MLDSA44Cosigners = map[string]*keys.MLDSA44KeyPool{removedKey: removedPool}
		w.s.keysFile.EncryptedMnemonics = []*keys.EncryptedMnemonic{{}}
		localIndex, err := libhtnwallet.MinimumCosignerIndex([]string{removedKey}, extendedPublicKeys)
		if err != nil {
			t.Fatal(err)
		}
		w.s.keysFile.CosignerIndex = localIndex
		gap, misconfigured = w.s.mldsa44ScanGap()
		if misconfigured || !strings.Contains(withoutKeys(gap), "2 of the wallet's 3 cosigners are missing") {
			t.Fatalf("a wallet holding only its own keys reports %q (a fault: %v); want a line, not a fault, that says "+
				"the other cosigners' keys have not been imported", gap, misconfigured)
		}
		// It says what is the matter, not that address indexes are past the keys: none are in use.
		if strings.Contains(gap, "uses address indexes") {
			t.Fatalf("the line for keys not yet imported talks about used address indexes: %s", gap)
		}

		// 3-of-3 has no ML-DSA-44 multisig addresses, so nothing can be missed.
		w.s.keysFile.MinimumSignatures = 3
		if gap, _ := w.s.mldsa44ScanGap(); gap != "" {
			t.Fatalf("a wallet ML-DSA-44 multisig does not support reports a gap: %s", gap)
		}
	})

	t.Run("a discovery past the keys is reported once", func(t *testing.T) {
		w := newMLWallet(t, 1, 1) // keys for indexes 0-2
		w.s.warnOfMLDSA44ScanGap()
		if w.s.warnedOfMLDSA44ScanGap {
			t.Fatalf("a gap was reported for a new wallet")
		}
		// The scan finds a coin on the secp256k1 address at index 5.
		found := &walletAddress{index: 5, keyChain: libhtnwallet.ExternalKeychain}
		address := w.address(t, found).String()
		err := w.s.updateAddressesAndLastUsedIndexes(walletAddressSet{address: found},
			&appmessage.GetUsableAddressesResponseMessage{Addresses: []string{address}})
		if err != nil {
			t.Fatalf("updateAddressesAndLastUsedIndexes: %+v", err)
		}
		if !w.s.warnedOfMLDSA44ScanGap {
			t.Fatalf("a coin found at index 5, past the wallet's ML-DSA-44 keys, was not reported")
		}
	})
}
