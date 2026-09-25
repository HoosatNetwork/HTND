package serialization

import (
	"testing"

	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/libhtnwallet/serialization/protoserialization"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/subnetworks"
)

func validPartiallySignedTransactionBytes(t *testing.T) []byte {
	scriptPublicKey := &externalapi.ScriptPublicKey{Script: []byte{0x51}, Version: 0}
	partiallySignedTransaction := &PartiallySignedTransaction{
		Tx: &externalapi.DomainTransaction{
			Inputs: []*externalapi.DomainTransactionInput{{
				PreviousOutpoint: externalapi.DomainOutpoint{
					TransactionID: *externalapi.NewDomainTransactionIDFromByteArray(&[externalapi.DomainHashSize]byte{1}),
				},
			}},
			Outputs:      []*externalapi.DomainTransactionOutput{{Value: 1, ScriptPublicKey: scriptPublicKey}},
			SubnetworkID: subnetworks.SubnetworkIDNative,
		},
		PartiallySignedInputs: []*PartiallySignedInput{{
			PrevOutput:           &externalapi.DomainTransactionOutput{Value: 2, ScriptPublicKey: scriptPublicKey},
			MinimumSignatures:    1,
			PubKeySignaturePairs: []*PubKeySignaturePair{{ExtendedPublicKey: "xpub"}},
			DerivationPath:       "m/0/0",
		}},
	}
	serialized, err := SerializePartiallySignedTransaction(partiallySignedTransaction)
	if err != nil {
		t.Fatalf("SerializePartiallySignedTransaction: %+v", err)
	}
	return serialized
}

// TestDeserializeMalformedPartiallySignedTransaction pins that partially signed transaction bytes missing a
// nested message, or with a different number of partially signed inputs than transaction inputs, are
// rejected. They used to be dereferenced or indexed unchecked, so the wallet daemon's Sign and Broadcast
// RPCs panicked on such bytes from a client or cosigner, and the daemon does not recover handler panics.
func TestDeserializeMalformedPartiallySignedTransaction(t *testing.T) {
	valid := validPartiallySignedTransactionBytes(t)
	if _, err := DeserializePartiallySignedTransaction(valid); err != nil {
		t.Fatalf("a well-formed partially signed transaction must deserialize: %+v", err)
	}

	tests := map[string]func(message *protoserialization.PartiallySignedTransaction){
		"no transaction":            func(m *protoserialization.PartiallySignedTransaction) { m.Tx = nil },
		"no subnetwork id":          func(m *protoserialization.PartiallySignedTransaction) { m.Tx.SubnetworkId = nil },
		"input without outpoint":    func(m *protoserialization.PartiallySignedTransaction) { m.Tx.Inputs[0].PreviousOutpoint = nil },
		"input without prev output": func(m *protoserialization.PartiallySignedTransaction) { m.PartiallySignedInputs[0].PrevOutput = nil },
		"output without script": func(m *protoserialization.PartiallySignedTransaction) {
			m.PartiallySignedInputs[0].PrevOutput.ScriptPublicKey = nil
		},
		"more signed inputs than tx": func(m *protoserialization.PartiallySignedTransaction) { m.Tx.Inputs = nil },
	}
	for name, corrupt := range tests {
		t.Run(name, func(t *testing.T) {
			message := &protoserialization.PartiallySignedTransaction{}
			if err := message.UnmarshalVT(valid); err != nil {
				t.Fatalf("UnmarshalVT: %+v", err)
			}
			corrupt(message)
			malformed, err := message.MarshalVT()
			if err != nil {
				t.Fatalf("MarshalVT: %+v", err)
			}
			if _, err := DeserializePartiallySignedTransaction(malformed); err == nil {
				t.Fatalf("expected an error for a partially signed transaction with %s", name)
			}
		})
	}
}
