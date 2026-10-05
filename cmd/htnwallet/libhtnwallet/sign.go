package libhtnwallet

import (
	"bytes"

	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/libhtnwallet/bip32"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/libhtnwallet/serialization"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/txscript"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/utxo"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
	"github.com/HoosatNetwork/HTND/v2/util"
	"github.com/pkg/errors"
)

func rawTxInSignature(extendedKey *bip32.ExtendedKey, tx *externalapi.DomainTransaction, idx int, hashType consensushashing.SigHashType,
	sighashReusedValues *consensushashing.SighashReusedValues, ecdsa bool,
) ([]byte, error) {
	privateKey := extendedKey.PrivateKey()
	if ecdsa {
		return txscript.RawTxInSignatureECDSA(tx, idx, hashType, privateKey, sighashReusedValues)
	}

	schnorrKeyPair, err := privateKey.ToSchnorr()
	if err != nil {
		return nil, err
	}

	return txscript.RawTxInSignature(tx, idx, hashType, schnorrKeyPair, sighashReusedValues)
}

// Sign signs the transaction with the given private keys
func Sign(params *dagconfig.Params, mnemonics []string, serializedPSTx []byte, ecdsa bool) ([]byte, error) {
	return SignWithImportedKeys(params, mnemonics, nil, serializedPSTx, ecdsa)
}

// SignWithImportedKeys signs the transaction with the given mnemonics, as Sign does, and with the keys of
// an imported wallet. importedKeys maps each imported key's extended public key
// (ImportedKeyExtendedPublicKey) to the imported key. A wallet has one or the other, so in practice only
// one of the two is given.
//
// Signers that match none of the transaction's keys are an error: each mnemonic on its own, as it always
// was for Sign, and the imported keys together. Imported keys are Schnorr keys and always sign with
// Schnorr.
func SignWithImportedKeys(params *dagconfig.Params, mnemonics []string, importedKeys map[string]*bip32.ExtendedKey,
	serializedPSTx []byte, ecdsa bool,
) ([]byte, error) {
	partiallySignedTransaction, err := serialization.DeserializePartiallySignedTransaction(serializedPSTx)
	if err != nil {
		return nil, err
	}
	prepareInputsForSigning(partiallySignedTransaction)

	for _, mnemonic := range mnemonics {
		signed, err := sign(params, mnemonic, partiallySignedTransaction, ecdsa)
		if err != nil {
			return nil, err
		}
		if !signed {
			return nil, errors.Errorf("Public key doesn't match any of the transaction public keys")
		}
	}

	if len(importedKeys) > 0 && !isTransactionFullySigned(partiallySignedTransaction) {
		signed, err := signWithImportedKeys(partiallySignedTransaction, importedKeys)
		if err != nil {
			return nil, err
		}
		if !signed {
			return nil, errors.Errorf("None of the imported keys matches the transaction public keys")
		}
	}
	return serialization.SerializePartiallySignedTransaction(partiallySignedTransaction)
}

// prepareInputsForSigning gives every input the UTXO entry and sig op count the signature hash covers.
func prepareInputsForSigning(partiallySignedTransaction *serialization.PartiallySignedTransaction) {
	for i, partiallySignedInput := range partiallySignedTransaction.PartiallySignedInputs {
		prevOut := partiallySignedInput.PrevOutput
		partiallySignedTransaction.Tx.Inputs[i].UTXOEntry = utxo.NewUTXOEntry(
			prevOut.Value,
			prevOut.ScriptPublicKey,
			false, // This is a fake value, because it's irrelevant for the signature
			0,     // This is a fake value, because it's irrelevant for the signature
		)
		partiallySignedTransaction.Tx.Inputs[i].SigOpCount = byte(len(partiallySignedInput.PubKeySignaturePairs))
	}
}

// signWithImportedKeys signs every input key that is one of the imported keys, and reports whether it
// signed any.
func signWithImportedKeys(partiallySignedTransaction *serialization.PartiallySignedTransaction,
	importedKeys map[string]*bip32.ExtendedKey,
) (bool, error) {
	sighashReusedValues := &consensushashing.SighashReusedValues{}
	signed := false
	for i, partiallySignedInput := range partiallySignedTransaction.PartiallySignedInputs {
		for _, pair := range partiallySignedInput.PubKeySignaturePairs {
			importedKey, ok := importedKeys[pair.ExtendedPublicKey]
			if !ok {
				continue
			}
			var err error
			pair.Signature, err = rawTxInSignature(importedKey, partiallySignedTransaction.Tx, i,
				consensushashing.SigHashAll, sighashReusedValues, false)
			if err != nil {
				return false, err
			}
			signed = true
		}
	}
	return signed, nil
}

