package serialization

import (
	unsafe "unsafe"

	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/constants"
)

// DbHashToDomainHash converts a DbHash to a DomainHash
func DbHashToDomainHash(dbHash *DbHash) (*externalapi.DomainHash, error) {
	return externalapi.NewDomainHashFromByteSlice(dbHash.Hash)
}

// DomainHashToDbHash converts a DomainHash to a DbHash
func DomainHashToDbHash(domainHash *externalapi.DomainHash) *DbHash {
	hashBytes := unsafe.Slice((*byte)(unsafe.Pointer(domainHash)), constants.DomainHashSize)
	return &DbHash{Hash: hashBytes}
}

// DomainHashesToDbHashes converts a slice of DomainHash to a slice of DbHash
func DomainHashesToDbHashes(domainHashes []*externalapi.DomainHash) []*DbHash {
	dbHashes := make([]*DbHash, len(domainHashes))
	for i, domainHash := range domainHashes {
		dbHashes[i] = DomainHashToDbHash(domainHash)
	}
	return dbHashes
}

// DbHashesToDomainHashes converts a slice of DbHash to a slice of DomainHash
func DbHashesToDomainHashes(dbHashes []*DbHash) ([]*externalapi.DomainHash, error) {
	domainHashes := make([]*externalapi.DomainHash, len(dbHashes))
	for i, domainHash := range dbHashes {
		var err error
		domainHashes[i], err = DbHashToDomainHash(domainHash)
		if err != nil {
			return nil, err
		}
	}
	return domainHashes, nil
}
