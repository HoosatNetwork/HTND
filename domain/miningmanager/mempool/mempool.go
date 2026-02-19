package mempool

import (
	"sync"

	"github.com/Hoosat-Oy/HTND/domain/consensus/ruleerrors"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/consensushashing"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/constants"
	"github.com/pkg/errors"

	"github.com/Hoosat-Oy/HTND/domain/consensusreference"

	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	miningmanagermodel "github.com/Hoosat-Oy/HTND/domain/miningmanager/model"
)

type mempool struct {
	mtx sync.RWMutex

	config             *Config
	consensusReference consensusreference.ConsensusReference

	mempoolUTXOSet        *mempoolUTXOSet
	transactionsPool      *transactionsPool
	orphansPool           *orphansPool
	compoundTxRateLimiter *compoundTxRateLimiter
	walletFreezingManager *walletFreezingManager
}

// New constructs a new mempool
func New(config *Config, consensusReference consensusreference.ConsensusReference) miningmanagermodel.Mempool {
	mp := &mempool{
		config:             config,
		consensusReference: consensusReference,
	}

	mp.mempoolUTXOSet = newMempoolUTXOSet(mp)
	mp.transactionsPool = newTransactionsPool(mp)
	mp.orphansPool = newOrphansPool(mp)
	mp.compoundTxRateLimiter = newCompoundTxRateLimiter(config)
	mp.walletFreezingManager = newWalletFreezingManager(config)

	return mp
}

func (mp *mempool) ValidateAndInsertTransaction(transaction *externalapi.DomainTransaction, isHighPriority bool, allowOrphan bool) (
	acceptedTransactions []*externalapi.DomainTransaction, err error) {

	mp.mtx.Lock()
	defer mp.mtx.Unlock()

	return mp.validateAndInsertTransaction(transaction, isHighPriority, allowOrphan)
}

func (mp *mempool) GetTransaction(transactionID *externalapi.DomainTransactionID,
	includeTransactionPool bool,
	includeOrphanPool bool) (
	transaction *externalapi.DomainTransaction,
	isOrphan bool,
	found bool) {

	mp.mtx.RLock()
	defer mp.mtx.RUnlock()

	var transactionfound bool
	isOrphan = false

	if includeTransactionPool {
		transaction, transactionfound = mp.transactionsPool.getTransaction(transactionID, true)
		isOrphan = false
	}
	if !transactionfound && includeOrphanPool {
		transaction, transactionfound = mp.orphansPool.getOrphanTransaction(transactionID)
		isOrphan = true
	}

	return transaction, isOrphan, transactionfound
}

func (mp *mempool) GetTransactionsByAddresses(includeTransactionPool bool, includeOrphanPool bool) (
	sendingInTransactionPool map[string]*externalapi.DomainTransaction,
	receivingInTransactionPool map[string]*externalapi.DomainTransaction,
	sendingInOrphanPool map[string]*externalapi.DomainTransaction,
	receivingInOrphanPool map[string]*externalapi.DomainTransaction,
	err error) {
	mp.mtx.RLock()
	defer mp.mtx.RUnlock()

	if includeTransactionPool {
		sendingInTransactionPool, receivingInTransactionPool, err = mp.transactionsPool.getTransactionsByAddresses()
		if err != nil {
			return nil, nil, nil, nil, err
		}
	}

	if includeOrphanPool {
		sendingInTransactionPool, receivingInOrphanPool, err = mp.orphansPool.getOrphanTransactionsByAddresses()
		if err != nil {
			return nil, nil, nil, nil, err
		}
	}

	return sendingInTransactionPool, receivingInTransactionPool, sendingInTransactionPool, receivingInOrphanPool, nil
}

func (mp *mempool) AllTransactions(includeTransactionPool bool, includeOrphanPool bool) (
	transactionPoolTransactions []*externalapi.DomainTransaction,
	orphanPoolTransactions []*externalapi.DomainTransaction) {

	mp.mtx.RLock()
	defer mp.mtx.RUnlock()

	if includeTransactionPool {
		transactionPoolTransactions = mp.transactionsPool.getAllTransactions()
	}

	if includeOrphanPool {
		orphanPoolTransactions = mp.orphansPool.getAllOrphanTransactions()
	}

	return transactionPoolTransactions, orphanPoolTransactions
}

func (mp *mempool) TransactionCount(includeTransactionPool bool, includeOrphanPool bool) int {
	mp.mtx.RLock()
	defer mp.mtx.RUnlock()

	transactionCount := 0

	if includeOrphanPool {
		transactionCount += mp.orphansPool.orphanTransactionCount()
	}
	if includeTransactionPool {
		transactionCount += mp.transactionsPool.transactionCount()
	}

	return transactionCount
}

