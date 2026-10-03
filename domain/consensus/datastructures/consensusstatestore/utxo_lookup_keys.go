package consensusstatestore

import (
	"bytes"
	"slices"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/database/serialization"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/pkg/errors"
)

// maxOutpointSuffix is enough for a DbOutpoint: a 32-byte transaction id with its
// protobuf tags, plus a uint32 index. Measured keys are 36 bytes for index 0 and
// 38 to 42 once the index is present.
const maxOutpointSuffix = 48

// outpointKeyEncoder marshals an outpoint into a reused buffer. One is enough for a
// whole bulk lookup: the previous key's bytes have already been copied into the slab.
type outpointKeyEncoder struct {
	outpoint serialization.DbOutpoint
	txid     serialization.DbTransactionId
	raw      [externalapi.DomainHashSize]byte
	buf      [maxOutpointSuffix]byte
}

func (e *outpointKeyEncoder) append(dst []byte, prefix []byte, outpoint *externalapi.DomainOutpoint) ([]byte, error) {
	e.txid.TransactionId = outpoint.TransactionID.AppendBytes(e.raw[:0])
	e.outpoint.TransactionID = &e.txid
	e.outpoint.Index = outpoint.Index
	size := e.outpoint.SizeVT()
	if size > len(e.buf) {
		return nil, errors.Errorf("virtual UTXO key for %s is %d bytes", outpoint, size)
	}
	n, err := e.outpoint.MarshalToSizedBufferVT(e.buf[:size])
	if err != nil {
		return nil, err
	}
	dst = append(dst, prefix...)
	dst = append(dst, e.buf[size-n:size]...)
	return dst, nil
}

// utxoEntryEncoder marshals a UTXO entry into a reused buffer so a bulk lookup can
// compare it with the bytes the database already holds, and skip decoding when they match.
type utxoEntryEncoder struct {
	entry  serialization.DbUtxoEntry
	script serialization.DbScriptPublicKey
	buf    []byte
}

func (e *utxoEntryEncoder) matches(entry externalapi.UTXOEntry, raw []byte) (bool, error) {
	if spk := entry.ScriptPublicKey(); spk == nil {
		e.entry.ScriptPublicKey = nil
	} else {
		e.script.Script = spk.Script
		e.script.Version = uint32(spk.Version)
		e.entry.ScriptPublicKey = &e.script
	}
	e.entry.Amount = entry.Amount()
	e.entry.BlockDaaScore = entry.BlockDAAScore()
	e.entry.IsCoinbase = entry.IsCoinbase()
	size := e.entry.SizeVT()
	if cap(e.buf) < size {
		e.buf = make([]byte, size)
	} else {
		e.buf = e.buf[:size]
	}
	n, err := e.entry.MarshalToSizedBufferVT(e.buf)
	if err != nil {
		return false, err
	}
	return bytes.Equal(e.buf[size-n:size], raw), nil
}

// prefixLimit is the exclusive upper bound of every key that starts with prefix,
// matching the bound a bucket cursor already uses.
func prefixLimit(prefix []byte) []byte {
	for i := len(prefix) - 1; i >= 0; i-- {
		if prefix[i] < 0xff {
			limit := make([]byte, i+1)
			copy(limit, prefix)
			limit[i]++
			return limit
		}
	}
	return nil
}

// keySuccessor returns the exclusive upper bound just past key. It returns nil when
// incrementing key overflows, which only happens for an all-0xff key.
func keySuccessor(key []byte) []byte {
	out := make([]byte, len(key))
	copy(out, key)
	for i := len(out) - 1; i >= 0; i-- {
		out[i]++
		if out[i] != 0 {
			return out
		}
	}
	return nil
}

func (css *consensusStateStore) cursorUpper(lastKey []byte) []byte {
	upper := keySuccessor(lastKey)
	if upper == nil || (css.utxoSetKeyLimit != nil && bytes.Compare(upper, css.utxoSetKeyLimit) > 0) {
		return css.utxoSetKeyLimit
	}
	return upper
}

// PrepareOrderedVirtualUTXOKeys encodes outpoints as virtual-UTXO-set keys and sorts
// them by those keys. It does not read the database.
func (css *consensusStateStore) PrepareOrderedVirtualUTXOKeys(
	outpoints []*externalapi.DomainOutpoint,
) ([]model.OrderedVirtualUTXOKey, error) {
	ordered := make([]model.OrderedVirtualUTXOKey, len(outpoints))
	if len(outpoints) == 0 {
		return ordered, nil
	}
	// The slab is filled before any key slice is taken from it. Appending can grow
	// the slab, and a slice taken earlier would then point at the old array.
	slab := make([]byte, 0, len(outpoints)*(len(css.utxoSetKeyPrefix)+40))
	ends := make([]int, len(outpoints))
	var encoder outpointKeyEncoder
	for i, outpoint := range outpoints {
		var err error
		slab, err = encoder.append(slab, css.utxoSetKeyPrefix, outpoint)
		if err != nil {
			return nil, err
		}
		ends[i] = len(slab)
	}
	start := 0
	for i := range outpoints {
		ordered[i].Index = i
		ordered[i].Key = slab[start:ends[i]]
		start = ends[i]
	}
	slices.SortFunc(ordered, func(a, b model.OrderedVirtualUTXOKey) int {
		return bytes.Compare(a.Key, b.Key)
	})
	return ordered, nil
}

type cursorRanger interface {
	CursorRange(bucket model.DBBucket, lower, upper []byte) (model.DBCursor, error)
}

func (css *consensusStateStore) openBoundedUTXOCursor(dbContext model.DBReader, lower, upper []byte) (model.DBCursor, error) {
	if ranger, ok := dbContext.(cursorRanger); ok {
		return ranger.CursorRange(css.utxoSetBucket, lower, upper)
	}
	return dbContext.Cursor(css.utxoSetBucket)
}
