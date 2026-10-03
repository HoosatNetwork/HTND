package transactionvalidator

import (
	"testing"

	"github.com/kaspanet/go-secp256k1"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/txscript"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/utxo"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
	"github.com/HoosatNetwork/HTND/v2/util"
)

// signedSchnorrSpend returns a one-input transaction spending a P2PK output, signed by a fresh key,
// together with what the signature cache would hold for it.
func signedSchnorrSpend(t *testing.T) (*externalapi.DomainTransaction, secp256k1.Hash,
	*secp256k1.SchnorrSignature, *secp256k1.SchnorrPublicKey,
) {
	t.Helper()
	keyPair, err := secp256k1.GenerateSchnorrKeyPair()
	if err != nil {
		t.Fatalf("GenerateSchnorrKeyPair: %v", err)
	}
	publicKey, err := keyPair.SchnorrPublicKey()
	if err != nil {
		t.Fatalf("SchnorrPublicKey: %v", err)
	}
	serializedPublicKey, err := publicKey.Serialize()
	if err != nil {
		t.Fatalf("Serialize: %v", err)
	}
	address, err := util.NewAddressPublicKey(serializedPublicKey[:], util.Bech32PrefixHoosatTest)
	if err != nil {
		t.Fatalf("NewAddressPublicKey: %v", err)
	}
	scriptPublicKey, err := txscript.PayToAddrScript(address)
	if err != nil {
		t.Fatalf("PayToAddrScript: %v", err)
	}
	transaction := &externalapi.DomainTransaction{
		Inputs: []*externalapi.DomainTransactionInput{{
			UTXOEntry: utxo.NewUTXOEntry(1000, scriptPublicKey, false, 1),
		}},
		Outputs: []*externalapi.DomainTransactionOutput{{Value: 900, ScriptPublicKey: scriptPublicKey}},
	}

	signature, err := txscript.RawTxInSignature(transaction, 0, consensushashing.SigHashAll, keyPair,
		&consensushashing.SighashReusedValues{})
	if err != nil {
		t.Fatalf("RawTxInSignature: %v", err)
	}
	transaction.Inputs[0].SignatureScript, err = txscript.NewScriptBuilder().AddData(signature).Script()
	if err != nil {
		t.Fatalf("build signature script: %v", err)
	}

	sigHash, err := consensushashing.CalculateSignatureHashSchnorr(transaction, 0, consensushashing.SigHashAll,
		&consensushashing.SighashReusedValues{})
	if err != nil {
		t.Fatalf("CalculateSignatureHashSchnorr: %v", err)
	}
	parsedSignature, err := secp256k1.DeserializeSchnorrSignatureFromSlice(signature[:len(signature)-1])
	if err != nil {
		t.Fatalf("DeserializeSchnorrSignatureFromSlice: %v", err)
	}
	return transaction, secp256k1.Hash(*sigHash.ByteArray()), parsedSignature, publicKey
}

// TestPrewarmScriptCachesRecordsOnlyValidSignatures pins that prewarming leaves exactly what a
// sequential validation would have left in the signature cache: the valid signature, and nothing
// for a transaction whose signature no longer matches it.
func TestPrewarmScriptCachesRecordsOnlyValidSignatures(t *testing.T) {
	validator := New(0, false, 0, 0, 0, nil, nil, nil, nil, nil, &dagconfig.TestnetParams).(*transactionValidator)

	valid, validHash, validSignature, validPublicKey := signedSchnorrSpend(t)
	invalid, _, invalidSignature, invalidPublicKey := signedSchnorrSpend(t)
	invalid.Outputs[0].Value-- // the signature no longer covers the transaction
	invalidHash, err := consensushashing.CalculateSignatureHashSchnorr(invalid, 0, consensushashing.SigHashAll,
		&consensushashing.SighashReusedValues{})
	if err != nil {
		t.Fatalf("CalculateSignatureHashSchnorr: %v", err)
	}

	validator.PrewarmScriptCaches([]*externalapi.DomainTransaction{valid, invalid}, 1000)

	if !validator.sigCache.Exists(validHash, validSignature, validPublicKey) {
		t.Fatalf("the valid signature was not recorded in the signature cache")
	}
	if validator.sigCache.Exists(secp256k1.Hash(*invalidHash.ByteArray()), invalidSignature, invalidPublicKey) {
		t.Fatalf("a signature that does not verify was recorded in the signature cache")
	}
}

// TestPrewarmScriptCachesRespectsTheInputBudget pins that one call verifies at most
// prewarmInputBudget inputs, so a huge merge set cannot evict its own early results.
func TestPrewarmScriptCachesRespectsTheInputBudget(t *testing.T) {
	validator := New(0, false, 0, 0, 0, nil, nil, nil, nil, nil, &dagconfig.TestnetParams).(*transactionValidator)

	first, firstHash, firstSignature, firstPublicKey := signedSchnorrSpend(t)
	beyond, beyondHash, beyondSignature, beyondPublicKey := signedSchnorrSpend(t)
	filler := &externalapi.DomainTransaction{Inputs: make([]*externalapi.DomainTransactionInput, prewarmInputBudget)}
	for i := range filler.Inputs {
		filler.Inputs[i] = &externalapi.DomainTransactionInput{}
	}

	validator.PrewarmScriptCaches([]*externalapi.DomainTransaction{first, filler, beyond}, 1000)

	if !validator.sigCache.Exists(firstHash, firstSignature, firstPublicKey) {
		t.Fatalf("the transaction within the budget was not prewarmed")
	}
	if validator.sigCache.Exists(beyondHash, beyondSignature, beyondPublicKey) {
		t.Fatalf("a transaction past the input budget was prewarmed")
	}
}
