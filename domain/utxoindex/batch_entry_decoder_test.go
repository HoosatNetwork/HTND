package utxoindex

import (
	"math"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/database/serialization"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/utxo"
)

func TestBatchEntryDecoder(t *testing.T) {
	shared := &externalapi.ScriptPublicKey{Script: []byte{1, 2, 3}, Version: 0}
	foreign := &externalapi.ScriptPublicKey{Script: []byte{4, 5}, Version: 1}
	decoder := batchEntryDecoder{batch: utxo.NewEntryBatch(shared, 3)}

	for _, want := range []externalapi.UTXOEntry{
		utxo.NewUTXOEntry(1000, shared, true, 77),
		utxo.NewUTXOEntry(0, &externalapi.ScriptPublicKey{}, false, 0),
		utxo.NewUTXOEntry(5, foreign, false, 9),
	} {
		raw, err := serializeUTXOEntry(want)
		if err != nil {
			t.Fatal(err)
		}
		got, err := decoder.decode(raw)
		if err != nil {
			t.Fatal(err)
		}
		clear(raw)
		if !got.Equal(want) {
			t.Fatalf("decoded entry differs from original after cursor value changed")
		}
	}
}

func TestBatchEntryDecoderRejectsInvalidEntries(t *testing.T) {
	decoder := batchEntryDecoder{batch: utxo.NewEntryBatch(nil, 0)}
	for _, entry := range []*serialization.DbUtxoEntry{
		{},
		{ScriptPublicKey: &serialization.DbScriptPublicKey{Version: math.MaxUint16 + 1}},
	} {
		raw, err := entry.MarshalVT()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := decoder.decode(raw); err == nil {
			t.Fatalf("accepted invalid entry %v", entry)
		}
	}
	if _, err := decoder.decode([]byte{0x12, 0xff}); err == nil {
		t.Fatal("accepted truncated protobuf")
	}
}

func BenchmarkUTXOEntryDecode(b *testing.B) {
	script := &externalapi.ScriptPublicKey{Script: []byte{1, 2, 3}, Version: 0}
	raw, err := serializeUTXOEntry(utxo.NewUTXOEntry(1000, script, true, 77))
	if err != nil {
		b.Fatal(err)
	}
	const entriesPerRead = 1024
	for _, batched := range []bool{false, true} {
		name := "individual"
		if batched {
			name = "batched"
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				entries := make([]externalapi.UTXOEntry, entriesPerRead)
				if batched {
					decoder := batchEntryDecoder{batch: utxo.NewEntryBatch(script, entriesPerRead)}
					for i := range entries {
						entries[i], err = decoder.decode(raw)
						if err != nil {
							b.Fatal(err)
						}
					}
				} else {
					for i := range entries {
						message := &serialization.DbUtxoEntry{}
						if err := message.UnmarshalVT(raw); err != nil {
							b.Fatal(err)
						}
						entries[i], err = serialization.DBUTXOEntryToUTXOEntry(message)
						if err != nil {
							b.Fatal(err)
						}
					}
				}
			}
		})
	}
}
