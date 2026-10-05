package pruningmanager

import (
	"math"
	"slices"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/utxo"
	"github.com/pkg/errors"
)

// utxoLookup answers whether a UTXO set holds an outpoint, and with which entry.
type utxoLookup func(outpoint *externalapi.DomainOutpoint) (externalapi.UTXOEntry, bool, error)

// addAcceptedOutpoints adds to created every outpoint an accepted transaction in acceptanceData creates.
func addAcceptedOutpoints(created map[externalapi.DomainOutpoint]struct{}, acceptanceData externalapi.AcceptanceData) error {
	for _, blockAcceptanceData := range acceptanceData {
		for _, transactionAcceptanceData := range blockAcceptanceData.TransactionAcceptanceData {
			if !transactionAcceptanceData.IsAccepted {
				continue
			}
			transaction := transactionAcceptanceData.Transaction
			transactionID := consensushashing.TransactionID(transaction)
			for i := range transaction.Outputs {
				if i > math.MaxUint32 {
					return errors.Errorf("output index %d cannot be represented as uint32", i)
				}
				created[externalapi.DomainOutpoint{TransactionID: *transactionID, Index: uint32(i)}] = struct{}{}
			}
		}
	}
	return nil
}

// reconcileReplayWithPreviousSet turns a diff built by replaying acceptance data from an empty diff into the diff
// from the previous pruning point's UTXO set, which is what calculateDiffBetweenPreviousAndCurrentPruningPoints
// derives from the stored UTXO diffs.
//
// A replay that starts from nothing cannot see the coins the previous set already holds. That matters only for an
// outpoint the replay creates while the previous set holds it - a byte-identical coinbase accepted again after the
// previous pruning point, which restamps the coin with the new merging block's DAA score:
//
//   - still held at the current pruning point: the replay has toAdd(new) and no removal of the old entry. The bucket
//     happens to end up right, because the put overwrites, but the old entry never leaves the multiset.
//   - spent later in the range: the spend cancels the replay's toAdd(new), so the replay records nothing, and the
//     spent coin stays in the served set.
//
// Both need toRemove(previous entry), which is what the diff-chain walk produces. An outpoint the replay already
// removed was spent from the previous set before it was created again, so the replay has it right. previous is only
// asked about outpoints in created.
func reconcileReplayWithPreviousSet(replay externalapi.UTXODiff, created map[externalapi.DomainOutpoint]struct{},
	previous utxoLookup,
) (externalapi.UTXODiff, error) {
	toAdd, err := collectionToMap(replay.ToAdd())
	if err != nil {
		return nil, err
	}
	toRemove, err := collectionToMap(replay.ToRemove())
	if err != nil {
		return nil, err
	}

	for outpoint := range created {
		previousEntry, held, err := previous(&outpoint)
		if err != nil {
			return nil, err
		}
		if !held {
			continue
		}
		if _, removed := toRemove[outpoint]; removed {
			continue
		}
		if added, ok := toAdd[outpoint]; ok && added.Equal(previousEntry) {
			// Created again exactly as it was: no change at all.
			delete(toAdd, outpoint)
			continue
		}
		toRemove[outpoint] = previousEntry
	}

	return utxo.NewUTXODiffFromCollections(utxo.NewUTXOCollection(toAdd), utxo.NewUTXOCollection(toRemove))
}

func collectionToMap(collection externalapi.UTXOCollection) (map[externalapi.DomainOutpoint]externalapi.UTXOEntry, error) {
	result := make(map[externalapi.DomainOutpoint]externalapi.UTXOEntry, collection.Len())
	iterator := collection.Iterator()
	defer iterator.Close()
	for ok := iterator.First(); ok; ok = iterator.Next() {
		outpoint, entry, err := iterator.Get()
		if err != nil {
			return nil, err
		}
		result[*outpoint] = entry
	}
	return result, nil
}

// pastUTXOLookup looks up blockHash's past UTXO set the way consensus restores it (restorePastUTXO): the block's
// UTXO diffs are walked along UTXODiffChild to virtual and accumulated, and anything they do not mention is looked
// up in virtual's UTXO set. This is the same view of the block the diff-chain walk diffs against.
func (pm *pruningManager) pastUTXOLookup(stagingArea *model.StagingArea, blockHash *externalapi.DomainHash) (utxoLookup, error) {
	var diffs []externalapi.UTXODiff
	for current := blockHash; ; {
		diff, err := pm.utxoDiffStore.UTXODiff(pm.databaseContext, stagingArea, current)
		if err != nil {
			return nil, err
		}
		diffs = append(diffs, diff)
		hasChild, err := pm.utxoDiffStore.HasUTXODiffChild(pm.databaseContext, stagingArea, current)
		if err != nil {
			return nil, err
		}
		if !hasChild {
			break
		}
		current, err = pm.utxoDiffStore.UTXODiffChild(pm.databaseContext, stagingArea, current)
		if err != nil {
			return nil, err
		}
	}
	accumulated := utxo.NewMutableUTXODiff()
	for _, diff := range slices.Backward(diffs) {
		err := accumulated.WithDiffInPlace(diff)
		if err != nil {
			return nil, err
		}
	}
	fromVirtual := accumulated.ToImmutable()

	return func(outpoint *externalapi.DomainOutpoint) (externalapi.UTXOEntry, bool, error) {
		// toAdd first: a restamped coin is in both, and the set holds the toAdd entry.
		if entry, ok := fromVirtual.ToAdd().Get(outpoint); ok {
			return entry, true, nil
		}
		if fromVirtual.ToRemove().Contains(outpoint) {
			return nil, false, nil
		}
		return pm.consensusStateStore.LookupUTXOByOutpointWithoutPopulatingCache(pm.databaseContext, stagingArea, outpoint)
	}, nil
}
