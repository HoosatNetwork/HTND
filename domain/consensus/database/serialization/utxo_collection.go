package serialization

import (
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/utxo"
	"github.com/Hoosat-Oy/HTND/util/memory"
)

func utxoCollectionToDBUTXOCollection(utxoCollection externalapi.UTXOCollection, buffer *memory.Block[*DbUtxoCollectionItem]) ([]*DbUtxoCollectionItem, error) {
	count := utxoCollection.Len()
	if buffer == nil || cap(buffer.Slice()) < count {
		buffer = memory.Realloc(buffer, count)
	}
	items := buffer.Slice()[:0]
	utxoIterator := utxoCollection.Iterator()
	defer utxoIterator.Close()
	for ok := utxoIterator.First(); ok; ok = utxoIterator.Next() {
		outpoint, entry, err := utxoIterator.Get()
		if err != nil {
			return nil, err
		}

		items = append(items, &DbUtxoCollectionItem{
			Outpoint:  DomainOutpointToDbOutpoint(outpoint),
			UtxoEntry: UTXOEntryToDBUTXOEntry(entry),
		})
	}

	return items, nil
}

func dbUTXOCollectionToUTXOCollection(items []*DbUtxoCollectionItem) (externalapi.UTXOCollection, error) {
	utxoMap := make(map[externalapi.DomainOutpoint]externalapi.UTXOEntry, len(items))
	for _, item := range items {
		outpoint, err := DbOutpointToDomainOutpoint(item.Outpoint)
		if err != nil {
			return nil, err
		}
		utxoEntry, err := DBUTXOEntryToUTXOEntry(item.UtxoEntry)
		if err != nil {
			return nil, err
		}
		utxoMap[*outpoint] = utxoEntry
	}
	return utxo.NewUTXOCollection(utxoMap), nil
}
