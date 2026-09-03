package utxoderive

import (
	"bufio"
	"encoding/binary"
	"io"
	"os"

	consensusdatabase "github.com/HoosatNetwork/HTND/domain/consensus/database"
	"github.com/HoosatNetwork/HTND/domain/consensus/datastructures/multisetstore"
	"github.com/HoosatNetwork/HTND/domain/consensus/datastructures/pruningstore"
	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/multiset"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/utxo"
	infrastructuredatabase "github.com/HoosatNetwork/HTND/infrastructure/db/database"
	"github.com/HoosatNetwork/HTND/util/staging"
	"github.com/pkg/errors"
)

// snapshotMagic identifies the file and its version. A snapshot is a pruning-point UTXO set
// pinned to one specific pruning point, nothing more.
//
// This exists because of a measured result, not a theory: htnd's forward path was audited against
// an independent replay of the same bodies from the same anchor over 61,044 consecutive blocks
// and agreed on every one. Nodes that start from the same pruning-point set therefore stay in
// agreement; the divergence between them comes entirely from having imported different sets.
// Handing every node the same bytes is what makes their balances match.
//
// Note carefully what this does NOT do. A snapshot is not evidence that the set inside it is
// correct - no set on this network currently reconciles with its own header commitment. It makes
// nodes agree with each other, which is a strictly weaker and much cheaper property.
var snapshotMagic = [8]byte{'H', 'T', 'N', 'D', 'U', 'T', 'X', '1'}

// SnapshotHeader is the fixed-size prologue of a snapshot file.
type SnapshotHeader struct {
	PruningPoint *externalapi.DomainHash
	Multiset     *externalapi.DomainHash
	EntryCount   uint64
}

// ExportSnapshot writes a datadir's served pruning-point UTXO set to a file, together with the
// pruning point it belongs to and a multiset over the entries so an importer can detect a
// truncated or altered transfer.
//
// Read-only with respect to the source.
func ExportSnapshot(stores Stores, path string) (*SnapshotHeader, error) {
	stagingArea := model.NewStagingArea()
	pruningPoint, err := stores.PruningStore.PruningPoint(stores.DatabaseContext, stagingArea)
	if err != nil {
		return nil, errors.Wrap(err, "utxoderive: could not read the pruning point to export")
	}

	file, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	writer := bufio.NewWriterSize(file, 1<<20)

	// The header is written twice: a placeholder now, then again with the real count and multiset
	// once the entries have been streamed, so the file never has to be built in memory.
	placeholder := make([]byte, snapshotHeaderLen)
	if _, err := writer.Write(placeholder); err != nil {
		return nil, err
	}

	iterator, err := stores.PruningStore.PruningPointUTXOIterator(stores.DatabaseContext)
	if err != nil {
		return nil, err
	}
	defer iterator.Close()

	ms := multiset.New()
	count := uint64(0)
	lengthBuffer := make([]byte, 4)
	for ok := iterator.First(); ok; ok = iterator.Next() {
		outpoint, entry, err := iterator.Get()
		if err != nil {
			return nil, err
		}
		serialized, err := utxo.SerializeUTXO(entry, outpoint)
		if err != nil {
			return nil, err
		}
		ms.Add(serialized)

		binary.LittleEndian.PutUint32(lengthBuffer, uint32(len(serialized)))
		if _, err := writer.Write(lengthBuffer); err != nil {
			return nil, err
		}
		if _, err := writer.Write(serialized); err != nil {
			return nil, err
		}
		count++
	}
	if count == 0 {
		return nil, errors.Errorf("utxoderive: the served pruning-point UTXO set at %s is empty, "+
			"there is nothing to export", pruningPoint)
	}
	if err := writer.Flush(); err != nil {
		return nil, err
	}

	header := &SnapshotHeader{PruningPoint: pruningPoint, Multiset: ms.Hash(), EntryCount: count}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	if _, err := file.Write(serializeSnapshotHeader(header)); err != nil {
		return nil, err
	}
	return header, file.Sync()
}

// ReadSnapshotHeader reads and validates a snapshot's prologue without importing it.
func ReadSnapshotHeader(path string) (*SnapshotHeader, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	buffer := make([]byte, snapshotHeaderLen)
	if _, err := io.ReadFull(file, buffer); err != nil {
		return nil, errors.Wrap(err, "utxoderive: snapshot is too short to contain a header")
	}
	return deserializeSnapshotHeader(buffer)
}

