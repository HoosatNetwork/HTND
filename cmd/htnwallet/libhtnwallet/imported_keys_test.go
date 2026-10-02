package libhtnwallet_test

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/libhtnwallet"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/libhtnwallet/bip32"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/testutils"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/txscript"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/utxo"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
	"github.com/HoosatNetwork/HTND/v2/util"
)

// The web wallet vectors below were produced by the HTN web wallet's own code - htn-wallet's
// Wallet.fromMnemonic (bitcore-mnemonic) and AddressManager.deriveAddress (htn-core-lib) - for the BIP39
// test mnemonic, so they pin compatibility with the real wallet rather than with this package's reading
// of it.
const (
	webWalletTestMnemonic = "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about"
	// The "privKey" the web wallet derives from webWalletTestMnemonic and stores in its exports.
	webWalletTestMasterKey = "xprv9s21ZrQH143K3GJpoapnV8SFfukcVBSfeCficPSGfubmSFDxo1kuHnLisriDvSnRRuL2Qrg5ggqHKNVpxR86QEC8w35uxmGoggxtQTPvfUu"
)

var webWalletTestVectors = []struct {
	params     *dagconfig.Params
	keyChain   uint8
	index      uint32
	privateKey string
	address    string
}{
	{&dagconfig.MainnetParams, 0, 0, "a0d0c77f58b0c1b3a7862bef6cf87a48fa61c800b83bdc4c4c74453ae6daab34",
		"hoosat:qzf9ktzyqsjmjxasjz8axdmlrcnjz7vqw5fhaza7zcwt5qx2knwdwqggfna4x"},
	{&dagconfig.MainnetParams, 0, 1, "5c22b0a9b1269a32a02ee1b6b22c3ac4629817ad2b170ea3c9b321c5bee11cd1",
		"hoosat:qqzmggzj5turm9mld6sgqahnxcz54kp7zxcmx6v337lfq7tzpymwwmzh9ugt2"},
	{&dagconfig.MainnetParams, 1, 0, "40492a5a688f299a1b41fb57b997afe63de958e23f0363ea944e1f6ce4c1b94f",
		"hoosat:qzhh4mvycttxdze7sqxfrhqmsd5xz8j0s6mmf2kj547782p84lst22scqza4q"},
	{&dagconfig.MainnetParams, 0, 255, "f3a88a59e5ed9741a27870d22c813e3d885f78413acbf05fd28a49e22d26b3ac",
		"hoosat:qrn8vvja8jvdcq3jtpxe44ruccfnmy68z7fq7fxuhtlrj237ye4kqtwksxksj"},
	{&dagconfig.TestnetParams, 0, 0, "a0d0c77f58b0c1b3a7862bef6cf87a48fa61c800b83bdc4c4c74453ae6daab34",
		"hoosattest:qzf9ktzyqsjmjxasjz8axdmlrcnjz7vqw5fhaza7zcwt5qx2knwdwtpsareux"},
	{&dagconfig.TestnetParams, 1, 0, "40492a5a688f299a1b41fb57b997afe63de958e23f0363ea944e1f6ce4c1b94f",
		"hoosattest:qzhh4mvycttxdze7sqxfrhqmsd5xz8j0s6mmf2kj547782p84lst2peq5jeuq"},
}

