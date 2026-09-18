package staging

import (
	"sync/atomic"

	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/infrastructure/logger"
)

// CommitAllChanges creates a transaction in `databaseContext`, and commits all changes in `stagingArea` through it.
//
// The transaction is unindexed: a StagingShard's Commit only writes through it, so none of them
// needs to read a key back before the commit lands. The one shard that reads at all,
// headersSelectedChainStore, reads its highest-index key before it writes anything, which an
// unindexed transaction still answers from the database. Indexing every write instead costs work
// per Put that grows with the number of keys in the transaction, which is what a reachability
// reindex makes expensive - it rewrites the whole subtree it propagates over in one commit.
func CommitAllChanges(databaseContext model.DBManager, stagingArea *model.StagingArea) error {
	onEnd := logger.LogAndMeasureExecutionTime(utilLog, "commitAllChanges")
	defer onEnd()

	dbTx, err := databaseContext.BeginUnindexed()
	if err != nil {
		return err
	}

	err = stagingArea.Commit(dbTx)
	if err != nil {
		return err
	}

	return dbTx.Commit()
}

var lastShardingID atomic.Uint64

// GenerateShardingID generates a unique staging sharding ID.
func GenerateShardingID() model.StagingShardID {
	return model.StagingShardID(lastShardingID.Add(1))
}
