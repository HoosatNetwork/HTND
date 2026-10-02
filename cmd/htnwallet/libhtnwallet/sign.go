package libhtnwallet

import (
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/libhtnwallet/bip32"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/libhtnwallet/serialization"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/txscript"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/utxo"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
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
	for i, partiallySignedInput := range partiallySignedTransaction.PartiallySignedInputs {
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
