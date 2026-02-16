package utxoindex

import (
	"encoding/binary"
	"maps"
	"sync"

	"github.com/Hoosat-Oy/HTND/domain/consensus/database/binaryserialization"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/infrastructure/db/database"
	"github.com/Hoosat-Oy/HTND/infrastructure/logger"
	"github.com/pkg/errors"
)

var utxoIndexBucket = database.MakeBucket([]byte("utxo-index"))
var virtualParentsKey = database.MakeBucket([]byte("")).Key([]byte("utxo-index-virtual-parents"))
var circulatingSupplyKey = database.MakeBucket([]byte("")).Key([]byte("utxo-index-circulating-supply"))

type utxoIndexStore struct {
	database database.Database
	toAdd    map[ScriptPublicKeyString]UTXOOutpointEntryPairs
	toRemove map[ScriptPublicKeyString]UTXOOutpointEntryPairs

	virtualParents []*externalapi.DomainHash
}

func newUTXOIndexStore(database database.Database) *utxoIndexStore {
	return &utxoIndexStore{
		database: database,
		toAdd:    make(map[ScriptPublicKeyString]UTXOOutpointEntryPairs),
		toRemove: make(map[ScriptPublicKeyString]UTXOOutpointEntryPairs),
	}
}

func (uis *utxoIndexStore) add(scriptPublicKey *externalapi.ScriptPublicKey, outpoint *externalapi.DomainOutpoint, utxoEntry externalapi.UTXOEntry) error {

	key := ScriptPublicKeyString(scriptPublicKey.String())
	log.Tracef("Adding outpoint %s:%d to scriptPublicKey %s",
		outpoint.TransactionID, outpoint.Index, key)

	// If the outpoint exists in `toRemove` simply remove it from there and return
	if toRemoveOutpointsOfKey, ok := uis.toRemove[key]; ok {
		if _, ok := toRemoveOutpointsOfKey[*outpoint]; ok {
			log.Tracef("Outpoint %s:%d exists in `toRemove`. Deleting it from there",
				outpoint.TransactionID, outpoint.Index)
			delete(toRemoveOutpointsOfKey, *outpoint)
			return nil
		}
	}

	// Create a UTXOOutpointEntryPairs entry in `toAdd` if it doesn't exist
	if _, ok := uis.toAdd[key]; !ok {
		log.Tracef("Creating key %s in `toAdd`", key)
		uis.toAdd[key] = make(UTXOOutpointEntryPairs)
	}

	// Return an error if the outpoint already exists in `toAdd`
	toAddPairsOfKey := uis.toAdd[key]
	if _, ok := toAddPairsOfKey[*outpoint]; ok {
		return errors.Errorf("cannot add outpoint %s because it's being added already", outpoint)
	}

	toAddPairsOfKey[*outpoint] = utxoEntry

	log.Tracef("Added outpoint %s:%d to scriptPublicKey %s",
		outpoint.TransactionID, outpoint.Index, key)
	return nil
}

func (uis *utxoIndexStore) remove(scriptPublicKey *externalapi.ScriptPublicKey, outpoint *externalapi.DomainOutpoint, utxoEntry externalapi.UTXOEntry) error {
	key := ScriptPublicKeyString(scriptPublicKey.String())
	log.Tracef("Removing outpoint %s:%d from scriptPublicKey %s",
		outpoint.TransactionID, outpoint.Index, key)

	// If the outpoint exists in `toAdd` simply remove it from there and return
	if toAddPairsOfKey, ok := uis.toAdd[key]; ok {
		if _, ok := toAddPairsOfKey[*outpoint]; ok {
			log.Tracef("Outpoint %s:%d exists in `toAdd`. Deleting it from there",
				outpoint.TransactionID, outpoint.Index)
			delete(toAddPairsOfKey, *outpoint)
			return nil
		}
	}

	// Create a UTXOOutpointEntryPair in `toRemove` if it doesn't exist
	if _, ok := uis.toRemove[key]; !ok {
		log.Tracef("Creating key %s in `toRemove`", key)
		uis.toRemove[key] = make(UTXOOutpointEntryPairs)
	}

	// Return an error if the outpoint already exists in `toRemove`
	toRemovePairsOfKey := uis.toRemove[key]
	if _, ok := toRemovePairsOfKey[*outpoint]; ok {
		return errors.Errorf("cannot remove outpoint %s because it's being removed already", outpoint)
	}

	toRemovePairsOfKey[*outpoint] = utxoEntry

	log.Tracef("Removed outpoint %s:%d from scriptPublicKey %s",
		outpoint.TransactionID, outpoint.Index, key)
	return nil
}

