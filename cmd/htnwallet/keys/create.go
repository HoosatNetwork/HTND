package keys

import (
	"bufio"
	"crypto/rand"
	"crypto/subtle"
	"fmt"
	"os"

	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/libhtnwallet"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/utils"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
	"github.com/pkg/errors"
	"github.com/tyler-smith/go-bip39"
)

// CreateMnemonics generates `numKeys` number of mnemonics.
//
// mldsa44KeyPools holds an ML-DSA-44 key pool for each generated mnemonic, keyed by its extended public
// key: its single-sig keys for a single-sig wallet, its multisig cosigner keys for a multisig one.
func CreateMnemonics(params *dagconfig.Params, numKeys uint32, cmdLinePassword string, isMultisig bool) (
	encryptedPrivateKeys []*EncryptedMnemonic, extendedPublicKeys []string, mldsa44KeyPools map[string]*MLDSA44KeyPool, err error,
) {
	mnemonics := make([]string, numKeys)
	for i := range numKeys {
		var err error
		mnemonics[i], err = libhtnwallet.CreateMnemonic()
		if err != nil {
			return nil, nil, nil, err
		}
	}

	return encryptedMnemonicExtendedPublicKeyPairs(params, mnemonics, cmdLinePassword, isMultisig)
}

// ImportMnemonics imports a `numKeys` of mnemonics. See CreateMnemonics for mldsa44KeyPools.
func ImportMnemonics(params *dagconfig.Params, numKeys uint32, cmdLinePassword string, isMultisig bool) (
	encryptedPrivateKeys []*EncryptedMnemonic, extendedPublicKeys []string, mldsa44KeyPools map[string]*MLDSA44KeyPool, err error,
) {
	mnemonics := make([]string, numKeys)
	for i := range numKeys {
		fmt.Printf("Enter mnemonic #%d here:\n", i+1)
		reader := bufio.NewReader(os.Stdin)
		mnemonic, err := utils.ReadLine(reader)
		if err != nil {
			return nil, nil, nil, err
		}

		if !bip39.IsMnemonicValid(string(mnemonic)) {
			return nil, nil, nil, errors.Errorf("mnemonic is invalid")
		}

		mnemonics[i] = string(mnemonic)
	}
	return encryptedMnemonicExtendedPublicKeyPairs(params, mnemonics, cmdLinePassword, isMultisig)
}

func encryptedMnemonicExtendedPublicKeyPairs(params *dagconfig.Params, mnemonics []string, cmdLinePassword string, isMultisig bool) (
	encryptedPrivateKeys []*EncryptedMnemonic, extendedPublicKeys []string, mldsa44KeyPools map[string]*MLDSA44KeyPool, err error,
) {
	password := []byte(cmdLinePassword)
	if len(password) == 0 {

		password = []byte(GetPassword("Enter password for the key file:"))
		confirmPassword := []byte(GetPassword("Confirm password:"))

		if subtle.ConstantTimeCompare(password, confirmPassword) != 1 {
			return nil, nil, nil, errors.New("Passwords are not identical")
		}
	}

	encryptedPrivateKeys = make([]*EncryptedMnemonic, 0, len(mnemonics))
	extendedPublicKeys = make([]string, 0, len(mnemonics))

	for _, mnemonic := range mnemonics {
		extendedPublicKey, err := libhtnwallet.MasterPublicKeyFromMnemonic(params, mnemonic, isMultisig)
		if err != nil {
			return nil, nil, nil, err
		}

		extendedPublicKeys = append(extendedPublicKeys, extendedPublicKey)

		encryptedPrivateKey, err := encryptMnemonic(mnemonic, password)
		if err != nil {
			return nil, nil, nil, err
		}
		encryptedPrivateKeys = append(encryptedPrivateKeys, encryptedPrivateKey)
	}

	mldsa44KeyPools = make(map[string]*MLDSA44KeyPool, len(mnemonics))
	for i, mnemonic := range mnemonics {
		mldsa44KeyPools[extendedPublicKeys[i]], err = NewMLDSA44KeyPool(mnemonic, libhtnwallet.DefaultMLDSA44KeyPoolSize, isMultisig)
		if err != nil {
			return nil, nil, nil, err
		}
	}

	return encryptedPrivateKeys, extendedPublicKeys, mldsa44KeyPools, nil
}

func generateSalt() ([]byte, error) {
	salt := make([]byte, 16)
	_, err := rand.Read(salt)
	if err != nil {
		return nil, err
	}

	return salt, nil
}

func encryptMnemonic(mnemonic string, password []byte) (*EncryptedMnemonic, error) {
	mnemonicBytes := []byte(mnemonic)

	salt, err := generateSalt()
	if err != nil {
		return nil, err
	}

	aead, err := getAEAD(defaultNumThreads, password, salt)
	if err != nil {
		return nil, err
	}

	// Select a random nonce, and leave capacity for the ciphertext.
	nonce := make([]byte, aead.NonceSize(), aead.NonceSize()+len(mnemonicBytes)+aead.Overhead())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}

	// Encrypt the message and append the ciphertext to the nonce.
	cipher := aead.Seal(nonce, nonce, []byte(mnemonicBytes), nil)

	return &EncryptedMnemonic{
		cipher: cipher,
		salt:   salt,
	}, nil
}