// Exports of {privKey: webWalletTestMasterKey, seedPhrase: webWalletTestMnemonic} made with the web
// wallet's Crypto.encrypt and the password "correct horse", under crypto-js 4.1.1 (PBKDF2 defaults to
// SHA-1) and 4.2.0 (PBKDF2 defaults to SHA-256).
const (
	webWalletTestExportPassword   = "correct horse"
	webWalletTestExportCryptoJS41 = "004803d7d0b5adcead78a516c5f99db43df4efd3571f999dddd37a43d17eee9ac248a4bbf4a90d13550e2a6066e68e0138bebc46263de686e66f11cde93cea065628de08ce5dad4040d2bb3147fd19d4aa8fe8912da6f51dfaefa1c860132c86c90d33046d036b4bb35bf84ba95e26710e3e3cd20cabd2ea38d74a253962cc1636c1c39a3dae0484a7c779f9cc5ef2bb4106fca9cf7fb1da48fcc6c2582da495f8cdbdfc0ba00c1b8b932b6117eb0c7d54a9b10b8823cca7071ebac5d2ca59539ab36454e8ecb8f47161fa0701417e9bedcfa45683744317805c7016c34d1fbe6645c9a0bf4d44ba4e71c0d0953c369488c6c000326a95a809e4ae0feb4c0ee886d189e8f7000163bc2deb1e1d6580700032f95778c8e9513c6d294ee7d4a3b72bc9"
	webWalletTestExportCryptoJS42 = "00480991b7453da2ac79d09029b297c187ae982b8d81e0e68ac6314cdff91f888aa7d5a25f8a3ae4ea9ef1567c90ccec79dc4ca0a5be8490e66c7960ec8a0e077a44ae5c4246080488a6808be8512c2cca1b28f9032a2d38439f54a3a089c0eea073e78223f3be1161beb02de00d8a21f35bca250deeb2d797042ad84101b97f4119a126a1f472ebdc8f28145594045c936b8316ea63cba386b909b5674e4001311ff60bc6a4689237c11cd9f527838779889e3b7926a735b4ec258aaa6df0fc9c9dfcce28a36fe190fe70863eeb7967dca8249968fd3a3188e9fc956029949feff011b7edcccec5f5830cf7aad0a8f35c2b500032ee12a686fd16c58abb3a345d7fb5f18b000169c3131ed35a826df00032ca19d9e2c4ad4bfc7e8c44f24803e654"
)

// TestWebWalletKeysMatchTheWebWallet pins that a web wallet imported by its mnemonic or by its master key
// gives the same keys and addresses the web wallet itself derives.
func TestWebWalletKeysMatchTheWebWallet(t *testing.T) {
	for _, secret := range []string{webWalletTestMnemonic, "  abandon  abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about\n", webWalletTestMasterKey} {
		for _, vector := range webWalletTestVectors {
			path := libhtnwallet.WebWalletPath(vector.keyChain, vector.index)
			keys, err := libhtnwallet.ImportedKeysFromSecret(vector.params, libhtnwallet.ImportedKeyTypeHTNWebWallet, secret, []string{path})
			if err != nil {
				t.Fatalf("ImportedKeysFromSecret(%q, %s): %+v", secret, path, err)
			}
			privateKey := keys[0].PrivateKey().Serialize()
			if hex.EncodeToString(privateKey[:]) != vector.privateKey {
				t.Fatalf("%s from %q: got private key %x, want %s", path, secret, privateKey[:], vector.privateKey)
			}

			extendedPublicKey, err := libhtnwallet.ImportedKeyExtendedPublicKey(keys[0])
			if err != nil {
				t.Fatalf("ImportedKeyExtendedPublicKey: %+v", err)
			}
			address, err := libhtnwallet.ImportedKeyAddress(vector.params, extendedPublicKey, libhtnwallet.SingleSigAddressTypeP2PK)
			if err != nil {
				t.Fatalf("ImportedKeyAddress: %+v", err)
			}
			if address.String() != vector.address {
				t.Fatalf("%s from %q: got address %s, want %s", path, secret, address, vector.address)
			}
		}
	}
}

func TestWebWalletPathMatchesTheWebWallet(t *testing.T) {
	if got := libhtnwallet.WebWalletPath(libhtnwallet.InternalKeychain, 7); got != "m/44'/972/0'/1'/7'" {
		t.Fatalf("got %s, want the web wallet's m/44'/972/0'/1'/7'", got)
	}
}