func (uis *utxoIndexStore) updateVirtualParents(virtualParents []*externalapi.DomainHash) {
	uis.virtualParents = virtualParents
}

func (uis *utxoIndexStore) discard() {
	uis.toAdd = make(map[ScriptPublicKeyString]UTXOOutpointEntryPairs)
	uis.toRemove = make(map[ScriptPublicKeyString]UTXOOutpointEntryPairs)
	uis.virtualParents = nil
}

func (uis *utxoIndexStore) commit() error {
	onEnd := logger.LogAndMeasureExecutionTime(log, "utxoIndexStore.commit")
	defer onEnd()

	dbTransaction, err := uis.database.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = dbTransaction.RollbackUnlessClosed() }()

	toRemoveSompiSupply := uint64(0)

	for scriptPublicKeyString, toRemoveUTXOOutpointEntryPairs := range uis.toRemove {
		scriptPublicKey := externalapi.NewScriptPublicKeyFromString(string(scriptPublicKeyString))
		bucket := uis.bucketForScriptPublicKey(scriptPublicKey)
		for outpointToRemove, utxoEntryToRemove := range toRemoveUTXOOutpointEntryPairs {
			key, err := uis.convertOutpointToKey(bucket, &outpointToRemove)
			if err != nil {
				return err
			}
			err = dbTransaction.Delete(key)
			if err != nil {
				return err
			}
			toRemoveSompiSupply = toRemoveSompiSupply + utxoEntryToRemove.Amount()
		}
	}

	toAddSompiSupply := uint64(0)

	for scriptPublicKeyString, toAddUTXOOutpointEntryPairs := range uis.toAdd {
		scriptPublicKey := externalapi.NewScriptPublicKeyFromString(string(scriptPublicKeyString))
		bucket := uis.bucketForScriptPublicKey(scriptPublicKey)
		for outpointToAdd, utxoEntryToAdd := range toAddUTXOOutpointEntryPairs {
			key, err := uis.convertOutpointToKey(bucket, &outpointToAdd)
			if err != nil {
				return err
			}
			serializedUTXOEntry, err := serializeUTXOEntry(utxoEntryToAdd)
			if err != nil {
				return err
			}
			err = dbTransaction.Put(key, serializedUTXOEntry)
			if err != nil {
				return err
			}
			toAddSompiSupply = toAddSompiSupply + utxoEntryToAdd.Amount()
		}
	}

	serializeParentHashes := serializeHashes(uis.virtualParents)
	err = dbTransaction.Put(virtualParentsKey, serializeParentHashes)
	if err != nil {
		return err
	}

	err = uis.updateCirculatingSompiSupply(dbTransaction, toAddSompiSupply, toRemoveSompiSupply)
	if err != nil {
		return err
	}

	err = dbTransaction.Commit()
	if err != nil {
		return err
	}

	uis.discard()
	return nil
}

