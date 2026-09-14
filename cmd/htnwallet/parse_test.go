package main

import (
	"testing"

	"github.com/HoosatNetwork/HTND/cmd/htnwallet/libhtnwallet/serialization"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/subnetworks"
	"github.com/HoosatNetwork/HTND/domain/dagconfig"
	"github.com/HoosatNetwork/HTND/infrastructure/config"
)

// TestParseNonStandardOutput pins that `htnwallet parse` prints a transaction whose output script has no
// address. ExtractScriptPubKeyAddress returns a nil address for such a script, and parse called
// EncodeAddress on it before checking, so the command panicked.
func TestParseNonStandardOutput(t *testing.T) {
	nonStandardScript := &externalapi.ScriptPublicKey{Script: []byte{0x00, 0x01, 0x02}, Version: 0}
	partiallySignedTransaction := &serialization.PartiallySignedTransaction{
		Tx: &externalapi.DomainTransaction{
			Inputs: []*externalapi.DomainTransactionInput{{
				PreviousOutpoint: externalapi.DomainOutpoint{
					TransactionID: *externalapi.NewDomainTransactionIDFromByteArray(&[externalapi.DomainHashSize]byte{1}),
				},
			}},
			Outputs:      []*externalapi.DomainTransactionOutput{{Value: 1, ScriptPublicKey: nonStandardScript}},
			SubnetworkID: subnetworks.SubnetworkIDNative,
		},
		PartiallySignedInputs: []*serialization.PartiallySignedInput{{
			PrevOutput:           &externalapi.DomainTransactionOutput{Value: 2, ScriptPublicKey: nonStandardScript},
			MinimumSignatures:    1,
			PubKeySignaturePairs: []*serialization.PubKeySignaturePair{{ExtendedPublicKey: "xpub"}},
			DerivationPath:       "m/0/0",
		}},
	}
	serialized, err := serialization.SerializePartiallySignedTransaction(partiallySignedTransaction)
	if err != nil {
		t.Fatalf("SerializePartiallySignedTransaction: %+v", err)
	}

	conf := &parseConfig{
		Transaction:  encodeTransactionsToHex([][]byte{serialized}),
		NetworkFlags: config.NetworkFlags{ActiveNetParams: &dagconfig.MainnetParams},
	}
	if err := parse(conf); err != nil {
		t.Fatalf("parse: %+v", err)
	}
}
