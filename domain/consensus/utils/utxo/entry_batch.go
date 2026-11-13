package utxo

import (
	"bytes"
	"unsafe"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
)

// entryBatchChunkLen is how many entries an EntryBatch allocates at once: 16 KiB of utxoEntry.
const entryBatchChunkLen = 512

// EntryBatch allocates UTXO entries a chunk at a time, for a bulk reader that decodes many entries
// which all live and die together, such as one UTXO index read of an address.
//
// Allocated one by one, each entry is three heap objects - the entry, its ScriptPublicKey and the
// script bytes - and the collector marks every one of them. A batch makes the entry an element of a
// shared chunk, and lets every entry whose script equals the batch's shared one point at that one
// ScriptPublicKey instead of carrying a copy. Every coin an address holds pays to the same script,
// so for an index read that is all of them: 1.8M coins become about 3.5K objects instead of 5.4M.
//
// The flip side is that one entry still referenced keeps its whole chunk alive. That is what the
// bulk reader wants, and why a batch is wrong for entries that go on to outlive the rest, such as
// ones put into a cache.
//
// Entries from a batch, like all UTXO entries, are immutable, which is what makes sharing the
// ScriptPublicKey safe.
type EntryBatch struct {
	chunk    []utxoEntry
	shared   *externalapi.ScriptPublicKey
	sizeHint int
	retain   func(chunk any)
}

// NewEntryBatch returns a batch whose entries share shared, which the batch copies, wherever their
// script equals it. shared may be nil, and then every entry gets a script of its own.
//
// sizeHint is how many entries the caller expects, or 0 when it does not know. It sizes the first
// chunks so a small read does not allocate a full one, and is only a hint: more entries still fit.
//
// retain, when not nil, is called with every chunk the batch allocates, before any entry of it is
// handed out. A caller that stores the entries where the garbage collector does not look - a
// memory.Block - passes the block's Retain, so the block keeps the chunks alive instead of every
// entry needing a reference of its own.
func NewEntryBatch(shared *externalapi.ScriptPublicKey, sizeHint int, retain func(chunk any)) *EntryBatch {
	batch := &EntryBatch{sizeHint: max(sizeHint, 0), retain: retain}
	if shared != nil {
		batch.shared = &externalapi.ScriptPublicKey{Script: bytes.Clone(shared.Script), Version: shared.Version}
	}
	return batch
}

// New returns an entry with these contents. script is copied unless it equals the shared script, so
// the caller may pass bytes it reuses or that alias a database cursor.
func (b *EntryBatch) New(amount uint64, script []byte, version uint16, isCoinbase bool,
	blockDAAScore uint64,
) externalapi.UTXOEntry {
	if len(b.chunk) == cap(b.chunk) {
		b.grow()
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

func (b *EntryBatch) grow() {
	n := entryBatchChunkLen
	if b.sizeHint > 0 {
		n = min(n, b.sizeHint)
		b.sizeHint -= n
	}
	// A new chunk instead of a grown one: entries already handed out point into the old chunk,
	// so it must never move.
	b.chunk = make([]utxoEntry, 0, n)
	if b.retain != nil {
		// A pointer to the first element keeps the whole chunk alive, and a pointer-shaped value
		// boxes into an interface without allocating.
		b.retain(unsafe.SliceData(b.chunk))
	}
}
