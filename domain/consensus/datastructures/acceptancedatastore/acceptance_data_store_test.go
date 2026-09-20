package acceptancedatastore

import (
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus/database"
	"github.com/HoosatNetwork/HTND/domain/consensus/datastructures/testutils"
	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
)

func TestAcceptanceDataStoreRoundTripAndDelete(t *testing.T) {
	dbManager, prefixBucket, teardown := testutils.NewTestDB(t)
	defer teardown()

	store := New(prefixBucket, 10, false)

	blockHash := testutils.Hash(1)
	relatedBlockHash := testutils.Hash(2)

	tx := &externalapi.DomainTransaction{
		Version:      0,
		Inputs:       []*externalapi.DomainTransactionInput{},
		Outputs:      []*externalapi.DomainTransactionOutput{},
		LockTime:     0,
		SubnetworkID: externalapi.DomainSubnetworkID{},
		Gas:          0,
		Payload:      []byte{},
	}

	ad := externalapi.AcceptanceData{
		&externalapi.BlockAcceptanceData{
			BlockHash: relatedBlockHash,
			TransactionAcceptanceData: []*externalapi.TransactionAcceptanceData{
				{
					Transaction:                 tx,
					Fee:                         1,
					IsAccepted:                  true,
					TransactionInputUTXOEntries: []externalapi.UTXOEntry{},
				},
			},
		},
	}

	stagingArea := model.NewStagingArea()
	store.Stage(stagingArea, blockHash, ad)
	testutils.Commit(t, dbManager, stagingArea)

	stagingArea = model.NewStagingArea()
	got, err := store.Get(dbManager, stagingArea, blockHash)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.Equal(ad) {
		t.Fatalf("unexpected acceptance data")
	}

	// Delete
	stagingArea = model.NewStagingArea()
	store.Delete(stagingArea, blockHash)
	testutils.Commit(t, dbManager, stagingArea)

	stagingArea = model.NewStagingArea()
	_, err = store.Get(dbManager, stagingArea, blockHash)
	if err == nil || !database.IsNotFoundError(err) {
		t.Fatalf("expected not-found after delete, got %v", err)
	}
}