func (mp *mempool) HandleNewBlockTransactions(transactions []*externalapi.DomainTransaction) (
	acceptedOrphans []*externalapi.DomainTransaction, err error) {

	mp.mtx.Lock()
	defer mp.mtx.Unlock()

	return mp.handleNewBlockTransactions(transactions)
}

func (mp *mempool) BlockCandidateTransactions() []*externalapi.DomainTransaction {
	mp.mtx.RLock()
	defer mp.mtx.RUnlock()

	readyTxs := mp.transactionsPool.allReadyTransactions()
	var candidateTxs []*externalapi.DomainTransaction
	var spamTx *externalapi.DomainTransaction
	var spamTxNewestUTXODaaScore uint64
	for i := range readyTxs {
		if len(readyTxs[i].Outputs) <= 2 {
			candidateTxs = append(candidateTxs, readyTxs[i])
			continue
		} else {
			hasCoinbaseInput := false
			for x := 0; x < len(readyTxs[i].Inputs); x++ {
				if readyTxs[i].Inputs[x].UTXOEntry.IsCoinbase() {
					hasCoinbaseInput = true
					break
				}
			}

			numExtraOuts := len(readyTxs[i].Outputs) - len(readyTxs[i].Inputs)
			if !hasCoinbaseInput && numExtraOuts > 2 && readyTxs[i].Fee < uint64(numExtraOuts)*constants.SompiPerHoosat {
				log.Debugf("Filtered spam tx %s", consensushashing.TransactionID(readyTxs[i]))
				continue
			}

			if hasCoinbaseInput || readyTxs[i].Fee > uint64(numExtraOuts)*constants.SompiPerHoosat {
				candidateTxs = append(candidateTxs, readyTxs[i])
			} else {
				txNewestUTXODaaScore := readyTxs[i].Inputs[0].UTXOEntry.BlockDAAScore()

				for x := 0; x < len(readyTxs[i].Inputs); x++ {
					if readyTxs[i].Inputs[x].UTXOEntry.BlockDAAScore() > txNewestUTXODaaScore {
						txNewestUTXODaaScore = readyTxs[i].Inputs[x].UTXOEntry.BlockDAAScore()
					}
				}

				if spamTx != nil {
					if txNewestUTXODaaScore < spamTxNewestUTXODaaScore {
						spamTx = readyTxs[i]
						spamTxNewestUTXODaaScore = txNewestUTXODaaScore
					}
				} else {
					spamTx = readyTxs[i]
					spamTxNewestUTXODaaScore = txNewestUTXODaaScore
				}
			}
		}
	}

	if spamTx != nil {
		log.Debugf("Adding spam tx candidate %s", consensushashing.TransactionID(spamTx))
		candidateTxs = append(candidateTxs, spamTx)
	}

	return candidateTxs
}

func (mp *mempool) RevalidateHighPriorityTransactions() (validTransactions []*externalapi.DomainTransaction, err error) {
	mp.mtx.Lock()
	defer mp.mtx.Unlock()

	return mp.revalidateHighPriorityTransactions()
}

func (mp *mempool) RemoveInvalidTransactions(err *ruleerrors.ErrInvalidTransactionsInNewBlock) error {
	mp.mtx.Lock()
	defer mp.mtx.Unlock()

	for _, tx := range err.InvalidTransactions {
		removeRedeemers := !errors.As(tx.Error, &ruleerrors.ErrMissingTxOut{})
		err := mp.removeTransaction(consensushashing.TransactionID(tx.Transaction), removeRedeemers)
		if err != nil {
			return err
		}
	}

	return nil
}

func (mp *mempool) RemoveTransaction(transactionID *externalapi.DomainTransactionID, removeRedeemers bool) error {
	mp.mtx.Lock()
	defer mp.mtx.Unlock()

	return mp.removeTransaction(transactionID, removeRedeemers)
}

// FreezeWallet adds an address to the frozen wallets list
func (mp *mempool) FreezeWallet(address string) error {
	mp.mtx.Lock()
	defer mp.mtx.Unlock()

	mp.walletFreezingManager.freezeWallet(address)
	log.Infof("Wallet address %s has been frozen", address)
	return nil
}

// UnfreezeWallet removes an address from the frozen wallets list
func (mp *mempool) UnfreezeWallet(address string) error {
	mp.mtx.Lock()
	defer mp.mtx.Unlock()

	mp.walletFreezingManager.unfreezeWallet(address)
	log.Infof("Wallet address %s has been unfrozen", address)
	return nil
}

// IsFrozen checks if a specific address is frozen
func (mp *mempool) IsFrozen(address string) bool {
	mp.mtx.RLock()
	defer mp.mtx.RUnlock()

	return mp.walletFreezingManager.isFrozen(address)
}

// GetFrozenWallets returns a list of all frozen wallet addresses
func (mp *mempool) GetFrozenWallets() []string {
	mp.mtx.RLock()
	defer mp.mtx.RUnlock()

	return mp.walletFreezingManager.getFrozenWallets()
}
