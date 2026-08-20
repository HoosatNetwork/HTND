package consensusstatemanager

import (
	"math"

	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/multiset"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/utxo"
	"github.com/pkg/errors"
)

func (csm *consensusStateManager) calculateMultiset(stagingArea *model.StagingArea,
	blockHash *externalapi.DomainHash,
	acceptanceData externalapi.AcceptanceData,
	blockGHOSTDAGData *externalapi.BlockGHOSTDAGData,
	daaScore uint64,
) (model.Multiset, error) {
	log.Tracef("calculateMultiset start for block with selected parent %s", blockGHOSTDAGData.SelectedParent())
	defer log.Tracef("calculateMultiset end for block with selected parent %s", blockGHOSTDAGData.SelectedParent())

	// Case 1: we are calculating the multiset of the genesis / virtual-genesis itself
	if blockHash.Equal(csm.genesisHash) || blockHash.Equal(model.VirtualGenesisBlockHash) {
		log.Debugf("Selected parent is nil, which could only happen for the genesis. " +
			"The genesis has a predefined multiset")
		return csm.multisetStore.Get(csm.databaseContext, stagingArea, csm.genesisHash)
	}

	// Case 2: normal path – but the selected parent may be the virtual-genesis marker
	selectedParent := blockGHOSTDAGData.SelectedParent()
	if selectedParent.Equal(model.VirtualGenesisBlockHash) || selectedParent.Equal(csm.genesisHash) {
		// VirtualGenesis is only a marker and never has its own multiset.
		// Fall back to the real genesis multiset (same idea as restorePastUTXO).
		csm.multisetStore.Stage(stagingArea, csm.genesisHash, multiset.New())
		selectedParent = csm.genesisHash
	}

	ms, err := csm.multisetStore.Get(csm.databaseContext, stagingArea, selectedParent)
	if err != nil {
		return nil, err
	}
	log.Debugf("The multiset for the selected parent %s is: %s", selectedParent, ms.Hash())

	for _, blockAcceptanceData := range acceptanceData {
		for i, transactionAcceptanceData := range blockAcceptanceData.TransactionAcceptanceData {
			transaction := transactionAcceptanceData.Transaction
			transactionID := consensushashing.TransactionID(transaction)
			if !transactionAcceptanceData.IsAccepted {
				log.Tracef("Skipping transaction %s because it was not accepted", transactionID)
				continue
			}

			isCoinbase := i == 0
			log.Tracef("Is transaction %s a coinbase transaction: %t", transactionID, isCoinbase)

			err := addTransactionToMultiset(ms, transaction, daaScore, isCoinbase)
			if err != nil {
				return nil, err
			}
			log.Tracef("Added transaction %s to the multiset", transactionID)
		}
	}

	return ms, nil
}

func addTransactionToMultiset(multiset model.Multiset, transaction *externalapi.DomainTransaction,
	blockDAAScore uint64, isCoinbase bool,
) error {
	transactionID := consensushashing.TransactionID(transaction)
	log.Tracef("addTransactionToMultiset start for transaction %s", transactionID)
	defer log.Tracef("addTransactionToMultiset end for transaction %s", transactionID)

	for _, input := range transaction.Inputs {
		log.Tracef("Removing input %s at index %d from the multiset",
			input.PreviousOutpoint.TransactionID, input.PreviousOutpoint.Index)
		err := removeUTXOFromMultiset(multiset, input.UTXOEntry, &input.PreviousOutpoint)
		if err != nil {
			return err
		}
	}

	for i, output := range transaction.Outputs {
		if i < 0 || i > math.MaxUint32 {
			return errors.Errorf("output index %d cannot be represented as uint32", i)
		}
		outpoint := &externalapi.DomainOutpoint{
			TransactionID: *transactionID,
			Index:         uint32(i),
		}
		utxoEntry := utxo.NewUTXOEntry(output.Value, output.ScriptPublicKey, isCoinbase, blockDAAScore)

		log.Tracef("Adding input %s at index %d from the multiset", transactionID, i)
		err := addUTXOToMultiset(multiset, utxoEntry, outpoint)
		if err != nil {
			return err
		}
	}

	return nil
}

func addUTXOToMultiset(multiset model.Multiset, entry externalapi.UTXOEntry,
	outpoint *externalapi.DomainOutpoint,
) error {
	serializedUTXO, err := utxo.SerializeUTXO(entry, outpoint)
	if err != nil {
		return err
	}
	multiset.Add(serializedUTXO)

	return nil
}

func removeUTXOFromMultiset(multiset model.Multiset, entry externalapi.UTXOEntry,
	outpoint *externalapi.DomainOutpoint,
) error {
	serializedUTXO, err := utxo.SerializeUTXO(entry, outpoint)
	if err != nil {
		return err
	}
	multiset.Remove(serializedUTXO)

	return nil
}