// sign signs every input key the mnemonic derives, and reports whether it signed any. A transaction that
// is already fully signed needs nothing more, and counts as signed.
func sign(params *dagconfig.Params, mnemonic string, partiallySignedTransaction *serialization.PartiallySignedTransaction,
	ecdsa bool,
) (bool, error) {
	if isTransactionFullySigned(partiallySignedTransaction) {
		return true, nil
	}

	sighashReusedValues := &consensushashing.SighashReusedValues{}
	signed := false
	var mldsa44BIP39SeedCache []byte
	for i, partiallySignedInput := range partiallySignedTransaction.PartiallySignedInputs {
		if IsMLDSA44Input(partiallySignedInput) {
			if mldsa44BIP39SeedCache == nil {
				mldsa44BIP39SeedCache = mldsa44BIP39Seed(mnemonic)
			}
			signMLDSA44 := signMLDSA44Input
			if partiallySignedInput.RedeemScript != nil && txscript.IsMultiSigMLDSA44RedeemScript(partiallySignedInput.RedeemScript) {
				signMLDSA44 = signMLDSA44MultiSigInput
			}
			inputSigned, err := signMLDSA44(params, mldsa44BIP39SeedCache, partiallySignedTransaction, i, sighashReusedValues)
			if err != nil {
				return false, err
			}
			signed = signed || inputSigned
			continue
		}

		isMultisig := len(partiallySignedInput.PubKeySignaturePairs) > 1
		path := defaultPath(isMultisig)
		extendedKey, err := extendedKeyFromMnemonicAndPath(mnemonic, path, params)
		if err != nil {
			return false, err
		}

		derivedKey, err := extendedKey.DeriveFromPath(partiallySignedInput.DerivationPath)
		if err != nil {
			return false, err
		}

		derivedPublicKey, err := derivedKey.Public()
		if err != nil {
			return false, err
		}

		for _, pair := range partiallySignedInput.PubKeySignaturePairs {
			if pair.ExtendedPublicKey == derivedPublicKey.String() {
				pair.Signature, err = rawTxInSignature(derivedKey, partiallySignedTransaction.Tx, i, consensushashing.SigHashAll, sighashReusedValues, ecdsa)
				if err != nil {
					return false, err
				}

				signed = true
			}
		}
	}

	return signed, nil
}

// signMLDSA44Input signs input idx, which spends a single-sig ML-DSA-44 output - P2PKH, or P2SH of the
// P2PKH script - if this mnemonic owns it. It reports false, without error, when the key at the
// input's derivation path is not the one the output is locked to - the input belongs to another
// mnemonic.
func signMLDSA44Input(params *dagconfig.Params, bip39Seed []byte, partiallySignedTransaction *serialization.PartiallySignedTransaction, idx int,
	sighashReusedValues *consensushashing.SighashReusedValues,
) (bool, error) {
	partiallySignedInput := partiallySignedTransaction.PartiallySignedInputs[idx]
	if len(partiallySignedInput.PubKeySignaturePairs) != 1 {
		return false, errors.Errorf("ML-DSA-44 input %d has %d signers; only single-sig is supported",
			idx, len(partiallySignedInput.PubKeySignaturePairs))
	}

	publicKey, privateKey, err := mldsa44KeyFromBIP39Seed(bip39Seed, partiallySignedInput.DerivationPath, false)
	if err != nil {
		return false, err
	}
	publicKeyBytes := publicKey.Bytes()

	owned, err := mldsa44SingleSigInputIsLockedTo(params, partiallySignedInput, publicKeyBytes)
	if err != nil {
		return false, errors.Wrapf(err, "input %d", idx)
	}
	if !owned {
		return false, nil
	}

	signature, err := txscript.RawTxInSignatureMLDSA44(partiallySignedTransaction.Tx, idx, consensushashing.SigHashAll,
		privateKey, sighashReusedValues)
	if err != nil {
		return false, err
	}
	partiallySignedInput.PubKeySignaturePairs[0].Signature = append(signature, publicKeyBytes...)
	return true, nil
}

