package main

import (
	"fmt"

	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/keys"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/libhtnwallet"
	"github.com/pkg/errors"
)

// generateMLDSA44Keys fills the keys file's ML-DSA-44 key pools up to conf.Count indexes per key chain:
// the wallet's single pool for a single-sig wallet, and the pool of each mnemonic it holds for a
// multisig wallet. Wallets created before ML-DSA-44 support have none, and a pool runs out once
// addresses are used past its size; both need the mnemonic, which only this command (not the daemon)
// can decrypt.
func generateMLDSA44Keys(conf *generateMLDSA44KeysConfig) error {
	keysFile, err := keys.ReadKeysFile(conf.NetParams(), conf.KeysFile)
	if err != nil {
		return err
	}
	if len(keysFile.EncryptedMnemonics) == 0 {
		return errors.New("this wallet holds no mnemonic to derive ML-DSA-44 keys from")
	}
	if conf.Count == 0 {
		return errors.New("--count must be positive")
	}
	isMultisig := len(keysFile.ExtendedPublicKeys) > 1
	if !isMultisig && conf.Count <= keysFile.MLDSA44.Size() {
		fmt.Printf("The wallet already has ML-DSA-44 keys for %d indexes per key chain\n", keysFile.MLDSA44.Size())
		return nil
	}

	// The daemon holds this lock while it runs, and it only reads the key pools at startup.
	err = keysFile.TryLock()
	if err != nil {
		return errors.Wrap(err, "stop the wallet daemon before generating ML-DSA-44 keys")
	}

	if len(conf.Password) == 0 {
		conf.Password = keys.GetPassword("Password:")
	}
	mnemonics, err := keysFile.DecryptMnemonics(conf.Password)
	if err != nil {
		return err
	}

	if !isMultisig {
		keyPool, err := keys.NewMLDSA44KeyPool(mnemonics[0], conf.Count, false)
		if err != nil {
			return err
		}
		keysFile.MLDSA44 = keyPool
	} else {
		if keysFile.MLDSA44Cosigners == nil {
			keysFile.MLDSA44Cosigners = make(map[string]*keys.MLDSA44KeyPool, len(mnemonics))
		}
		for _, mnemonic := range mnemonics {
			extendedPublicKey, err := libhtnwallet.MasterPublicKeyFromMnemonic(conf.NetParams(), mnemonic, true)
			if err != nil {
				return err
			}
			if keysFile.MLDSA44Cosigners[extendedPublicKey].Size() >= conf.Count {
				continue
			}
			keyPool, err := keys.NewMLDSA44KeyPool(mnemonic, conf.Count, true)
			if err != nil {
				return err
			}
			keysFile.MLDSA44Cosigners[extendedPublicKey] = keyPool
		}
	}

	err = keysFile.Save()
	if err != nil {
		return err
	}

	fmt.Printf("Wrote ML-DSA-44 keys for %d indexes per key chain into %s\n", conf.Count, keysFile.Path())
	if isMultisig {
		fmt.Printf("Share them with the other cosigners with \"htnwallet %s\"\n", exportMLDSA44KeysSubCmd)
	}
	return nil
}

// exportMLDSA44Keys writes the multisig wallet's ML-DSA-44 cosigner keys for the other cosigners.
// It needs no password: the pools hold public key hashes only.
func exportMLDSA44Keys(conf *exportMLDSA44KeysConfig) error {
	keysFile, err := keys.ReadKeysFile(conf.NetParams(), conf.KeysFile)
	if err != nil {
		return err
	}
	count, err := keysFile.ExportMLDSA44Cosigners(conf.NetParams(), conf.OutputFile)
	if err != nil {
		return err
	}
	fmt.Printf("Wrote the ML-DSA-44 keys of %d cosigner(s) into %s\n", count, conf.OutputFile)
	return nil
}

// importMLDSA44Keys adds another cosigner's exported ML-DSA-44 keys to the multisig wallet.
func importMLDSA44Keys(conf *importMLDSA44KeysConfig) error {
	keysFile, err := keys.ReadKeysFile(conf.NetParams(), conf.KeysFile)
	if err != nil {
		return err
	}
	err = keysFile.TryLock()
	if err != nil {
		return errors.Wrap(err, "stop the wallet daemon before importing ML-DSA-44 keys")
	}
	count, err := keysFile.ImportMLDSA44Cosigners(conf.NetParams(), conf.InputFile)
	if err != nil {
		return err
	}
	err = keysFile.Save()
	if err != nil {
		return err
	}

	missing := 0
	for _, extendedPublicKey := range keysFile.ExtendedPublicKeys {
		if _, ok := keysFile.MLDSA44Cosigners[extendedPublicKey]; !ok {
			missing++
		}
	}
	fmt.Printf("Imported the ML-DSA-44 keys of %d cosigner(s) into %s\n", count, keysFile.Path())
	if missing > 0 {
		fmt.Printf("ML-DSA-44 keys of %d cosigner(s) are still missing; ML-DSA-44 addresses need all of them\n", missing)
	}
	return nil
}
