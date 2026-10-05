package miningmanager

import (
	"sync"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/transactionhelper"
	mempoolpkg "github.com/HoosatNetwork/HTND/v2/domain/miningmanager/mempool"
	"github.com/pkg/errors"
)

// maxQueuedDisqualifiedBlocks bounds the blocks waiting for RestoreTransactionsOfDisqualifiedBlocks. A
// node that disqualifies a whole lineage during IBD reports every block of it; past this many the oldest
// are the least likely to hold anything still restorable, so new ones are dropped instead.
const maxQueuedDisqualifiedBlocks = 10_000

// disqualifiedBlockQueue holds the blocks consensus disqualified since the last restore. It has a lock
// of its own because NoteDisqualifiedBlock runs under the consensus lock, and the mempool lock is taken
// before the consensus lock everywhere else.
type disqualifiedBlockQueue struct {
	lock    sync.Mutex
	hashes  []*externalapi.DomainHash
	queued  map[externalapi.DomainHash]struct{}
	dropped int
}

func newDisqualifiedBlockQueue() *disqualifiedBlockQueue {
	return &disqualifiedBlockQueue{queued: make(map[externalapi.DomainHash]struct{})}
}

func (q *disqualifiedBlockQueue) add(blockHash *externalapi.DomainHash) {
	q.lock.Lock()
	defer q.lock.Unlock()

	if _, ok := q.queued[*blockHash]; ok {
		return
	}
	if len(q.hashes) >= maxQueuedDisqualifiedBlocks {
		q.dropped++
		return
	}
	q.queued[*blockHash] = struct{}{}
	q.hashes = append(q.hashes, blockHash)
}

func (q *disqualifiedBlockQueue) take() (hashes []*externalapi.DomainHash, dropped int) {
	q.lock.Lock()
	defer q.lock.Unlock()

	hashes, dropped = q.hashes, q.dropped
	q.hashes = nil
	q.queued = make(map[externalapi.DomainHash]struct{})
	q.dropped = 0
	return hashes, dropped
}

// NoteDisqualifiedBlock records that consensus disqualified blockHash from the chain. It is meant for
// consensus.Config.OnDisqualification and so runs under the consensus lock: it only queues the hash.
func (mm *miningManager) NoteDisqualifiedBlock(blockHash *externalapi.DomainHash) {
	mm.disqualifiedBlocks.add(blockHash)
}

// RestoreTransactionsOfDisqualifiedBlocks puts the transactions of every block disqualified since the
// last call back into the mempool, and returns the ones it accepted.
//
// HandleNewBlockTransactions removes a block's transactions from the mempool when the block arrives,
// before anyone knows whether the block will ever be merged. A block disqualified from the chain is never
// a tip again, so nothing this node builds merges it, and a transaction it carried is then in no block
// that counts and in no mempool. Its inputs stay unspent and its outputs never appear, and nothing resends
// it. The caller skips HandleNewBlockTransactions for a block that is already disqualified when it
// arrives; this covers a block that was pending verification then and was disqualified afterwards.
//
// Each transaction is validated against the current virtual like any relayed one, so one that some other
// block already got accepted is rejected as spending spent coins and simply not restored.
func (mm *miningManager) RestoreTransactionsOfDisqualifiedBlocks() ([]*externalapi.DomainTransaction, error) {
	blockHashes, dropped := mm.disqualifiedBlocks.take()
	if dropped > 0 {
		log.Warnf("%d disqualified blocks were not queued for restoring their transactions to the mempool: "+
			"more than %d were waiting", dropped, maxQueuedDisqualifiedBlocks)
	}

	var restored []*externalapi.DomainTransaction
	for _, blockHash := range blockHashes {
		consensus := mm.consensusReference.Consensus()
		blockInfo, err := consensus.GetBlockInfo(blockHash)
		if err != nil {
			return nil, err
		}
		// OnDisqualification fires before the status is committed, and a staging consensus reports into
		// the same queue, so check what the consensus being served actually holds.
		if !blockInfo.Exists || blockInfo.BlockStatus != externalapi.StatusDisqualifiedFromChain {
			continue
		}
		block, found, err := consensus.GetBlock(blockHash)
		if err != nil {
			return nil, err
		}
		if !found {
			continue
		}

		for i, transaction := range block.Transactions {
			if i == transactionhelper.CoinbaseTransactionIndex {
				continue
			}
			accepted, err := mm.mempool.ValidateAndInsertTransaction(transaction.Clone(), false, false, false)
			if err != nil {
				if errors.As(err, &mempoolpkg.RuleError{}) {
					log.Debugf("Not restoring transaction %s of disqualified block %s to the mempool: %s",
						consensushashing.TransactionID(transaction), blockHash, err)
					continue
				}
				return nil, err
			}
			restored = append(restored, accepted...)
		}
	}
	if len(restored) > 0 {
		log.Infof("Restored %d transactions of %d disqualified blocks to the mempool", len(restored), len(blockHashes))
	}
	return restored, nil
}
