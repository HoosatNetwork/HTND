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

type batchEntryDecoder struct {
	entry serialization.DbUtxoEntry
	batch *utxo.EntryBatch
}

func (d *batchEntryDecoder) decode(serializedUTXOEntry []byte) (externalapi.UTXOEntry, error) {
	// Unsafe decoding aliases the cursor value. EntryBatch.New copies or shares
	// the script before the cursor advances.
	d.entry = serialization.DbUtxoEntry{}
	err := d.entry.UnmarshalVTUnsafe(serializedUTXOEntry)
	if err != nil {
		return nil, err
	}
	script := d.entry.ScriptPublicKey
	if script == nil {
		return nil, errors.New("utxo entry is missing its script public key")
	}
	if script.Version > math.MaxUint16 {
		return nil, errors.New("script public key version is bigger than uint16")
	}
	return d.batch.New(d.entry.Amount, script.Script, uint16(script.Version), d.entry.IsCoinbase,
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
