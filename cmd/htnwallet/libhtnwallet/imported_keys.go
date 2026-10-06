package libhtnwallet

import (
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/libhtnwallet/bip32"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
	"github.com/HoosatNetwork/HTND/v2/util"
	"github.com/pkg/errors"
	"github.com/tyler-smith/go-bip39"
)

// Imported key types. An imported key is a key the wallet can sign with that is not part of its own key
// tree, so the daemon cannot derive it from the wallet's extended public keys.
const (
	// ImportedKeyTypePrivateKey is a single raw Schnorr private key, such as the one genkeypair prints.
	ImportedKeyTypePrivateKey = "privateKey"
	// ImportedKeyTypeHTNWebWallet is a range of keys from the HTN web wallet
	// (github.com/HoosatNetwork/htn-wallet), derived from its mnemonic or master extended private key.
	ImportedKeyTypeHTNWebWallet = "htnWebWallet"
)

// ImportedKeyDerivationPath is the derivation path recorded in a partially signed transaction for an
// input spent by an imported key. The input's public key is the imported key itself, so nothing is
// derived from it.
const ImportedKeyDerivationPath = "m"

// webWalletCoinType is the coin type in the HTN web wallet's derivation path. Unlike the rest of the
// path it is not hardened - the web wallet derives m/44'/972/0'/<keyChain>'/<index>'.
const webWalletCoinType = 972

// WebWalletPath returns the HTN web wallet's derivation path for the key at index in keyChain
// (ExternalKeychain for receive addresses, InternalKeychain for change addresses). Every index after the
// coin type is hardened, which is why these keys cannot be derived from an extended public key and have
// to be imported one by one.
func WebWalletPath(keyChain uint8, index uint32) string {
	return fmt.Sprintf("m/%d'/%d/0'/%d'/%d'", SingleSignerPurpose, webWalletCoinType, keyChain, index)
}

// NormalizeMnemonic collapses the whitespace in a typed mnemonic to single spaces. The BIP39 seed is
// computed from the exact string, and the web wallet only ever accepted single-space separated words.
func NormalizeMnemonic(mnemonic string) string {
	return strings.Join(strings.Fields(mnemonic), " ")
}

// WebWalletMasterKey returns the HTN web wallet's master extended private key from its secret: either its
// BIP39 mnemonic or its serialized master extended private key (the "privKey" of its export).
//
// The web wallet turns a mnemonic into its master key the standard way - a BIP39 seed with an empty
// passphrase, then a BIP32 master key - so the same mnemonic gives the same master key here.
func WebWalletMasterKey(params *dagconfig.Params, secret string) (*bip32.ExtendedKey, error) {
	version, err := versionFromParams(params)
	if err != nil {
		return nil, err
	}

	mnemonic := NormalizeMnemonic(secret)
	if bip39.IsMnemonicValid(mnemonic) {
		return bip32.NewMaster(bip39.NewSeed(mnemonic, ""), version)
	}

	masterKey, err := bip32.DeserializeExtendedKey(strings.TrimSpace(secret))
	if err != nil {
		return nil, errors.New("the web wallet secret is neither a valid mnemonic nor an extended private key")
	}
	if !masterKey.IsPrivate() {
		return nil, errors.New("the web wallet secret is an extended public key; its private key is needed")
	}
	if masterKey.Depth != 0 {
		return nil, errors.Errorf("the web wallet secret is an extended private key at depth %d; "+
			"its master key (depth 0) is needed", masterKey.Depth)
	}
	// Only the version prefix differs between networks; the key material is the same.
	masterKey.Version = version
	return masterKey, nil
}

// ImportedKeyFromPrivateKey returns the imported key holding the given 32-byte Schnorr private key.
func ImportedKeyFromPrivateKey(params *dagconfig.Params, privateKey []byte) (*bip32.ExtendedKey, error) {
	version, err := versionFromParams(params)
	if err != nil {
		return nil, err
	}
	return bip32.NewMasterFromPrivateKey(privateKey, version)
}