func (uis *utxoIndexStore) addAndCommitOutpointsWithoutTransaction(utxoPairs []*externalapi.OutpointAndUTXOEntryPair) error {
	var (
		wg         sync.WaitGroup
		errChan    = make(chan error, len(utxoPairs))
		amountChan = make(chan uint64, len(utxoPairs))
	)

	for _, pair := range utxoPairs {
		wg.Add(1)
		go func(pair *externalapi.OutpointAndUTXOEntryPair) {
			defer wg.Done()

			bucket := uis.bucketForScriptPublicKey(pair.UTXOEntry.ScriptPublicKey())
			key, err := uis.convertOutpointToKey(bucket, pair.Outpoint)
			if err != nil {
				errChan <- err
				return
			}

			serializedUTXOEntry, err := serializeUTXOEntry(pair.UTXOEntry)
			if err != nil {
				errChan <- err
				return
			}

			err = uis.database.Put(key, serializedUTXOEntry)
			if err != nil {
				errChan <- err
				return
			}

			amountChan <- pair.UTXOEntry.Amount()
		}(pair)
	}

	// Wait for all goroutines to finish
	wg.Wait()
	close(errChan)
	close(amountChan)

	// Check for any errors
	for err := range errChan {
		if err != nil {
			return err
		}
	}

	// Sum all amounts
	toAddSompiSupply := uint64(0)
	for amount := range amountChan {
		toAddSompiSupply += amount
	}

	// Final update
	return uis.updateCirculatingSompiSupplyWithoutTransaction(toAddSompiSupply, 0)
}

func (uis *utxoIndexStore) updateAndCommitVirtualParentsWithoutTransaction(virtualParents []*externalapi.DomainHash) error {
	serializeParentHashes := serializeHashes(virtualParents)
	return uis.database.Put(virtualParentsKey, serializeParentHashes)
}

func (uis *utxoIndexStore) bucketForScriptPublicKey(scriptPublicKey *externalapi.ScriptPublicKey) *database.Bucket {
	var scriptPublicKeyBytes = make([]byte, 2+len(scriptPublicKey.Script)) // uint16
	binary.LittleEndian.PutUint16(scriptPublicKeyBytes[:2], scriptPublicKey.Version)
	copy(scriptPublicKeyBytes[2:], scriptPublicKey.Script)
	return utxoIndexBucket.Bucket(scriptPublicKeyBytes)
}

func (uis *utxoIndexStore) convertOutpointToKey(bucket *database.Bucket, outpoint *externalapi.DomainOutpoint) (*database.Key, error) {
	serializedOutpoint, err := serializeOutpoint(outpoint)
	if err != nil {
		return nil, err
	}
	return bucket.Key(serializedOutpoint), nil
}

func (uis *utxoIndexStore) convertKeyToOutpoint(key *database.Key) (*externalapi.DomainOutpoint, error) {
	serializedOutpoint := key.Suffix()
	return deserializeOutpoint(serializedOutpoint)
}

func (uis *utxoIndexStore) stagedData() (
	toAdd map[ScriptPublicKeyString]UTXOOutpointEntryPairs,
	toRemove map[ScriptPublicKeyString]UTXOOutpointEntryPairs,
	virtualParents []*externalapi.DomainHash) {

	toAddClone := make(map[ScriptPublicKeyString]UTXOOutpointEntryPairs, len(uis.toAdd))
	for scriptPublicKeyString, toAddUTXOOutpointEntryPairs := range uis.toAdd {
		toAddUTXOOutpointEntryPairsClone := make(UTXOOutpointEntryPairs, len(toAddUTXOOutpointEntryPairs))
		maps.Copy(toAddUTXOOutpointEntryPairsClone, toAddUTXOOutpointEntryPairs)
		toAddClone[scriptPublicKeyString] = toAddUTXOOutpointEntryPairsClone
	}

	toRemoveClone := make(map[ScriptPublicKeyString]UTXOOutpointEntryPairs, len(uis.toRemove))
	for scriptPublicKeyString, toRemoveUTXOOutpointEntryPairs := range uis.toRemove {
		toRemoveUTXOOutpointEntryPairsClone := make(UTXOOutpointEntryPairs, len(toRemoveUTXOOutpointEntryPairs))
		maps.Copy(toRemoveUTXOOutpointEntryPairsClone, toRemoveUTXOOutpointEntryPairs)
		toRemoveClone[scriptPublicKeyString] = toRemoveUTXOOutpointEntryPairsClone
	}

	return toAddClone, toRemoveClone, uis.virtualParents
}

