package serialization

import (
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/utxo"
	"github.com/Hoosat-Oy/HTND/util/memory"
)

// UTXODiffToDBUTXODiff converts UTXODiff to DbUtxoDiff
func UTXODiffToDBUTXODiff(diff externalapi.UTXODiff, toAddBuffer *memory.Block[*DbUtxoCollectionItem], toRemoveBuffer *memory.Block[*DbUtxoCollectionItem]) (*DbUtxoDiff, error) {

	toAdd, err := utxoCollectionToDBUTXOCollection(diff.ToAdd(), toAddBuffer)
	if err != nil {
		return nil, err
	}

	toRemove, err := utxoCollectionToDBUTXOCollection(diff.ToRemove(), toRemoveBuffer)
	if err != nil {
		return nil, err
	}

	return &DbUtxoDiff{
		ToAdd:    toAdd,
		ToRemove: toRemove,
	}, nil
}

// DBUTXODiffToUTXODiff converts DbUtxoDiff to UTXODiff
func DBUTXODiffToUTXODiff(diff *DbUtxoDiff) (externalapi.UTXODiff, error) {
	toAdd, err := dbUTXOCollectionToUTXOCollection(diff.ToAdd)
	if err != nil {
		return nil, err
	}

	toRemove, err := dbUTXOCollectionToUTXOCollection(diff.ToRemove)
	if err != nil {
		return nil, err
	}

	return utxo.NewUTXODiffFromCollections(toAdd, toRemove)
}