func TestWebWalletMasterKeyRejectsOtherSecrets(t *testing.T) {
	for _, secret := range []string{
		"",
		"abandon abandon abandon",
		"abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon", // bad checksum
		"not a key",
	} {
		if _, err := libhtnwallet.WebWalletMasterKey(&dagconfig.MainnetParams, secret); err == nil {
			t.Fatalf("WebWalletMasterKey(%q) succeeded", secret)
		}
	}

	masterKey, err := libhtnwallet.WebWalletMasterKey(&dagconfig.MainnetParams, webWalletTestMnemonic)
	if err != nil {
		t.Fatalf("WebWalletMasterKey: %+v", err)
	}
	child, err := masterKey.DeriveFromPath("m/44'")
	if err != nil {
		t.Fatalf("DeriveFromPath: %+v", err)
	}
	if _, err := libhtnwallet.WebWalletMasterKey(&dagconfig.MainnetParams, child.String()); err == nil {
		t.Fatalf("WebWalletMasterKey accepted a non-master extended key")
	}
	publicKey, err := masterKey.Public()
	if err != nil {
		t.Fatalf("Public: %+v", err)
	}
	if _, err := libhtnwallet.WebWalletMasterKey(&dagconfig.MainnetParams, publicKey.String()); err == nil {
		t.Fatalf("WebWalletMasterKey accepted an extended public key")
	}
}

func TestDecryptWebWalletExport(t *testing.T) {
	for name, export := range map[string]string{
		"crypto-js 4.1": webWalletTestExportCryptoJS41,
		"crypto-js 4.2": webWalletTestExportCryptoJS42,
	} {
		decrypted, err := libhtnwallet.DecryptWebWalletExport(" "+export+"\n", webWalletTestExportPassword)
		if err != nil {
			t.Fatalf("%s: DecryptWebWalletExport: %+v", name, err)
		}
		if decrypted.SeedPhrase != webWalletTestMnemonic || decrypted.PrivateKey != webWalletTestMasterKey {
			t.Fatalf("%s: decrypted %+v", name, decrypted)
		}

		_, err = libhtnwallet.DecryptWebWalletExport(export, "wrong horse")
		if err == nil || !strings.Contains(err.Error(), "password is wrong") {
			t.Fatalf("%s: decrypting with a wrong password: got %v", name, err)
		}
	}

	for _, malformed := range []string{"", "0001", "00010x", "00004abcd", "00032" + strings.Repeat("ab", 16)} {
		if _, err := libhtnwallet.DecryptWebWalletExport(malformed, webWalletTestExportPassword); err == nil {
			t.Fatalf("DecryptWebWalletExport(%q) succeeded", malformed)
		}
	}
}

// TestImportedPrivateKeyAddressMatchesGenkeypair pins that an imported genkeypair key is tracked at the
// address genkeypair prints for it.
func TestImportedPrivateKeyAddressMatchesGenkeypair(t *testing.T) {
	params := &dagconfig.MainnetParams
	for range 20 {
		// What genkeypair does.
		privateKey, publicKey, err := libhtnwallet.CreateKeyPair(false)
		if err != nil {
			t.Fatalf("CreateKeyPair: %+v", err)
		}
		genkeypairAddress, err := util.NewAddressPublicKey(publicKey, params.Prefix)
		if err != nil {
			t.Fatalf("NewAddressPublicKey: %+v", err)
		}

		keys, err := libhtnwallet.ImportedKeysFromSecret(params, libhtnwallet.ImportedKeyTypePrivateKey, hex.EncodeToString(privateKey), nil)
		if err != nil {
			t.Fatalf("ImportedKeysFromSecret: %+v", err)
		}
		extendedPublicKey, err := libhtnwallet.ImportedKeyExtendedPublicKey(keys[0])
		if err != nil {
			t.Fatalf("ImportedKeyExtendedPublicKey: %+v", err)
		}
		addresses, err := libhtnwallet.ImportedKeyAddresses(params, extendedPublicKey)
		if err != nil {
			t.Fatalf("ImportedKeyAddresses: %+v", err)
		}
		if len(addresses) != 3 || addresses[0].String() != genkeypairAddress.String() {
			t.Fatalf("imported key addresses %v, want genkeypair's %s first", addresses, genkeypairAddress)
		}
	}

	for _, malformed := range []string{"", "zz", "00", strings.Repeat("00", 32), strings.Repeat("ab", 33)} {
		if _, err := libhtnwallet.ImportedKeyFromHexPrivateKey(params, malformed); err == nil {
			t.Fatalf("ImportedKeyFromHexPrivateKey(%q) succeeded", malformed)
		}
	}
}