func (uis *utxoIndexStore) isAnythingStaged() bool {
	return len(uis.toAdd) > 0 || len(uis.toRemove) > 0
}

func (uis *utxoIndexStore) getUTXOOutpointEntryPairs(scriptPublicKey *externalapi.ScriptPublicKey) (UTXOOutpointEntryPairs, error) {
	pairs, err := uis.UTXOs(scriptPublicKey)
	if err != nil {
		return nil, err
	}
	result := make(UTXOOutpointEntryPairs)
	for _, pair := range pairs {
		result[pair.Outpoint] = pair.Entry
	}
	return result, nil
}

// UTXOPair is a struct for streaming UTXO results efficiently
type UTXOPair struct {
	Outpoint externalapi.DomainOutpoint
	Entry    externalapi.UTXOEntry
}

// UTXOs streams UTXOs for a ScriptPublicKey directly into a slice (allocation-efficient)
func (uis *utxoIndexStore) UTXOs(scriptPublicKey *externalapi.ScriptPublicKey) ([]UTXOPair, error) {
	if uis.isAnythingStaged() {
		return nil, errors.Errorf("cannot get UTXOs while staging isn't empty")
	}
	bucket := uis.bucketForScriptPublicKey(scriptPublicKey)
	cursor, err := uis.database.Cursor(bucket)
	if err != nil {
		return nil, err
	}
	defer cursor.Close()
	var pairs []UTXOPair
	for cursor.Next() {
		key, err := cursor.Key()
		if err != nil {
			return nil, err
		}
		outpoint, err := uis.convertKeyToOutpoint(key)
		if err != nil {
			return nil, err
		}
		serializedUTXOEntry, err := cursor.Value()
		if err != nil {
			return nil, err
		}
		utxoEntry, err := deserializeUTXOEntry(serializedUTXOEntry)
		if err != nil {
			return nil, err
		}
		pairs = append(pairs, UTXOPair{Outpoint: *outpoint, Entry: utxoEntry})
	}
	return pairs, nil
}

// ForEachUTXO provides an iterator-style API for advanced streaming
func (uis *utxoIndexStore) ForEachUTXO(scriptPublicKey *externalapi.ScriptPublicKey, fn func(outpoint externalapi.DomainOutpoint, entry externalapi.UTXOEntry) error) error {
	if uis.isAnythingStaged() {
		return errors.Errorf("cannot iterate UTXOs while staging isn't empty")
	}
	bucket := uis.bucketForScriptPublicKey(scriptPublicKey)
	cursor, err := uis.database.Cursor(bucket)
	if err != nil {
		return err
	}
	defer cursor.Close()
	for cursor.Next() {
		key, err := cursor.Key()
		if err != nil {
			return err
		}
		outpoint, err := uis.convertKeyToOutpoint(key)
		if err != nil {
			return err
		}
		serializedUTXOEntry, err := cursor.Value()
		if err != nil {
			return err
		}
		utxoEntry, err := deserializeUTXOEntry(serializedUTXOEntry)
		if err != nil {
			return err
		}
		if err := fn(*outpoint, utxoEntry); err != nil {
			return err
		}
	}
	return nil
}

func (uis *utxoIndexStore) getVirtualParents() ([]*externalapi.DomainHash, error) {
	if uis.isAnythingStaged() {
		return nil, errors.Errorf("cannot get the virtual parents while staging isn't empty")
	}

	serializedHashes, err := uis.database.Get(virtualParentsKey)
	// if database.IsNotFoundError(err) {
	// 	log.Infof("getVirtualParents failed to retrieve with %s\n", virtualParentsKey)
	// 	return nil, err
	// }
	if err != nil {
		return nil, err
	}

	return deserializeHashes(serializedHashes)
}