// ImportedKeyFromHexPrivateKey returns the imported key holding the hex encoded Schnorr private key, as
// genkeypair prints it.
func ImportedKeyFromHexPrivateKey(params *dagconfig.Params, privateKeyHex string) (*bip32.ExtendedKey, error) {
	privateKey, err := hex.DecodeString(strings.TrimSpace(privateKeyHex))
	if err != nil {
		return nil, errors.Wrap(err, "the private key is not valid hex")
	}
	if len(privateKey) != 32 {
		return nil, errors.Errorf("a private key is 32 bytes, but got %d", len(privateKey))
	}
	return ImportedKeyFromPrivateKey(params, privateKey)
}

// ImportedKeysFromSecret derives the imported keys an import's secret holds. For ImportedKeyTypePrivateKey
// the secret is the hex encoded private key and paths is ignored. For ImportedKeyTypeHTNWebWallet the
// secret is the web wallet's mnemonic or master extended private key, and one key is returned for each
// path, in order.
func ImportedKeysFromSecret(params *dagconfig.Params, importType string, secret string, paths []string) (
	[]*bip32.ExtendedKey, error,
) {
	switch importType {
	case ImportedKeyTypePrivateKey:
		key, err := ImportedKeyFromHexPrivateKey(params, secret)
		if err != nil {
			return nil, err
		}
		return []*bip32.ExtendedKey{key}, nil
	case ImportedKeyTypeHTNWebWallet:
		masterKey, err := WebWalletMasterKey(params, secret)
		if err != nil {
			return nil, err
		}
		return webWalletKeysAtPaths(params, masterKey, paths)
	}
	return nil, errors.Errorf("unknown imported key type %q", importType)
}

// webWalletKeysAtPaths derives the key at each path and returns it as an imported key. The paths of one
// key chain share everything but their last index, so each parent is derived once.
//
// The web wallet's path has one non-hardened step, the coin type, and its key library derives
// non-hardened children from the parent's x-only public key rather than BIP32's compressed one
// (bip32.ExtendedKey.ChildXOnly). Standard BIP32 derivation gives different keys from the same mnemonic.
func webWalletKeysAtPaths(params *dagconfig.Params, masterKey *bip32.ExtendedKey, paths []string) (
	[]*bip32.ExtendedKey, error,
) {
	parents := make(map[string]*bip32.ExtendedKey)
	keys := make([]*bip32.ExtendedKey, len(paths))
	for i, path := range paths {
		separator := strings.LastIndex(path, "/")
		if separator < 0 {
			return nil, errors.Errorf("invalid web wallet derivation path %q", path)
		}
		parentPath, childPath := path[:separator], "m"+path[separator:]

		parent, ok := parents[parentPath]
		if !ok {
			var err error
			parent, err = masterKey.DeriveFromPathXOnly(parentPath)
			if err != nil {
				return nil, err
			}
			parents[parentPath] = parent
		}

		derivedKey, err := parent.DeriveFromPathXOnly(childPath)
		if err != nil {
			return nil, err
		}

		privateKey := derivedKey.PrivateKey().Serialize()
		keys[i], err = ImportedKeyFromPrivateKey(params, privateKey[:])
		if err != nil {
			return nil, err
		}
	}
	return keys, nil
}

// ImportedKeyExtendedPublicKey returns the extended public key that identifies an imported key: the key a
// partially signed transaction lists for the inputs it spends.
func ImportedKeyExtendedPublicKey(importedKey *bip32.ExtendedKey) (string, error) {
	publicKey, err := importedKey.Public()
	if err != nil {
		return "", err
	}
	return publicKey.String(), nil
}

// ImportedKeyAddresses returns every address an imported key can receive coins on - P2PK, P2PKH and
// P2SH-wrapped P2PKH, like a single-sig wallet address. The first is the P2PK address, which is the one
// both genkeypair and the HTN web wallet show.
func ImportedKeyAddresses(params *dagconfig.Params, extendedPublicKey string) ([]util.Address, error) {
	return WalletAddressesAtPath(params, []string{extendedPublicKey}, 1, ImportedKeyDerivationPath, false)
}

// ImportedKeyAddress returns an imported key's address of the given single-sig type.
func ImportedKeyAddress(params *dagconfig.Params, extendedPublicKey string, addressType SingleSigAddressType) (
	util.Address, error,
) {
	return AddressWithSingleSigAddressType(params, []string{extendedPublicKey}, 1, ImportedKeyDerivationPath,
		false, addressType)
}
