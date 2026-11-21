package main

import (
	"fmt"

	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/keys"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/libhtnwallet"
	"github.com/pkg/errors"
)

// generateMLDSA44Keys extends the wallet's single pool or the pools of its local cosigners.
// The keys file stays locked from the read through the save so the indexes and pools
// come from the same snapshot.
func generateMLDSA44Keys(conf *generateMLDSA44KeysConfig) error {
	count := conf.Count
	implicit := !conf.countSet && count == 0
	if !implicit && count == 0 {
		return errors.New("--count must be positive")
	}
	if count > keys.MLDSA44AddressIndexLimit {
		return errors.Errorf("--count must be at most %d, the end of the addressable range", keys.MLDSA44AddressIndexLimit)
	}

	keysFile, unlock, err := keys.ReadLockedKeysFile(conf.NetParams(), conf.KeysFile)
	if err != nil {
		return err
	}
	defer unlock()
	if len(keysFile.EncryptedMnemonics) == 0 {
		return errors.New("this wallet holds no mnemonic to derive ML-DSA-44 keys from")
	}
	if implicit {
		var limited bool
		count, limited = keysFile.DefaultMLDSA44KeyCount()
		if limited {
			fmt.Printf("The address index limit leaves fewer than %d future indexes per key chain.\n",
				libhtnwallet.DefaultMLDSA44KeyPoolSize)
		}
	}

	isMultisig := len(keysFile.ExtendedPublicKeys) > 1
	if !isMultisig {
		if count <= keysFile.MLDSA44.Size() {
			fmt.Printf("The wallet already has ML-DSA-44 keys for at least %d indexes per key chain\n", keysFile.MLDSA44.Size())
			return nil
		}
		if implicit {
			existing, missing := mldsa44PoolGrowth(keysFile.MLDSA44, count)
			if err := checkImplicitMLDSA44Growth(count, existing+missing, missing); err != nil {
				return err
			}
		}
	}

	password := conf.Password
	if password == "" {
		password = keys.GetPassword("Password:")
	}
	mnemonics, err := keysFile.DecryptMnemonics(password)
	if err != nil {
		return err
	}

	correctedLocalPool := false
	if !isMultisig {
		keysFile.MLDSA44, err = keysFile.MLDSA44.Extend(mnemonics[0], count, false)
		if err != nil {
			return err
		}
	} else {
		needed := make(map[string]string, len(mnemonics))
		for _, mnemonic := range mnemonics {
			publicKey, err := libhtnwallet.MasterPublicKeyFromMnemonic(conf.NetParams(), mnemonic, true)
			if err != nil {
				return err
			}
			if keysFile.MLDSA44Cosigners[publicKey].Size() < count {
				needed[publicKey] = mnemonic
			}
		}
		if len(needed) == 0 {
			fmt.Printf("The local cosigners already have ML-DSA-44 keys for at least %d indexes per key chain\n", count)
			return nil
		}
		if implicit {
			var stored, work uint64
			for _, pool := range keysFile.MLDSA44Cosigners {
				existing, _ := mldsa44PoolGrowth(pool, 0)
				stored += existing
			}
			for publicKey := range needed {
				existing, missing := mldsa44PoolGrowth(keysFile.MLDSA44Cosigners[publicKey], count)
				stored += missing
				work += existing + missing
			}
			if err := checkImplicitMLDSA44Growth(count, stored, work); err != nil {
				return err
			}
		}

		if keysFile.MLDSA44Cosigners == nil {
			keysFile.MLDSA44Cosigners = make(map[string]*keys.MLDSA44KeyPool, len(needed))
		}
		for publicKey, mnemonic := range needed {
			pool, matches, err := keysFile.MLDSA44Cosigners[publicKey].RebuildCosigner(mnemonic, count)
			if err != nil {
				return err
			}
			if !matches {
				if implicit {
					return errors.Errorf("the stored ML-DSA-44 pool for this account differs from the keys derived "+
						"from the mnemonic; run generate-mldsa44-keys --count %d to rebuild it", count)
				}
				correctedLocalPool = true
			}
			keysFile.MLDSA44Cosigners[publicKey] = pool
		}
	}

	if err := keysFile.Save(); err != nil {
		return err
	}
	fmt.Printf("Wrote ML-DSA-44 keys for at least %d indexes per key chain into %s\n", count, keysFile.Path())
	if correctedLocalPool {
		fmt.Println("Rebuilt the stored ML-DSA-44 pool for this account from the mnemonic.")
	}
	if isMultisig {
		fmt.Printf("Share them with the other cosigners with \"htnwallet %s\"\n", exportMLDSA44KeysSubCmd)
	}
	return nil
}

func mldsa44PoolGrowth(pool *keys.MLDSA44KeyPool, count uint32) (existing, missing uint64) {
	var receive, change uint64
	if pool != nil {
		receive = uint64(len(pool.ExternalPublicKeyHashes))
		change = uint64(len(pool.InternalPublicKeyHashes))
	}
	target := uint64(count)
	return receive + change, max(receive, target) - receive + max(change, target) - change
}

func checkImplicitMLDSA44Growth(count uint32, stored, work uint64) error {
	const (
		maxDerivations = 20_000
		maxHashBytes   = 2 << 20
		// Each 32-byte hash is written as 64 hex digits with quotes and a separator.
		bytesPerHash = 67
	)
	estimatedBytes := stored * bytesPerHash
	if work > maxDerivations || estimatedBytes > maxHashBytes {
		return errors.Errorf("default generation would derive %d hashes and store about %d bytes of hashes; "+
			"pass --count %d to request this explicitly or choose a smaller --count", work, estimatedBytes, count)
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