// mldsa44SingleSigInputIsLockedTo reports whether input's output is locked to publicKey, in whichever
// single-sig ML-DSA-44 form it takes.
func mldsa44SingleSigInputIsLockedTo(params *dagconfig.Params, input *serialization.PartiallySignedInput, publicKey []byte) (bool, error) {
	lockingScript := input.PrevOutput.ScriptPublicKey
	if input.RedeemScript != nil {
		// The redeem script travels with the unsigned transaction, so check it is the one the output
		// commits to before trusting the key hash in it.
		p2sh, err := txscript.PayToScriptHashScript(input.RedeemScript)
		if err != nil {
			return false, err
		}
		if !bytes.Equal(p2sh, lockingScript.Script) {
			return false, errors.New("the redeem script does not match the output it spends")
		}
		lockingScript = &externalapi.ScriptPublicKey{Script: input.RedeemScript, Version: lockingScript.Version}
	}

	_, address, err := txscript.ExtractScriptPubKeyAddress(lockingScript, params)
	if err != nil {
		return false, err
	}
	mldsa44Address, ok := address.(*util.AddressPublicKeyHashMLDSA44)
	if !ok {
		return false, errors.Errorf("expected an ML-DSA-44 address, got %T", address)
	}
	return bytes.Equal(mldsa44Address.ScriptAddress(), util.HashBlake2b(publicKey)), nil
}

// IsMLDSA44Input reports whether input spends an ML-DSA-44 output: a single-sig ML-DSA-44 P2PKH, or a
// P2SH - single-sig or multisig - which the input marks by carrying its redeem script.
func IsMLDSA44Input(input *serialization.PartiallySignedInput) bool {
	return IsMLDSA44Coin(input.PrevOutput.ScriptPublicKey.Script, input.RedeemScript)
}

// IsMLDSA44Coin reports whether a coin locked by scriptPublicKey, spent with redeemScript (nil unless
// it is ML-DSA-44 P2SH), is an ML-DSA-44 coin in any form.
func IsMLDSA44Coin(scriptPublicKey []byte, redeemScript []byte) bool {
	if redeemScript != nil {
		return txscript.IsMultiSigMLDSA44RedeemScript(redeemScript) || isMLDSA44SingleSigRedeemScript(redeemScript)
	}
	return txscript.GetScriptClass(scriptPublicKey) == txscript.PubKeyHashMLDSA44Ty
}

// signMLDSA44MultiSigInput signs input idx, which spends an ML-DSA-44 multisig P2SH output, in the slot
// of this mnemonic's key. It reports false, without error, when none of the redeem script's key
// hashes is this mnemonic's key at the input's derivation path.
func signMLDSA44MultiSigInput(_ *dagconfig.Params, bip39Seed []byte, partiallySignedTransaction *serialization.PartiallySignedTransaction,
	idx int, sighashReusedValues *consensushashing.SighashReusedValues,
) (bool, error) {
	partiallySignedInput := partiallySignedTransaction.PartiallySignedInputs[idx]

	// The redeem script travels with the unsigned transaction, so check it is the one the output
	// commits to before trusting its key hashes.
	p2sh, err := txscript.PayToScriptHashScript(partiallySignedInput.RedeemScript)
	if err != nil {
		return false, err
	}
	if !bytes.Equal(p2sh, partiallySignedInput.PrevOutput.ScriptPublicKey.Script) {
		return false, errors.Errorf("input %d: the redeem script does not match the output it spends", idx)
	}
	_, publicKeyHashes, err := txscript.ExtractMultiSigMLDSA44RedeemScript(partiallySignedInput.RedeemScript)
	if err != nil {
		return false, err
	}
	if len(publicKeyHashes) != len(partiallySignedInput.PubKeySignaturePairs) {
		return false, errors.Errorf("input %d: the redeem script has %d keys but the input has %d signers",
			idx, len(publicKeyHashes), len(partiallySignedInput.PubKeySignaturePairs))
	}

	publicKey, privateKey, err := mldsa44KeyFromBIP39Seed(bip39Seed, partiallySignedInput.DerivationPath, true)
	if err != nil {
		return false, err
	}
	publicKeyBytes := publicKey.Bytes()
	publicKeyHash := util.HashBlake2b(publicKeyBytes)

	for slot, slotPublicKeyHash := range publicKeyHashes {
		if !bytes.Equal(slotPublicKeyHash, publicKeyHash) {
			continue
		}
		signature, err := txscript.RawTxInSignatureMLDSA44(partiallySignedTransaction.Tx, idx, consensushashing.SigHashAll,
			privateKey, sighashReusedValues)
		if err != nil {
			return false, err
		}
		partiallySignedInput.PubKeySignaturePairs[slot].Signature = append(signature, publicKeyBytes...)
		return true, nil
	}
	return false, nil
}
