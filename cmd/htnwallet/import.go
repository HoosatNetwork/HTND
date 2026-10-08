package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/keys"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/libhtnwallet"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/utils"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
	"github.com/pkg/errors"
	"github.com/tyler-smith/go-bip39"
)

// maxImportedWebWalletAddresses bounds --num-addresses. Every imported key is stored in the keys file and
// scanned by the daemon on every sync, so the range is kept to what a web wallet plausibly used.
const maxImportedWebWalletAddresses = 10_000

func importPrivateKey(conf *importPrivateKeyConfig) error {
	privateKey := conf.PrivateKey
	if privateKey == "" {
		privateKey = keys.GetPassword("Enter the private key (hex): ")
	}
	privateKey = strings.ToLower(strings.TrimSpace(privateKey))

	keysFile, err := keys.NewImportedFile(conf.NetParams(), libhtnwallet.ImportedKeyTypePrivateKey, privateKey, nil,
		conf.Password)
	if err != nil {
		return err
	}
	err = saveNewKeysFile(conf.NetParams(), keysFile, conf.KeysFile, conf.Yes)
	if err != nil {
		return err
	}

	address, err := libhtnwallet.ImportedKeyAddress(conf.NetParams(), keysFile.ImportedKeys()[0].ExtendedPublicKey,
		libhtnwallet.SingleSigAddressTypeP2PK)
	if err != nil {
		return err
	}
	fmt.Printf("Wrote a wallet of the private key of %s into %s\n", address, keysFile.Path())
	return nil
}

func importWebWallet(conf *importWebWalletConfig) error {
	secret, err := webWalletSecret(conf)
	if err != nil {
		return err
	}

	paths := make([]string, 0, 2*conf.NumAddresses)
	for _, keyChain := range []uint8{libhtnwallet.ExternalKeychain, libhtnwallet.InternalKeychain} {
		for index := range conf.NumAddresses {
			paths = append(paths, libhtnwallet.WebWalletPath(keyChain, index))
		}
	}

	keysFile, err := keys.NewImportedFile(conf.NetParams(), libhtnwallet.ImportedKeyTypeHTNWebWallet, secret, paths,
		conf.Password)
	if err != nil {
		return err
	}
	err = saveNewKeysFile(conf.NetParams(), keysFile, conf.KeysFile, conf.Yes)
	if err != nil {
		return err
	}

	firstAddress, err := libhtnwallet.ImportedKeyAddress(conf.NetParams(), keysFile.ImportedKeys()[0].ExtendedPublicKey,
		libhtnwallet.SingleSigAddressTypeP2PK)
	if err != nil {
		return err
	}
	fmt.Printf("Wrote a wallet of %d receive and %d change addresses of the web wallet whose first address is\n%s\n"+
		"into %s\n", conf.NumAddresses, conf.NumAddresses, firstAddress, keysFile.Path())
	fmt.Printf("If the web wallet used more than %d addresses of either kind, import it again with a larger "+
		"--num-addresses.\n", conf.NumAddresses)
	return nil
}

// saveNewKeysFile writes a newly created keys file, as create does: asking before overwriting an existing
// file unless yes is set, and refusing while a daemon or another command holds the file.
func saveNewKeysFile(params *dagconfig.Params, keysFile *keys.File, path string, yes bool) error {
	err := keysFile.SetPath(params, path, yes)
	if err != nil {
		return err
	}
	err = keysFile.TryLock()
	if err != nil {
		return err
	}
	return keysFile.Save()
}

// webWalletSecret returns the secret to import a web wallet by: its mnemonic, or - when only an export
// whose mnemonic does not match its master key is at hand - its master extended private key.
func webWalletSecret(conf *importWebWalletConfig) (string, error) {
	export := conf.Export
	if conf.ExportFile != "" {
		exportBytes, err := os.ReadFile(conf.ExportFile)
		if err != nil {
			return "", err
		}
		export = string(exportBytes)
	}

	if export == "" {
		fmt.Println("Enter the web wallet's mnemonic here:")
		mnemonic, err := utils.ReadLine(bufio.NewReader(os.Stdin))
		if err != nil {
			return "", err
		}
		mnemonic = libhtnwallet.NormalizeMnemonic(mnemonic)
		if !bip39.IsMnemonicValid(mnemonic) {
			return "", errors.New("mnemonic is invalid")
		}
		return mnemonic, nil
	}

	exportPassword := conf.ExportPassword
	if exportPassword == "" {
		exportPassword = keys.GetPassword("Enter the web wallet export's password: ")
	}
	decrypted, err := libhtnwallet.DecryptWebWalletExport(export, exportPassword)
	if err != nil {
		return "", err
	}
	return webWalletSecretFromExport(conf.NetParams(), decrypted)
}

// webWalletSecretFromExport picks the secret to store from a decrypted export. The web wallet derives its
// addresses from the master key ("privKey"), and the mnemonic is only how that key was made. They agree
// in every export the web wallet writes, and then the mnemonic is kept, since it is what a user can write
// down and restore from. If they ever disagree, the master key is what the addresses came from.
func webWalletSecretFromExport(params *dagconfig.Params, export *libhtnwallet.WebWalletExport) (string, error) {
	mnemonic := libhtnwallet.NormalizeMnemonic(export.SeedPhrase)
	mnemonicIsValid := bip39.IsMnemonicValid(mnemonic)

	if export.PrivateKey == "" {
		if !mnemonicIsValid {
			return "", errors.New("the web wallet export holds neither a valid mnemonic nor a master key")
		}
		return mnemonic, nil
	}

	masterKey, err := libhtnwallet.WebWalletMasterKey(params, export.PrivateKey)
	if err != nil {
		return "", errors.Wrap(err, "invalid master key in the web wallet export")
	}
	if mnemonicIsValid {
		mnemonicMasterKey, err := libhtnwallet.WebWalletMasterKey(params, mnemonic)
		if err != nil {
			return "", err
		}
		if mnemonicMasterKey.String() == masterKey.String() {
			return mnemonic, nil
		}
		fmt.Println("Warning: the export's mnemonic does not produce its master key; importing the master key, " +
			"which the web wallet's addresses are derived from.")
	}
	return strings.TrimSpace(export.PrivateKey), nil
}

func validateImportWebWalletConfig(conf *importWebWalletConfig) error {
	if conf.Export != "" && conf.ExportFile != "" {
		return errors.New("only one of --export and --export-file can be given")
	}
	if conf.NumAddresses == 0 {
		return errors.New("--num-addresses must be at least 1")
	}
	if conf.NumAddresses > maxImportedWebWalletAddresses {
		return errors.Errorf("--num-addresses can be at most %d", maxImportedWebWalletAddresses)
	}
	return nil
}