func (uis *utxoIndexStore) deleteAll() error {
	// First we delete the virtual parents, so if anything goes wrong, the UTXO index will be marked as "not synced"
	// and will be reset.
	err := uis.database.Delete(virtualParentsKey)
	if err != nil {
		return err
	}

	err = uis.database.Delete(circulatingSupplyKey)
	if err != nil {
		return err
	}

	cursor, err := uis.database.Cursor(utxoIndexBucket)
	if err != nil {
		return err
	}
	defer cursor.Close()
	for cursor.Next() {
		key, err := cursor.Key()
		if err != nil {
			return err
		}

		err = uis.database.Delete(key)
		if err != nil {
			return err
		}
	}

	return nil
}

func (uis *utxoIndexStore) initializeCirculatingSompiSupply() error {

	cursor, err := uis.database.Cursor(utxoIndexBucket)
	if err != nil {
		return err
	}
	defer cursor.Close()

	circulatingSompiSupplyInDatabase := uint64(0)
	for cursor.Next() {
		serializedUTXOEntry, err := cursor.Value()
		if err != nil {
			return err
		}
		utxoEntry, err := deserializeUTXOEntry(serializedUTXOEntry)
		if err != nil {
			return err
		}

		circulatingSompiSupplyInDatabase = circulatingSompiSupplyInDatabase + utxoEntry.Amount()
	}

	err = uis.database.Put(
		circulatingSupplyKey,
		binaryserialization.SerializeUint64(circulatingSompiSupplyInDatabase),
	)

	if err != nil {
		return err
	}

	return nil
}

func (uis *utxoIndexStore) updateCirculatingSompiSupply(dbTransaction database.Transaction, toAddSompiSupply uint64, toRemoveSompiSupply uint64) error {
	if toAddSompiSupply != toRemoveSompiSupply {
		circulatingSupplyBytes, err := dbTransaction.Get(circulatingSupplyKey)
		if database.IsNotFoundError(err) {
			log.Infof("updateCirculatingSompiSupply failed to retrieve with %s\n", circulatingSupplyKey)
			return err
		}
		if err != nil {
			return err
		}

		circulatingSupply, err := binaryserialization.DeserializeUint64(circulatingSupplyBytes)
		if err != nil {
			return err
		}
		err = dbTransaction.Put(
			circulatingSupplyKey,
			binaryserialization.SerializeUint64(circulatingSupply+toAddSompiSupply-toRemoveSompiSupply),
		)
		if err != nil {
			return err
		}
	}
	return nil
}

func (uis *utxoIndexStore) updateCirculatingSompiSupplyWithoutTransaction(toAddSompiSupply uint64, toRemoveSompiSupply uint64) error {
	if toAddSompiSupply != toRemoveSompiSupply {
		circulatingSupplyBytes, err := uis.database.Get(circulatingSupplyKey)
		if database.IsNotFoundError(err) {
			log.Infof("updateCirculatingSompiSupplyWithoutTransaction failed to retrieve with %s\n", circulatingSupplyKey)
			return err
		}
		if err != nil {
			return err
		}

		circulatingSupply, err := binaryserialization.DeserializeUint64(circulatingSupplyBytes)
		if err != nil {
			return err
		}
		err = uis.database.Put(
			circulatingSupplyKey,
			binaryserialization.SerializeUint64(circulatingSupply+toAddSompiSupply-toRemoveSompiSupply),
		)
		if err != nil {
			return err
		}
	}
	return nil
}

func (uis *utxoIndexStore) getCirculatingSompiSupply() (uint64, error) {
	if uis.isAnythingStaged() {
		return 0, errors.Errorf("cannot get circulatingSupply while staging isn't empty")
	}
	circulatingSupply, err := uis.database.Get(circulatingSupplyKey)
	if database.IsNotFoundError(err) {
		log.Infof("getCirculatingSompiSupply failed to retrieve with %s\n", circulatingSupplyKey)
		return 0, err
	}
	if err != nil {
		return 0, err
	}
	return binaryserialization.DeserializeUint64(circulatingSupply)
}
