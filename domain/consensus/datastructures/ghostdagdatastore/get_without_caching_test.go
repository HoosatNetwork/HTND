package ghostdagdatastore

import (
	"errors"
	"math/big"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/database"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/datastructures/testutils"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
)

// TestGetWithoutCachingReadsTheDatabaseAlone pins that the GHOSTDAG read used without the consensus
// lock neither hits nor fills the cache, which is not safe for concurrent use, that it keeps trusted
// and untrusted data apart, and that it returns the last committed data for a key that is rewritten,
// as virtual's is.
func TestGetWithoutCachingReadsTheDatabaseAlone(t *testing.T) {
	dbManager, prefixBucket, teardown := testutils.NewTestDB(t)
	defer teardown()

	store := New(prefixBucket, 10, false).(*ghostdagDataStore)
	blockHash := testutils.Hash(1)
	data := func(blueScore uint64) *externalapi.BlockGHOSTDAGData {
		return externalapi.NewBlockGHOSTDAGData(blueScore, big.NewInt(123), testutils.Hash(2),
			[]*externalapi.DomainHash{testutils.Hash(3)}, []*externalapi.DomainHash{},
			map[externalapi.DomainHash]externalapi.KType{*testutils.Hash(3): 1}, externalapi.KType(1))
	}

	stagingArea := model.NewStagingArea()
	store.Stage(stagingArea, blockHash, data(7), false)
	testutils.Commit(t, dbManager, stagingArea)
	store.cache.Clear()

	got, err := store.GetWithoutCaching(dbManager, blockHash, false)
	if err != nil {
		t.Fatalf("GetWithoutCaching: %v", err)
	}
	if got.BlueScore() != 7 || !got.SelectedParent().Equal(testutils.Hash(2)) {
		t.Errorf("GetWithoutCaching returned blue score %d, selected parent %s; want 7, %s",
			got.BlueScore(), got.SelectedParent(), testutils.Hash(2))
	}
	if store.cache.Len() != 0 {
		t.Errorf("GetWithoutCaching left %d entries in the cache, want 0", store.cache.Len())
	}
	if _, err := store.GetWithoutCaching(dbManager, blockHash, true); !errors.Is(err, database.ErrNotFound) {
		t.Errorf("GetWithoutCaching of absent trusted data returned %v, want ErrNotFound", err)
	}

	// A rewrite, as virtual moving: the read returns the newly committed data, not what an earlier
	// read saw.
	stagingArea = model.NewStagingArea()
	store.Stage(stagingArea, blockHash, data(8), false)
	testutils.Commit(t, dbManager, stagingArea)
	got, err = store.GetWithoutCaching(dbManager, blockHash, false)
	if err != nil {
		t.Fatalf("GetWithoutCaching after a rewrite: %v", err)
	}
	if got.BlueScore() != 8 {
		t.Errorf("GetWithoutCaching after a rewrite returned blue score %d, want 8", got.BlueScore())
	}
}
