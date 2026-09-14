package main

import (
	"testing"

	"github.com/HoosatNetwork/HTND/cmd/htnwallet/libhtnwallet"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/txscript"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/utxo"
	"github.com/HoosatNetwork/HTND/domain/dagconfig"
	"github.com/HoosatNetwork/HTND/util"
)

// TestSweepSplitsIncludeEveryUTXO pins that sweeping more coins than fit in one transaction spends every one
// of them. When a coin pushed a split over the mass limit, the previous split was committed without it and
// the loop moved on to the next coin, so one coin per split boundary was never swept.
func TestSweepSplitsIncludeEveryUTXO(t *testing.T) {
	params := &dagconfig.MainnetParams
	toAddress, err := util.NewAddressPublicKey(make([]byte, 32), params.Prefix)
	if err != nil {
		t.Fatalf("NewAddressPublicKey: %+v", err)
	}
	scriptPublicKey, err := txscript.PayToAddrScript(toAddress)
	if err != nil {
		t.Fatalf("PayToAddrScript: %+v", err)
	}

	const utxoCount = 200
	const amount = 1_000_000
	utxos := make([]*libhtnwallet.UTXO, utxoCount)
	for i := range utxos {
		utxos[i] = &libhtnwallet.UTXO{
			Outpoint: &externalapi.DomainOutpoint{
				TransactionID: *externalapi.NewDomainTransactionIDFromByteArray(&[externalapi.DomainHashSize]byte{byte(i), byte(i >> 8)}),
			},
			UTXOEntry: utxo.NewUTXOEntry(amount, scriptPublicKey, false, 1),
		}
	}

	splits, err := createSplitTransactionsWithSchnorrPrivteKey(params, utxos, toAddress, feePerInput)
	if err != nil {
		t.Fatalf("createSplitTransactionsWithSchnorrPrivteKey: %+v", err)
	}
	if len(splits) < 2 {
		t.Fatalf("test setup: expected the sweep to need several transactions, got %d", len(splits))
	}

	spent := make(map[externalapi.DomainOutpoint]int)
	totalOutputs := uint64(0)
	totalInputs := 0
	for _, split := range splits {
		for _, input := range split.Inputs {
			spent[input.PreviousOutpoint]++
		}
		totalInputs += len(split.Inputs)
		totalOutputs += split.Outputs[0].Value
	}
	for _, u := range utxos {
		if spent[*u.Outpoint] != 1 {
			t.Fatalf("UTXO %s is spent by %d sweep transactions, want 1 (%d of %d coins swept)",
				u.Outpoint.TransactionID, spent[*u.Outpoint], len(spent), utxoCount)
		}
	}
	if want := uint64(utxoCount*amount) - uint64(totalInputs)*feePerInput; totalOutputs != want {
		t.Fatalf("sweep outputs total %d, want %d", totalOutputs, want)
	}
}
