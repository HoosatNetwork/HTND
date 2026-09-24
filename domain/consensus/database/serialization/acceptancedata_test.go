package serialization

import (
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/subnetworks"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/transactionid"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/utxo"
)

// TestAcceptanceDataRoundTripsAnAbsentInputEntry checks that an accepted transaction whose input
// spends a coin the set does not hold - a nil entry - survives storage as nil, next to a real entry
// with an empty script, which must not be mistaken for it.
func TestAcceptanceDataRoundTripsAnAbsentInputEntry(t *testing.T) {
	txID, err := transactionid.FromString("1111111111111111111111111111111111111111111111111111111111111111")
	if err != nil {
		t.Fatal(err)
	}
	held := utxo.NewUTXOEntry(10, &externalapi.ScriptPublicKey{Script: []byte{}, Version: 0}, false, 3)
	transaction := &externalapi.DomainTransaction{
		SubnetworkID: subnetworks.SubnetworkIDNative,
		Inputs: []*externalapi.DomainTransactionInput{
			{PreviousOutpoint: *externalapi.NewDomainOutpoint(txID, 0), UTXOEntry: held},
			{PreviousOutpoint: *externalapi.NewDomainOutpoint(txID, 1), UTXOEntry: nil},
		},
		Outputs: []*externalapi.DomainTransactionOutput{
			{Value: 5, ScriptPublicKey: &externalapi.ScriptPublicKey{Script: []byte{0x51}, Version: 0}},
		},
		Payload: []byte{},
	}
	acceptanceData := externalapi.AcceptanceData{{
		BlockHash: externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{7}),
		TransactionAcceptanceData: []*externalapi.TransactionAcceptanceData{{
			Transaction:                 transaction,
			IsAccepted:                  true,
			TransactionInputUTXOEntries: []externalapi.UTXOEntry{held, nil},
		}},
	}}

	bytes, err := DomainAcceptanceDataToDbAcceptanceData(acceptanceData).MarshalVT()
	if err != nil {
		t.Fatalf("MarshalVT: %+v", err)
	}
	dbAcceptanceData := &DbAcceptanceData{}
	if err := dbAcceptanceData.UnmarshalVT(bytes); err != nil {
		t.Fatalf("UnmarshalVT: %+v", err)
	}
	decoded, err := DbAcceptanceDataToDomainAcceptanceData(dbAcceptanceData)
	if err != nil {
		t.Fatalf("DbAcceptanceDataToDomainAcceptanceData: %+v", err)
	}

	entries := decoded[0].TransactionAcceptanceData[0].TransactionInputUTXOEntries
	if len(entries) != 2 {
		t.Fatalf("got %d input entries, want 2", len(entries))
	}
	if entries[0] == nil || !entries[0].Equal(held) {
		t.Fatalf("held entry did not round-trip: %v", entries[0])
	}
	if entries[1] != nil {
		t.Fatalf("absent entry decoded as %v, want nil", entries[1])
	}
	if decoded[0].TransactionAcceptanceData[0].Transaction.Inputs[1].UTXOEntry != nil {
		t.Fatal("absent input's UTXOEntry must stay nil")
	}
}
