package consensusstatestore

import (
	"github.com/Hoosat-Oy/HTND/domain/consensus/database/serialization"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
)

func serializeOutpoint(outpoint *externalapi.DomainOutpoint) ([]byte, error) {
	return serialization.DomainOutpointToDbOutpoint(outpoint).MarshalVT()
}

func serializeUTXOEntry(entry externalapi.UTXOEntry) ([]byte, error) {
	return serialization.UTXOEntryToDBUTXOEntry(entry).MarshalVT()
}

func deserializeOutpoint(outpointBytes []byte) (*externalapi.DomainOutpoint, error) {
	dbOutpoint := &serialization.DbOutpoint{}
	err := dbOutpoint.UnmarshalVT(outpointBytes)
	if err != nil {
		return nil, err
	}

	return serialization.DbOutpointToDomainOutpoint(dbOutpoint)
}

func deserializeUTXOEntry(entryBytes []byte) (externalapi.UTXOEntry, error) {
	dbEntry := &serialization.DbUtxoEntry{}
	err := dbEntry.UnmarshalVT(entryBytes)
	if err != nil {
		return nil, err
	}
	return serialization.DBUTXOEntryToUTXOEntry(dbEntry)
}
