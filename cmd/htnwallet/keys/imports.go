package keys

import (
	"crypto/subtle"
	"encoding/hex"

	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/libhtnwallet"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/libhtnwallet/bip32"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
	"github.com/pkg/errors"
)

type importedKeyJSON struct {
	Path              string `json:"path,omitempty"`
	ExtendedPublicKey string `json:"publicKey"`
}

type importJSON struct {
	Type   string             `json:"type"`
	Cipher string             `json:"cipher"`
	Salt   string             `json:"salt"`
	Keys   []*importedKeyJSON `json:"keys"`
}

// ImportedKey is one key of an Import: everything about it the daemon needs without the password.
type ImportedKey struct {
	// Type is the type of the import the key belongs to (libhtnwallet.ImportedKeyType*).
	Type string
	// Path is the key's derivation path in the wallet it was imported from, or empty for a lone key.
	Path string
	// ExtendedPublicKey identifies the key; see libhtnwallet.ImportedKeyExtendedPublicKey.
	ExtendedPublicKey string
}

// Import is a wallet imported from elsewhere - a lone private key, or the HTN web wallet - as the secret
// it was imported from, encrypted with the keys file's password, and the public keys of the keys it
// holds.
//
// A keys file holds either an Import or the wallet's own mnemonics and extended public keys, never both:
// an imported wallet is a wallet of its own, not keys added to an htnwallet wallet.
type Import struct {
	Type            string
	EncryptedSecret *EncryptedMnemonic
	Keys            []*ImportedKey
}

func importToJSON(imported *Import) *importJSON {
	if imported == nil {
		return nil
	}
	keysJSON := make([]*importedKeyJSON, len(imported.Keys))
	for i, key := range imported.Keys {
		keysJSON[i] = &importedKeyJSON{Path: key.Path, ExtendedPublicKey: key.ExtendedPublicKey}
	}
	return &importJSON{
		Type:   imported.Type,
		Cipher: hex.EncodeToString(imported.EncryptedSecret.cipher),
		Salt:   hex.EncodeToString(imported.EncryptedSecret.salt),
		Keys:   keysJSON,
	}
}

func importFromJSON(importJSON *importJSON) (*Import, error) {
	if importJSON == nil {
		return nil, nil
	}
	cipher, err := hex.DecodeString(importJSON.Cipher)
	if err != nil {
		return nil, err
	}
	salt, err := hex.DecodeString(importJSON.Salt)
	if err != nil {
		return nil, err
	}
	if len(importJSON.Keys) == 0 {
		return nil, errors.New("the imported wallet in the keys file has no keys")
	}
	keys := make([]*ImportedKey, len(importJSON.Keys))
	for i, keyJSON := range importJSON.Keys {
		keys[i] = &ImportedKey{Type: importJSON.Type, Path: keyJSON.Path, ExtendedPublicKey: keyJSON.ExtendedPublicKey}
	}
	return &Import{
		Type:            importJSON.Type,
		EncryptedSecret: &EncryptedMnemonic{cipher: cipher, salt: salt},
		Keys:            keys,
	}, nil
}

