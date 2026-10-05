package libhtnwallet

import (
	"crypto/aes"
	"crypto/md5"
	"crypto/pbkdf2"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"hash"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/pkg/errors"
)

// WebWalletExport is the content of an HTN web wallet export: what its Wallet.export() encrypts.
type WebWalletExport struct {
	// PrivateKey is the wallet's master extended private key - the key every address is derived from.
	PrivateKey string `json:"privKey"`
	// SeedPhrase is the wallet's mnemonic.
	SeedPhrase string `json:"seedPhrase"`
}

// webWalletExportPBKDF2Hashes are the hashes the web wallet's key derivation may have used. It derives
// its AES passphrase with crypto-js PBKDF2 without naming a hash, and crypto-js changed that default from
// SHA-1 to SHA-256 in version 4.2.0. The export carries no record of which one made it, so both are tried.
var webWalletExportPBKDF2Hashes = []func() hash.Hash{sha256.New, sha1.New}

// DecryptWebWalletExport decrypts an HTN web wallet export (the string its Wallet.export(password)
// returns) with the password it was exported with.
//
// The format, from htn-wallet's wallet/crypto.ts: the export is four length-prefixed hex fields - the
// ciphertext, an IV, an OpenSSL salt and a PBKDF2 salt - each preceded by its length as five decimal
// digits. The PBKDF2 output (1000 iterations, 64 bytes) is hex encoded and used as a crypto-js
// *passphrase*, which crypto-js stretches with OpenSSL's EVP_BytesToKey (MD5, the OpenSSL salt) into an
// AES-256 key and the IV actually used; the stored IV is ignored. The cipher is AES-256 in CFB mode with
// ANSI X.923 padding, and the plaintext is the JSON of WebWalletExport.
//
// Nothing authenticates the ciphertext, so a wrong password is only noticed by the plaintext not being
// that JSON.
func DecryptWebWalletExport(export string, password string) (*WebWalletExport, error) {
	fields, err := parseWebWalletExportFields(strings.TrimSpace(export))
	if err != nil {
		return nil, err
	}
	if len(fields) < 4 {
		return nil, errors.Errorf("a web wallet export has 4 fields, but got %d", len(fields))
	}

	ciphertext, err := hex.DecodeString(fields[0])
	if err != nil {
		return nil, errors.Wrap(err, "invalid web wallet export ciphertext")
	}
	openSSLSalt, err := hex.DecodeString(fields[2])
	if err != nil {
		return nil, errors.Wrap(err, "invalid web wallet export salt")
	}
	pbkdf2Salt, err := hex.DecodeString(fields[3])
	if err != nil {
		return nil, errors.Wrap(err, "invalid web wallet export password salt")
	}
	if len(ciphertext) == 0 || len(ciphertext)%aes.BlockSize != 0 {
		return nil, errors.Errorf("the web wallet export ciphertext is %d bytes, not a whole number of blocks",
			len(ciphertext))
	}

	for _, pbkdf2Hash := range webWalletExportPBKDF2Hashes {
		passphraseKey, err := pbkdf2.Key(pbkdf2Hash, password, pbkdf2Salt, 1000, 64)
		if err != nil {
			return nil, err
		}
		passphrase := []byte(hex.EncodeToString(passphraseKey))

		plaintext, err := decryptCryptoJSPassphraseAES256CFB(ciphertext, passphrase, openSSLSalt)
		if err != nil {
			continue
		}

		export := &WebWalletExport{}
		if !utf8.Valid(plaintext) || json.Unmarshal(plaintext, export) != nil {
			continue
		}
		if export.PrivateKey == "" && export.SeedPhrase == "" {
			continue
		}
		return export, nil
	}

	return nil, errors.New("could not decrypt the web wallet export: the password is wrong, " +
		"or this is not an HTN web wallet export")
}

func parseWebWalletExportFields(export string) ([]string, error) {
	const lengthDigits = 5
	var fields []string
	for len(export) > 0 {
		if len(export) < lengthDigits {
			return nil, errors.New("the web wallet export is truncated")
		}
		length, err := strconv.Atoi(export[:lengthDigits])
		if err != nil || length < 0 {
			return nil, errors.New("the web wallet export is malformed")
		}
		export = export[lengthDigits:]
		if len(export) < length {
			return nil, errors.New("the web wallet export is truncated")
		}
		fields = append(fields, export[:length])
		export = export[length:]
	}
	return fields, nil
}

// decryptCryptoJSPassphraseAES256CFB decrypts what crypto-js AES.encrypt produces when given a passphrase
// string, CFB mode and ANSI X.923 padding.
func decryptCryptoJSPassphraseAES256CFB(ciphertext, passphrase, salt []byte) ([]byte, error) {
	const keySize, ivSize = 32, aes.BlockSize
	keyAndIV := evpBytesToKeyMD5(passphrase, salt, keySize+ivSize)

	block, err := aes.NewCipher(keyAndIV[:keySize])
	if err != nil {
		return nil, err
	}

	// crypto-js's CFB is full-block CFB (CFB-128): each keystream block is the encryption of the previous
	// ciphertext block, starting from the IV.
	plaintext := make([]byte, len(ciphertext))
	feedback := keyAndIV[keySize:]
	keystream := make([]byte, aes.BlockSize)
	for offset := 0; offset < len(ciphertext); offset += aes.BlockSize {
		block.Encrypt(keystream, feedback)
		for i := range aes.BlockSize {
			plaintext[offset+i] = ciphertext[offset+i] ^ keystream[i]
		}
		feedback = ciphertext[offset : offset+aes.BlockSize]
	}

	// ANSI X.923: the last byte is the padding length, and the padding before it is zeros.
	paddingLength := int(plaintext[len(plaintext)-1])
	if paddingLength == 0 || paddingLength > aes.BlockSize || paddingLength > len(plaintext) {
		return nil, errors.New("invalid padding")
	}
	for _, paddingByte := range plaintext[len(plaintext)-paddingLength : len(plaintext)-1] {
		if paddingByte != 0 {
			return nil, errors.New("invalid padding")
		}
	}
	return plaintext[:len(plaintext)-paddingLength], nil
}

// evpBytesToKeyMD5 is OpenSSL's EVP_BytesToKey with MD5 and one iteration, the key derivation crypto-js
// uses for a passphrase.
func evpBytesToKeyMD5(passphrase, salt []byte, length int) []byte {
	var derived, previous []byte
	for len(derived) < length {
		digest := md5.New()
		digest.Write(previous)
		digest.Write(passphrase)
		digest.Write(salt)
		previous = digest.Sum(nil)
		derived = append(derived, previous...)
	}
	return derived[:length]
}
