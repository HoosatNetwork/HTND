package utxoindex

import (
	"encoding/binary"
	"io"
	"sync"

	"github.com/Hoosat-Oy/HTND/domain/consensus/database/serialization"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/pkg/errors"
)

var dbUtxoEntryPool = sync.Pool{
	New: func() interface{} {
		return &serialization.DbUtxoEntry{}
	},
}

var dbOutpointPool = sync.Pool{
	New: func() interface{} {
		return &serialization.DbOutpoint{}
	},
}

func serializeOutpoint(outpoint *externalapi.DomainOutpoint) ([]byte, error) {
	dbOutpoint := serialization.DomainOutpointToDbOutpoint(outpoint)
	return dbOutpoint.MarshalVT()
}

func deserializeOutpoint(serializedOutpoint []byte) (*externalapi.DomainOutpoint, error) {
	dbOutpoint := dbOutpointPool.Get().(*serialization.DbOutpoint)
	err := dbOutpoint.UnmarshalVT(serializedOutpoint)
	if err != nil {
		dbOutpointPool.Put(dbOutpoint)
		return nil, err
	}
	outpoint, err := serialization.DbOutpointToDomainOutpoint(dbOutpoint)
	dbOutpointPool.Put(dbOutpoint)
	if err != nil {
		return nil, err
	}
	return outpoint, nil
}

func serializeUTXOEntry(utxoEntry externalapi.UTXOEntry) ([]byte, error) {
	dbUTXOEntry := serialization.UTXOEntryToDBUTXOEntry(utxoEntry)
	return dbUTXOEntry.MarshalVT()
}

func deserializeUTXOEntry(serializedUTXOEntry []byte) (externalapi.UTXOEntry, error) {
	dbUTXOEntry := dbUtxoEntryPool.Get().(*serialization.DbUtxoEntry)
	err := dbUTXOEntry.UnmarshalVT(serializedUTXOEntry)
	if err != nil {
		dbUtxoEntryPool.Put(dbUTXOEntry)
		return nil, err
	}
	utxoEntry, err := serialization.DBUTXOEntryToUTXOEntry(dbUTXOEntry)
	dbUtxoEntryPool.Put(dbUTXOEntry)
	return utxoEntry, err
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