// NewImportedFile returns a keys file holding a wallet imported from secret, of the given type, with
// the keys at paths (see libhtnwallet.ImportedKeysFromSecret). The secret is encrypted with password; an
// empty password is asked for, twice. The caller sets the file's path and saves it.
//
// The imported keys are single Schnorr keys, so the file is a single-signer Schnorr wallet.
func NewImportedFile(params *dagconfig.Params, importType string, secret string, paths []string,
	password string,
) (*File, error) {
	importedKeys, err := libhtnwallet.ImportedKeysFromSecret(params, importType, secret, paths)
	if err != nil {
		return nil, err
	}
	if len(importedKeys) == 0 {
		return nil, errors.New("nothing to import")
	}

	keys := make([]*ImportedKey, len(importedKeys))
	for i, importedKey := range importedKeys {
		extendedPublicKey, err := libhtnwallet.ImportedKeyExtendedPublicKey(importedKey)
		if err != nil {
			return nil, err
		}
		path := ""
		if importType != libhtnwallet.ImportedKeyTypePrivateKey {
			path = paths[i]
		}
		keys[i] = &ImportedKey{Type: importType, Path: path, ExtendedPublicKey: extendedPublicKey}
	}

	passwordBytes := []byte(password)
	if len(passwordBytes) == 0 {
		passwordBytes = []byte(GetPassword("Enter password for the key file:"))
		confirmPassword := []byte(GetPassword("Confirm password:"))
		if subtle.ConstantTimeCompare(passwordBytes, confirmPassword) != 1 {
			return nil, errors.New("Passwords are not identical")
		}
	}

	encryptedSecret, err := encryptMnemonic(secret, passwordBytes)
	if err != nil {
		return nil, err
	}

	return &File{
		Version:           LastVersion,
		NumThreads:        defaultNumThreads,
		MinimumSignatures: 1,
		Imported: &Import{
			Type:            importType,
			EncryptedSecret: encryptedSecret,
			Keys:            keys,
		},
	}, nil
}

// IsImported returns whether the keys file holds an imported wallet rather than an htnwallet one.
func (d *File) IsImported() bool {
	return d.Imported != nil
}

// ImportedKeys returns the keys of the imported wallet, or nothing for an htnwallet wallet.
func (d *File) ImportedKeys() []*ImportedKey {
	if d.Imported == nil {
		return nil
	}
	return d.Imported.Keys
}

// DecryptImportedSecret decrypts the secret the imported wallet was imported from.
func (d *File) DecryptImportedSecret(password string) (string, error) {
	if d.Imported == nil {
		return "", errors.New("the keys file does not hold an imported wallet")
	}
	passwordBytes := []byte(password)
	// numThreads only differs from the default for version 0 files, which predate imported wallets.
	return decryptMnemonic(defaultNumThreads, d.Imported.EncryptedSecret, passwordBytes)
}

// DecryptImportedKeys decrypts the imported wallet and returns its keys, mapped from their extended public
// keys as libhtnwallet.SignWithImportedKeys takes them. It returns nothing for an htnwallet wallet.
func (d *File) DecryptImportedKeys(params *dagconfig.Params, password string) (map[string]*bip32.ExtendedKey, error) {
	if d.Imported == nil {
		return nil, nil
	}

	secret, err := d.DecryptImportedSecret(password)
	if err != nil {
		return nil, err
	}

	paths := make([]string, len(d.Imported.Keys))
	for i, key := range d.Imported.Keys {
		paths[i] = key.Path
	}
	keys, err := libhtnwallet.ImportedKeysFromSecret(params, d.Imported.Type, secret, paths)
	if err != nil {
		return nil, err
	}

	importedKeys := make(map[string]*bip32.ExtendedKey, len(keys))
	for i, key := range keys {
		extendedPublicKey, err := libhtnwallet.ImportedKeyExtendedPublicKey(key)
		if err != nil {
			return nil, err
		}
		if extendedPublicKey != d.Imported.Keys[i].ExtendedPublicKey {
			return nil, errors.Errorf("imported key #%d does not match its public key", i+1)
		}
		importedKeys[extendedPublicKey] = key
	}
	return importedKeys, nil
}

// DecryptSigningKeys decrypts what the wallet signs with: the mnemonics of an htnwallet wallet, or the
// keys of an imported one mapped from their extended public keys.
func (d *File) DecryptSigningKeys(params *dagconfig.Params, password string) ([]string, map[string]*bip32.ExtendedKey, error) {
	if d.IsImported() {
		importedKeys, err := d.DecryptImportedKeys(params, password)
		if err != nil {
			return nil, nil, err
		}
		return nil, importedKeys, nil
	}

	mnemonics, err := d.DecryptMnemonics(password)
	if err != nil {
		return nil, nil, err
	}
	return mnemonics, nil, nil
}
