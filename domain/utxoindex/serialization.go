package utxoindex

import (
	"encoding/binary"
	"io"
	"math"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/database/serialization"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/utxo"
	"github.com/pkg/errors"
)

func serializeOutpoint(outpoint *externalapi.DomainOutpoint) ([]byte, error) {
	dbOutpoint := serialization.DomainOutpointToDbOutpoint(outpoint)
	return dbOutpoint.MarshalVT()
}

func deserializeOutpoint(serializedOutpoint []byte) (*externalapi.DomainOutpoint, error) {
	dbOutpoint := &serialization.DbOutpoint{}
	err := dbOutpoint.UnmarshalVT(serializedOutpoint)
	if err != nil {
		return nil, err
	}
	outpoint, err := serialization.DbOutpointToDomainOutpoint(dbOutpoint)
	if err != nil {
		return nil, err
	}
	return outpoint, nil
}

func serializeUTXOEntry(utxoEntry externalapi.UTXOEntry) ([]byte, error) {
	dbUTXOEntry := serialization.UTXOEntryToDBUTXOEntry(utxoEntry)
	return dbUTXOEntry.MarshalVT()
}

// batchEntryDecoder decodes the entries of one bulk read into an EntryBatch, without allocating
// anything per entry beyond the batch's own chunks: the protobuf message is reused, and decoded
// unsafely, so its script aliases the cursor's value until EntryBatch.New copies or shares it.
type batchEntryDecoder struct {
	entry  serialization.DbUtxoEntry
	script serialization.DbScriptPublicKey
	batch  *utxo.EntryBatch
}

func newBatchEntryDecoder(batch *utxo.EntryBatch) *batchEntryDecoder {
	return &batchEntryDecoder{batch: batch}
}

// decode returns the entry serializedUTXOEntry holds. The entry does not alias serializedUTXOEntry,
// so it outlives the cursor's next move.
func (d *batchEntryDecoder) decode(serializedUTXOEntry []byte) (externalapi.UTXOEntry, error) {
	// Reset every time: proto3 leaves zero values out of the encoding, so a field the previous
	// entry set - IsCoinbase, say - would otherwise carry over into this one.
	d.entry = serialization.DbUtxoEntry{}
	d.script = serialization.DbScriptPublicKey{}
	d.entry.ScriptPublicKey = &d.script
	err := d.entry.UnmarshalVTUnsafe(serializedUTXOEntry)
	if err != nil {
		return nil, err
	}
	if d.script.Version > math.MaxUint16 {
		return nil, errors.Errorf("script public key version is bigger than uint16")
	}
	return d.batch.New(d.entry.Amount, d.script.Script, uint16(d.script.Version), d.entry.IsCoinbase,
		d.entry.BlockDaaScore), nil
}

func deserializeUTXOAmount(serializedUTXOEntry []byte) (uint64, error) {
	dbUTXOEntry := &serialization.DbUtxoEntry{}
	err := dbUTXOEntry.UnmarshalVT(serializedUTXOEntry)
	if err != nil {
		return 0, err
	}
	amount := dbUTXOEntry.Amount
	return amount, nil
}

const hashesLengthSize = 8

func serializeHashes(hashes []*externalapi.DomainHash) []byte {
	serializedHashes := make([]byte, hashesLengthSize+externalapi.DomainHashSize*len(hashes))
	binary.LittleEndian.PutUint64(serializedHashes[:hashesLengthSize], uint64(len(hashes)))
	for i, hash := range hashes {
		start := hashesLengthSize + externalapi.DomainHashSize*i
		end := start + externalapi.DomainHashSize
		copy(serializedHashes[start:end], hash.ByteSlice())
	}
	return serializedHashes
}

func deserializeHashes(serializedHashes []byte) ([]*externalapi.DomainHash, error) {
	length := binary.LittleEndian.Uint64(serializedHashes[:hashesLengthSize])
	hashes := make([]*externalapi.DomainHash, length)
	for i := range length {
		start := hashesLengthSize + externalapi.DomainHashSize*i
		end := start + externalapi.DomainHashSize

		if end > uint64(len(serializedHashes)) {
			return nil, errors.Wrapf(io.ErrUnexpectedEOF, "unexpected EOF while deserializing hashes")
		}

		var err error
		hashes[i], err = externalapi.NewDomainHashFromByteSlice(serializedHashes[start:end])
		if err != nil {
			return nil, err
		}
	}

	return hashes, nil
}
