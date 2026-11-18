package utxo

import (
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
)

func TestEntryBatchScriptsAndChunks(t *testing.T) {
	shared := &externalapi.ScriptPublicKey{Script: []byte{1, 2, 3}, Version: 1}
	batch := NewEntryBatch(shared, 2)
	input := []byte{1, 2, 3}
	first := batch.New(1, input, 1, true, 10)
	second := batch.New(2, input, 1, false, 20)
	foreign := batch.New(3, input, 2, false, 30)

	if first.ScriptPublicKey() != second.ScriptPublicKey() {
		t.Fatal("matching scripts were not shared")
	}
	if first.ScriptPublicKey() == shared || foreign.ScriptPublicKey() == first.ScriptPublicKey() {
		t.Fatal("shared the caller's script or a different script version")
	}
	input[0], shared.Script[0] = 9, 9
	if first.ScriptPublicKey().Script[0] != 1 || foreign.ScriptPublicKey().Script[0] != 1 {
		t.Fatal("an entry aliases caller-owned script bytes")
	}

	for i := 0; i < 2*entryBatchChunkLen; i++ {
		batch.New(uint64(i), nil, 0, false, 0)
	}
	if first.Amount() != 1 || !first.IsCoinbase() || first.BlockDAAScore() != 10 ||
		second.Amount() != 2 || foreign.Amount() != 3 {
		t.Fatal("earlier entries changed after the batch grew")
	}
}