// TestSignWithImportedKeys spends, in one transaction accepted by consensus, a coin on a web wallet key's
// P2PK address and a coin on a genkeypair key's P2PKH address, signed with the imported keys alone, as an
// imported wallet signs. It also pins that a signer matching none of a transaction's keys is refused.
func TestSignWithImportedKeys(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		params := &consensusConfig.Params
		consensusConfig.BlockCoinbaseMaturity = 0
		tc, teardown, err := consensus.NewFactory().NewTestConsensus(consensusConfig, "TestSignWithImportedKeys")
		if err != nil {
			t.Fatalf("Error setting up tc: %+v", err)
		}
		defer teardown(false)

		webWalletKeys, err := libhtnwallet.ImportedKeysFromSecret(params, libhtnwallet.ImportedKeyTypeHTNWebWallet,
			webWalletTestMnemonic, []string{libhtnwallet.WebWalletPath(libhtnwallet.ExternalKeychain, 2)})
		if err != nil {
			t.Fatalf("ImportedKeysFromSecret: %+v", err)
		}
		rawPrivateKey, _, err := libhtnwallet.CreateKeyPair(false)
		if err != nil {
			t.Fatalf("CreateKeyPair: %+v", err)
		}
		rawKeys, err := libhtnwallet.ImportedKeysFromSecret(params, libhtnwallet.ImportedKeyTypePrivateKey,
			hex.EncodeToString(rawPrivateKey), nil)
		if err != nil {
			t.Fatalf("ImportedKeysFromSecret: %+v", err)
		}

		webWalletPublicKey, err := libhtnwallet.ImportedKeyExtendedPublicKey(webWalletKeys[0])
		if err != nil {
			t.Fatalf("ImportedKeyExtendedPublicKey: %+v", err)
		}
		rawPublicKey, err := libhtnwallet.ImportedKeyExtendedPublicKey(rawKeys[0])
		if err != nil {
			t.Fatalf("ImportedKeyExtendedPublicKey: %+v", err)
		}
		importedKeys := map[string]*bip32.ExtendedKey{webWalletPublicKey: webWalletKeys[0], rawPublicKey: rawKeys[0]}

		webWalletAddress, err := libhtnwallet.ImportedKeyAddress(params, webWalletPublicKey, libhtnwallet.SingleSigAddressTypeP2PK)
		if err != nil {
			t.Fatalf("ImportedKeyAddress: %+v", err)
		}
		rawP2PKHAddress, err := libhtnwallet.ImportedKeyAddress(params, rawPublicKey, libhtnwallet.SingleSigAddressTypeP2PKH)
		if err != nil {
			t.Fatalf("ImportedKeyAddress: %+v", err)
		}

		// A block's coinbase pays the miner of its selected parent, so a chain of blocks mined to each
		// address, plus one more, funds every address.
		fundedAddresses := []util.Address{webWalletAddress, rawP2PKHAddress}
		tipHash := consensusConfig.GenesisHash
		for _, address := range append(fundedAddresses, nil) {
			var coinbaseData *externalapi.DomainCoinbaseData
			if address != nil {
				scriptPublicKey, err := txscript.PayToAddrScript(address)
				if err != nil {
					t.Fatalf("PayToAddrScript: %+v", err)
				}
				coinbaseData = &externalapi.DomainCoinbaseData{ScriptPublicKey: scriptPublicKey}
			}
			tipHash, _, err = tc.AddBlock([]*externalapi.DomainHash{tipHash}, coinbaseData, nil)
			if err != nil {
				t.Fatalf("AddBlock: %+v", err)
			}
		}

		utxos := make([]*libhtnwallet.UTXO, len(fundedAddresses))
		blockHash := tipHash
		for i := len(fundedAddresses) - 1; i >= 0; i-- {
			block, _, err := tc.GetBlock(blockHash)
			if err != nil {
				t.Fatalf("GetBlock: %+v", err)
			}
			utxos[i] = coinbaseUTXOPaying(t, block, fundedAddresses[i])
			blockHash = block.Header.DirectParents()[0]
		}
		utxos[0].ImportedExtendedPublicKey = webWalletPublicKey
		utxos[1].ImportedExtendedPublicKey = rawPublicKey

		// An imported wallet has no extended public keys of its own.
		unsignedTransaction, err := libhtnwallet.CreateUnsignedTransaction(nil, 1,
			[]*libhtnwallet.Payment{{Address: webWalletAddress, Amount: 1000}}, utxos, nil)
		if err != nil {
			t.Fatalf("CreateUnsignedTransaction: %+v", err)
		}

		mnemonic, err := libhtnwallet.CreateMnemonic()
		if err != nil {
			t.Fatalf("CreateMnemonic: %+v", err)
		}
		_, err = libhtnwallet.Sign(params, []string{mnemonic}, unsignedTransaction, false)
		if err == nil || !strings.Contains(err.Error(), "doesn't match") {
			t.Fatalf("signing imported coins with an unrelated mnemonic: got %v", err)
		}
		otherKeys := map[string]*bip32.ExtendedKey{rawPublicKey + "x": rawKeys[0]}
		_, err = libhtnwallet.SignWithImportedKeys(params, nil, otherKeys, unsignedTransaction, false)
		if err == nil || !strings.Contains(err.Error(), "None of the imported keys") {
			t.Fatalf("signing with unrelated imported keys: got %v", err)
		}

		signedTransaction, err := libhtnwallet.SignWithImportedKeys(params, nil, importedKeys, unsignedTransaction, false)
		if err != nil {
			t.Fatalf("SignWithImportedKeys: %+v", err)
		}
		transaction, err := libhtnwallet.ExtractTransaction(signedTransaction, false)
		if err != nil {
			t.Fatalf("ExtractTransaction: %+v", err)
		}

		_, virtualChangeSet, err := tc.AddBlock([]*externalapi.DomainHash{tipHash}, nil, []*externalapi.DomainTransaction{transaction})
		if err != nil {
			t.Fatalf("AddBlock: %+v", err)
		}
		if !virtualChangeSet.VirtualUTXODiff.ToAdd().Contains(&externalapi.DomainOutpoint{
			TransactionID: *consensushashing.TransactionID(transaction),
			Index:         0,
		}) {
			t.Fatalf("the transaction spending imported coins wasn't accepted in the DAG")
		}
	})
}

func coinbaseUTXOPaying(t *testing.T, block *externalapi.DomainBlock, address util.Address) *libhtnwallet.UTXO {
	t.Helper()
	scriptPublicKey, err := txscript.PayToAddrScript(address)
	if err != nil {
		t.Fatalf("PayToAddrScript: %+v", err)
	}
	coinbase := block.Transactions[0]
	for index, output := range coinbase.Outputs {
		if output.ScriptPublicKey.Equal(scriptPublicKey) {
			return &libhtnwallet.UTXO{
				Outpoint: &externalapi.DomainOutpoint{
					TransactionID: *consensushashing.TransactionID(coinbase),
					Index:         uint32(index),
				},
				UTXOEntry: utxo.NewUTXOEntry(output.Value, output.ScriptPublicKey, true, 0),
			}
		}
	}
	t.Fatalf("no coinbase output of block %s pays %s", consensushashing.BlockHash(block), address)
	return nil
}
