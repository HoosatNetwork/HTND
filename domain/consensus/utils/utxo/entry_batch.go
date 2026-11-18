package utxo

import (
	"bytes"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
)

const entryBatchChunkLen = 512

// EntryBatch allocates short-lived entries in chunks and shares an immutable
// script among entries paying to the same script. A surviving entry keeps its
// entire chunk alive, so batches should not be used for long-lived caches.
type EntryBatch struct {
	chunk    []utxoEntry
	shared   *externalapi.ScriptPublicKey
	sizeHint int
}

// NewEntryBatch copies shared and uses sizeHint to avoid oversized chunks for
// small reads. A nil shared script disables script sharing.
func NewEntryBatch(shared *externalapi.ScriptPublicKey, sizeHint int) *EntryBatch {
	batch := &EntryBatch{sizeHint: max(sizeHint, 0)}
	if shared != nil {
		batch.shared = &externalapi.ScriptPublicKey{Script: bytes.Clone(shared.Script), Version: shared.Version}
	}
	return batch
}

// New copies script unless it matches the batch's shared script.
func (b *EntryBatch) New(amount uint64, script []byte, version uint16, isCoinbase bool,
	blockDAAScore uint64,
) externalapi.UTXOEntry {
	if len(b.chunk) == cap(b.chunk) {
		n := entryBatchChunkLen
		if b.sizeHint > 0 {
			n = min(n, b.sizeHint)
			b.sizeHint -= n
		}
		b.chunk = make([]utxoEntry, 0, n)
	}
	scriptPublicKey := b.shared
	if scriptPublicKey == nil || version != scriptPublicKey.Version || !bytes.Equal(script, scriptPublicKey.Script) {
		scriptPublicKey = &externalapi.ScriptPublicKey{Script: bytes.Clone(script), Version: version}
	}
	b.chunk = append(b.chunk, utxoEntry{
		amount:          amount,
		scriptPublicKey: scriptPublicKey,
		blockDAAScore:   blockDAAScore,
		isCoinbase:      isCoinbase,
	})
	return &b.chunk[len(b.chunk)-1]
}
