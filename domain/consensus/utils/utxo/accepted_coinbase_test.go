package utxo

import (
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/subnetworks"
)

// TestIsAcceptedCoinbaseUsesTheTransactionNotItsPosition pins the property that the diff and the
// multiset must never disagree on.
//
// isCoinbase is serialized into the MuHash preimage by SerializeUTXO, so it is part of a coin's
// committed identity. The diff has always derived it from the transaction's subnetwork ID, while the
// multiset derived it from the transaction's position in its block's acceptance data. For a valid
// block those agree, because consensus puts the coinbase at index 0 - but they are different
// questions, and a block where they differ produces a UTXO commitment that does not match its header
// with nothing else about the block being wrong, which is the hardest possible failure to diagnose.
func TestIsAcceptedCoinbaseUsesTheTransactionNotItsPosition(t *testing.T) {
	coinbase := &externalapi.DomainTransaction{SubnetworkID: subnetworks.SubnetworkIDCoinbase}
	regular := &externalapi.DomainTransaction{SubnetworkID: subnetworks.SubnetworkIDNative}

	tests := []struct {
		name        string
		transaction *externalapi.DomainTransaction
		position    int
		expected    bool
	}{
		{"coinbase at index 0 - the ordinary case, both rules agree", coinbase, 0, true},
		{"regular transaction after it - both rules agree", regular, 3, false},
		// The cases that matter: the two rules disagree, and the answer must come from the
		// transaction, because that is what the UTXO diff - which maintains the real UTXO set -
		// uses when it stamps the entry.
		{"coinbase somewhere other than index 0", coinbase, 2, true},
		{"non-coinbase at index 0", regular, 0, false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := IsAcceptedCoinbase(test.transaction, test.position); got != test.expected {
				t.Errorf("expected coinbase=%t, got %t", test.expected, got)
			}
		})
	}
}

// The flag has to reach the serialized preimage, or unifying the definition changes nothing that
// matters. Two entries differing only in isCoinbase must serialize differently.
func TestIsCoinbaseChangesTheCommittedPreimage(t *testing.T) {
	script := &externalapi.ScriptPublicKey{Script: []byte{0x51}, Version: 0}
	outpoint := &externalapi.DomainOutpoint{Index: 0}

	asCoinbase, err := SerializeUTXO(NewUTXOEntry(100, script, true, 500), outpoint)
	if err != nil {
		t.Fatalf("SerializeUTXO: %+v", err)
	}
	asRegular, err := SerializeUTXO(NewUTXOEntry(100, script, false, 500), outpoint)
	if err != nil {
		t.Fatalf("SerializeUTXO: %+v", err)
	}
	if string(asCoinbase) == string(asRegular) {
		t.Fatal("isCoinbase must be part of the committed preimage, or the diff and the multiset " +
			"could disagree about it with no effect on the commitment - and then this whole " +
			"distinction would be pointless")
	}
}
