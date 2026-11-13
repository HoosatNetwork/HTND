package utxoindex

import (
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	consensusutxo "github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/utxo"
)

// The decoder reuses one protobuf message, and proto3 leaves zero values out of the encoding, so
// an entry whose fields are zero must not inherit the previous entry's.
func TestBatchEntryDecoderDoesNotCarryFieldsOver(t *testing.T) {
	addressScript := &externalapi.ScriptPublicKey{Script: []byte{0x51, 0x21, 0x02}, Version: 0}
	full, err := serializeUTXOEntry(consensusutxo.NewUTXOEntry(1000, addressScript, true, 77))
	if err != nil {
		t.Fatalf("serializeUTXOEntry: %v", err)
	}
	zero, err := serializeUTXOEntry(consensusutxo.NewUTXOEntry(0, &externalapi.ScriptPublicKey{}, false, 0))
	if err != nil {
		t.Fatalf("serializeUTXOEntry: %v", err)
	}

	decoder := newBatchEntryDecoder(consensusutxo.NewEntryBatch(addressScript, 2, nil))
	first, err := decoder.decode(full)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	second, err := decoder.decode(zero)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	if !first.Equal(consensusutxo.NewUTXOEntry(1000, addressScript, true, 77)) {
		t.Fatalf("first entry decoded as amount=%d coinbase=%t daaScore=%d script=%x",
			first.Amount(), first.IsCoinbase(), first.BlockDAAScore(), first.ScriptPublicKey().Script)
	}
	if second.Amount() != 0 || second.IsCoinbase() || second.BlockDAAScore() != 0 ||
		len(second.ScriptPublicKey().Script) != 0 {
		t.Fatalf("an all-zero entry decoded as amount=%d coinbase=%t daaScore=%d script=%x: fields of the "+
			"entry before it carried over", second.Amount(), second.IsCoinbase(), second.BlockDAAScore(),
			second.ScriptPublicKey().Script)
	}
}

// The decoder parses without copying, so the script it reads aliases the cursor's value, which the
// cursor's next move overwrites. The entry must not keep that alias.
func TestBatchEntryDecoderEntryOutlivesItsBytes(t *testing.T) {
	addressScript := &externalapi.ScriptPublicKey{Script: []byte{0x51, 0x21, 0x02}, Version: 0}
	foreignScript := &externalapi.ScriptPublicKey{Script: []byte{0x76, 0xa9}, Version: 0}

	for name, script := range map[string]*externalapi.ScriptPublicKey{"shared": addressScript, "own": foreignScript} {
		serialized, err := serializeUTXOEntry(consensusutxo.NewUTXOEntry(5, script, false, 9))
		if err != nil {
			t.Fatalf("serializeUTXOEntry: %v", err)
		}
		decoder := newBatchEntryDecoder(consensusutxo.NewEntryBatch(addressScript, 1, nil))
		entry, err := decoder.decode(serialized)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		clear(serialized)
		if !entry.Equal(consensusutxo.NewUTXOEntry(5, script, false, 9)) {
			t.Fatalf("%s script: entry changed when the bytes it was decoded from were overwritten", name)
		}
	}
}
