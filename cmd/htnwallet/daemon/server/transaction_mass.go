package server

import (
	"strconv"

	"github.com/kaspanet/go-secp256k1"
	"github.com/pkg/errors"

	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/libhtnwallet"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/libhtnwallet/serialization"
	"github.com/HoosatNetwork/HTND/v2/domain/miningmanager/mempool"
)

func checkedIntFromUint64(value uint64) (int, error) {
	parsedValue, err := strconv.ParseInt(strconv.FormatUint(value, 10), 10, 64)
	if err != nil {
		return 0, err
	}
	return int(parsedValue), nil
}

func checkedUint32FromInt(value int) (uint32, error) {
	parsedValue, err := strconv.ParseUint(strconv.Itoa(value), 10, 32)
	if err != nil {
		return 0, err
	}
	return uint32(parsedValue), nil
}

// requireStandardMass returns the transaction as the single transaction to sign and broadcast, or an
// error if, once signed, it would exceed MaximumStandardTransactionMass.
//
// The wallet used to split such a transaction into several that each moved part of the inputs to the
// change address, plus a merge transaction spending their outputs to pay the destination. The merge
// transaction spends outputs of transactions that are still unconfirmed, and nodes no longer accept or
// relay a transaction with an input younger than their minimum input age (mempool.Config.
// InputMinAgeDAAScore, 1000 DAA score units by default), let alone an unconfirmed one: the splits
// would go out and the payment would not. A send that needs more inputs than fit in one standard
// transaction is refused instead, before anything is broadcast.
func (s *server) requireStandardMass(transactionBytes []byte) ([][]byte, error) {
	transaction, err := serialization.DeserializePartiallySignedTransaction(transactionBytes)
	if err != nil {
		return nil, err
	}
	mass, err := s.estimateMassAfterSignatures(transaction)
	if err != nil {
		return nil, err
	}
	if mass > mempool.MaximumStandardTransactionMass {
		return nil, errors.Errorf("the transaction spends %d inputs and would have a mass of %d, over the "+
			"standard limit of %d. It cannot be split into chained transactions, because nodes do not "+
			"accept inputs younger than %d DAA score units. Consolidate the wallet's coins first "+
			"(for example with auto-compound), wait for the consolidated coins to reach that age, and "+
			"send again, or send a smaller amount",
			len(transaction.Tx.Inputs), mass, mempool.MaximumStandardTransactionMass, inputMinAgeDAAScore)
	}
	return [][]byte{transactionBytes}, nil
}

func (s *server) estimateMassAfterSignatures(transaction *serialization.PartiallySignedTransaction) (uint64, error) {
	transaction = transaction.Clone()
	var signatureSize uint64
	if s.keysFile.ECDSA {
		signatureSize = secp256k1.SerializedECDSASignatureSize
	} else {
		signatureSize = secp256k1.SerializedSchnorrSignatureSize
	}

	for i, input := range transaction.PartiallySignedInputs {
		if libhtnwallet.IsMLDSA44Input(input) {
			// The signer stores the signature and the public key together; see
			// libhtnwallet.MLDSA44SignatureWithPublicKeySize. A single-sig input has one pair; a
			// multisig one is extracted with its first MinimumSignatures signatures.
			for j := 0; j < len(input.PubKeySignaturePairs) && uint64(j) < uint64(max(input.MinimumSignatures, 1)); j++ {
				input.PubKeySignaturePairs[j].Signature = make([]byte, libhtnwallet.MLDSA44SignatureWithPublicKeySize)
			}
			transaction.Tx.Inputs[i].SigOpCount = byte(len(input.PubKeySignaturePairs))
			continue
		}
		for j, pubKeyPair := range input.PubKeySignaturePairs {
			index, err := checkedUint32FromInt(j)
			if err != nil {
				return 0, err
			}
			if index >= s.keysFile.MinimumSignatures {
				break
			}
			pubKeyPair.Signature = make([]byte, signatureSize+1) // +1 for SigHashType
		}
		transaction.Tx.Inputs[i].SigOpCount = byte(len(input.PubKeySignaturePairs))
	}

	transactionWithSignatures, err := libhtnwallet.ExtractTransactionDeserialized(transaction, s.keysFile.ECDSA)
	if err != nil {
		return 0, err
	}

	return s.txMassCalculator.CalculateTransactionMass(transactionWithSignatures), nil
}