// ImportSnapshot replaces a datadir's served pruning-point UTXO set with a snapshot's, and moves
// the node's own multiset anchor for that pruning point to match.
//
// Both writes are necessary and neither is sufficient alone. The bucket is what the node serves
// and what its virtual set is built from; the anchor is the running multiset every later block
// extends and every mined block commits to. Writing one without the other leaves the node
// internally inconsistent in exactly the way this whole investigation has been chasing.
//
// Refuses unless the snapshot's pruning point is the datadir's current pruning point. Importing a
// set belonging to a different pruning point would silently install a UTXO set for the wrong
// height, which is worse than any problem it could solve.
func ImportSnapshot(db infrastructuredatabase.Database, prefixBytes []byte, path string,
	batchSize int,
) (*SnapshotHeader, error) {
	dbManager := consensusdatabase.New(db)
	prefixBucket := consensusdatabase.MakeBucket(prefixBytes)
	pruningStore := pruningstore.New(prefixBucket, 2, false)
	multisetStore := multisetstore.New(prefixBucket, 100, false)

	stagingArea := model.NewStagingArea()
	currentPruningPoint, err := pruningStore.PruningPoint(dbManager, stagingArea)
	if err != nil {
		return nil, errors.Wrap(err, "utxoderive: could not read the destination's pruning point")
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	reader := bufio.NewReaderSize(file, 1<<20)

	headerBuffer := make([]byte, snapshotHeaderLen)
	if _, err := io.ReadFull(reader, headerBuffer); err != nil {
		return nil, errors.Wrap(err, "utxoderive: snapshot is too short to contain a header")
	}
	header, err := deserializeSnapshotHeader(headerBuffer)
	if err != nil {
		return nil, err
	}

	if !header.PruningPoint.Equal(currentPruningPoint) {
		return nil, errors.Errorf("utxoderive: snapshot is for pruning point %s but this datadir is at "+
			"%s. Importing it would install a UTXO set for the wrong height - refusing",
			header.PruningPoint, currentPruningPoint)
	}

	if err := pruningStore.ClearImportedPruningPointUTXOs(dbManager); err != nil {
		return nil, err
	}

	ms := multiset.New()
	batch := make([]*externalapi.OutpointAndUTXOEntryPair, 0, batchSize)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		dbTx, err := dbManager.Begin()
		if err != nil {
			return err
		}
		if err := pruningStore.AppendImportedPruningPointUTXOs(dbTx, batch); err != nil {
			dbTx.RollbackUnlessClosed()
			return err
		}
		if err := dbTx.Commit(); err != nil {
			return err
		}
		batch = batch[:0]
		return nil
	}

	lengthBuffer := make([]byte, 4)
	for i := uint64(0); i < header.EntryCount; i++ {
		if _, err := io.ReadFull(reader, lengthBuffer); err != nil {
			return nil, errors.Wrapf(err, "utxoderive: snapshot ended after %d of %d entries",
				i, header.EntryCount)
		}
		serialized := make([]byte, binary.LittleEndian.Uint32(lengthBuffer))
		if _, err := io.ReadFull(reader, serialized); err != nil {
			return nil, errors.Wrapf(err, "utxoderive: snapshot entry %d is truncated", i)
		}
		entry, outpoint, err := utxo.DeserializeUTXO(serialized)
		if err != nil {
			return nil, errors.Wrapf(err, "utxoderive: snapshot entry %d could not be decoded", i)
		}
		ms.Add(serialized)
		batch = append(batch, &externalapi.OutpointAndUTXOEntryPair{Outpoint: outpoint, UTXOEntry: entry})
		if len(batch) >= batchSize {
			if err := flush(); err != nil {
				return nil, err
			}
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}

	// Integrity: the entries actually read must hash to what the exporter recorded. A truncated or
	// altered transfer is precisely how an import goes wrong silently, so it is checked before
	// anything is promoted.
	if !ms.Hash().Equal(header.Multiset) {
		return nil, errors.Errorf("utxoderive: snapshot content hashes to %s but its header claims %s - "+
			"the transfer is corrupt and nothing was promoted", ms.Hash(), header.Multiset)
	}

	if err := pruningStore.CommitImportedPruningPointUTXOSet(dbManager); err != nil {
		return nil, err
	}

	// Move the anchor to match the set that was just installed.
	anchorArea := model.NewStagingArea()
	multisetStore.Stage(anchorArea, header.PruningPoint, ms)
	if err := staging.CommitAllChanges(dbManager, anchorArea); err != nil {
		return nil, err
	}

	return header, nil
}

const snapshotHeaderLen = 8 + externalapi.DomainHashSize + externalapi.DomainHashSize + 8

func serializeSnapshotHeader(header *SnapshotHeader) []byte {
	buffer := make([]byte, snapshotHeaderLen)
	copy(buffer[0:8], snapshotMagic[:])
	copy(buffer[8:], header.PruningPoint.ByteSlice())
	copy(buffer[8+externalapi.DomainHashSize:], header.Multiset.ByteSlice())
	binary.LittleEndian.PutUint64(buffer[8+2*externalapi.DomainHashSize:], header.EntryCount)
	return buffer
}

func deserializeSnapshotHeader(buffer []byte) (*SnapshotHeader, error) {
	if len(buffer) != snapshotHeaderLen {
		return nil, errors.Errorf("utxoderive: snapshot header is %d bytes, expected %d",
			len(buffer), snapshotHeaderLen)
	}
	for i := range snapshotMagic {
		if buffer[i] != snapshotMagic[i] {
			return nil, errors.Errorf("utxoderive: not a snapshot file (bad magic)")
		}
	}
	pruningPoint, err := externalapi.NewDomainHashFromByteSlice(buffer[8 : 8+externalapi.DomainHashSize])
	if err != nil {
		return nil, err
	}
	multisetHash, err := externalapi.NewDomainHashFromByteSlice(
		buffer[8+externalapi.DomainHashSize : 8+2*externalapi.DomainHashSize])
	if err != nil {
		return nil, err
	}
	return &SnapshotHeader{
		PruningPoint: pruningPoint,
		Multiset:     multisetHash,
		EntryCount:   binary.LittleEndian.Uint64(buffer[8+2*externalapi.DomainHashSize:]),
	}, nil
}
