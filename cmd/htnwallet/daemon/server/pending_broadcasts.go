package server

import (
	"slices"
	"strings"
	"time"

	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter/router"
	"github.com/pkg/errors"
)

const (
	// pendingBroadcastGracePeriod is how long after its broadcast a transaction is first checked on. By
	// then an ordinary payment has been carried by a block and merged, so the node not holding it means
	// it was accepted or lost - not that a block carrying it is still waiting to be merged, in which case
	// a resubmission would put an already-mined transaction back into the mempool.
	pendingBroadcastGracePeriod = time.Minute
	// pendingBroadcastCheckInterval is how often each pending transaction is checked on after that.
	pendingBroadcastCheckInterval = 30 * time.Second
	// pendingBroadcastMaxAge is when the wallet stops checking on a transaction. The used-outpoint expiry
	// (usedOutpointHasExpired) releases its inputs by then in any case.
	pendingBroadcastMaxAge = 2 * time.Hour
)

// pendingBroadcast is a transaction this daemon broadcast and has not yet seen leave the node's mempool
// for good.
type pendingBroadcast struct {
	transaction    *externalapi.DomainTransaction
	broadcastTime  time.Time
	lastCheckTime  time.Time
	resubmissions  int
	transactionID  string
	inputOutpoints []externalapi.DomainOutpoint
}

// trackBroadcast starts checking on a transaction the node just accepted. The caller holds s.lock.
func (s *server) trackBroadcast(transaction *externalapi.DomainTransaction, broadcastTime time.Time) {
	if s.pendingBroadcasts == nil {
		s.pendingBroadcasts = make(map[externalapi.DomainTransactionID]*pendingBroadcast)
	}
	transactionID := consensushashing.TransactionID(transaction)
	inputOutpoints := make([]externalapi.DomainOutpoint, len(transaction.Inputs))
	for i, input := range transaction.Inputs {
		inputOutpoints[i] = input.PreviousOutpoint
	}
	s.pendingBroadcasts[*transactionID] = &pendingBroadcast{
		transaction:    transaction,
		broadcastTime:  broadcastTime,
		transactionID:  transactionID.String(),
		inputOutpoints: inputOutpoints,
	}
}

// pendingBroadcastVerdict is what one check found out about a pending transaction.
type pendingBroadcastVerdict int

const (
	// stillPending: the node holds the transaction, accepted it again, or could not be asked.
	stillPending pendingBroadcastVerdict = iota
	// settled: the node no longer holds the transaction and refuses it, so it was either accepted or
	// can never be. Either way its inputs are not the wallet's to keep reserved.
	settled
)

// checkPendingBroadcasts makes sure every transaction this daemon broadcast either gets accepted or
// releases its inputs.
//
// A node takes a transaction out of its mempool as soon as a block carrying it arrives. If that block is
// never merged - disqualified from the chain, or too deep in a reorg to merge - the transaction is in no
// mempool and no block that counts, and nobody resends it: the payment never arrives, the change output
// never appears, and the input it spent stays hidden behind usedOutpoints for an hour although consensus
// never spent it. So once a transaction has had time to be accepted, the wallet asks the node for it, and
// if the node no longer holds it, submits it again. A transaction that was lost goes back into the
// mempool. One that was accepted, or can no longer be (its input vanished in a reorg), is refused, and its
// inputs are released at once.
func (s *server) checkPendingBroadcasts(now time.Time) {
	var due []*pendingBroadcast
	s.lock.Lock()
	for transactionID, pending := range s.pendingBroadcasts {
		if now.Sub(pending.broadcastTime) > pendingBroadcastMaxAge {
			delete(s.pendingBroadcasts, transactionID)
			continue
		}
		if now.Sub(pending.broadcastTime) < pendingBroadcastGracePeriod ||
			now.Sub(pending.lastCheckTime) < pendingBroadcastCheckInterval {
			continue
		}
		pending.lastCheckTime = now
		due = append(due, pending)
	}
	s.lock.Unlock()

	for _, pending := range due {
		// The node is asked without holding s.lock, so user requests are not held up by it.
		if s.checkPendingBroadcast(pending) != settled {
			continue
		}
		s.lock.Lock()
		for _, outpoint := range pending.inputOutpoints {
			delete(s.usedOutpoints, outpoint)
		}
		s.forgetSettledInputs(pending.inputOutpoints)
		delete(s.pendingBroadcasts, *consensushashing.TransactionID(pending.transaction))
		s.lock.Unlock()
	}
}

// forgetSettledInputs drops a settled transaction's inputs from the wallet's UTXO set. Settled means the
// node refuses those inputs: this transaction or another spent them, or they vanished in a reorg. A
// compound reuses the set between refreshes and selects the smallest coins first, so once usedOutpoints
// stops hiding them it would pick the same coins again and have its broadcast refused. An input that is
// in fact still unspent comes back with the next refresh. The caller holds s.lock.
func (s *server) forgetSettledInputs(inputOutpoints []externalapi.DomainOutpoint) {
	settledInputs := make(map[externalapi.DomainOutpoint]struct{}, len(inputOutpoints))
	for _, outpoint := range inputOutpoints {
		settledInputs[outpoint] = struct{}{}
	}
	s.utxosSortedByAmount = slices.DeleteFunc(s.utxosSortedByAmount, func(utxo *walletUTXO) bool {
		_, settled := settledInputs[*utxo.Outpoint]
		return settled
	})
}

func (s *server) checkPendingBroadcast(pending *pendingBroadcast) pendingBroadcastVerdict {
	_, err := s.backgroundRPCClient.GetMempoolEntry(pending.transactionID, true, false)
	if err == nil {
		return stillPending
	}
	if !strings.Contains(err.Error(), "was not found") {
		log.Debugf("Could not ask the node about pending transaction %s: %s", pending.transactionID, err)
		return stillPending
	}

	switch s.acceptanceVerdict(pending) {
	case appmessage.TransactionStatusAccepted, appmessage.TransactionStatusConfirmed:
		log.Debugf("Transaction %s was accepted; its inputs are released", pending.transactionID)
		return settled
	case appmessage.TransactionStatusInvalid:
		log.Infof("Transaction %s was merged and rejected by the chain; its inputs are released",
			pending.transactionID)
		return settled
	}

	_, err = sendTransaction(s.backgroundRPCClient, pending.transaction, false, nil)
	if err == nil {
		pending.resubmissions++
		log.Infof("Resubmitted transaction %s (%d time(s)): the node no longer held it, and had not accepted it",
			pending.transactionID, pending.resubmissions)
		return stillPending
	}
	errString := strings.ToLower(err.Error())
	switch {
	case !strings.Contains(errString, "rejected transaction"),
		strings.Contains(errString, "already in the mempool"),
		strings.Contains(errString, "rate limit"):
		log.Debugf("Could not resubmit pending transaction %s yet: %s", pending.transactionID, err)
		return stillPending
	}
	log.Infof("Transaction %s left the node's mempool and the node refuses it again, so it was either "+
		"accepted or can no longer be; its inputs are released: %s", pending.transactionID, err)
	return settled
}

// acceptanceVerdict asks the node whether the chain accepted a transaction that has left its mempool.
//
// Leaving the mempool nearly always means a block carrying the transaction was merged. Without asking,
// the only way to tell that apart from a lost transaction was to submit it again and read the refusal,
// so every broadcast that succeeded ended in a resubmission the node refused over all of its inputs.
// An accepted or rejected verdict settles the transaction without one. Any other answer - not merged
// yet, not found, unknown - is left to the resubmission, which puts a lost transaction back.
//
// A node that predates the recent-chain lookup answers by scanning every block it holds, which outlasts
// the RPC timeout, and the late answer would then be read as the reply to the next request on the
// route. So after one failure the wallet stops asking for the rest of the session. Only the sync loop
// calls this, so transactionStatusUnavailable needs no lock.
func (s *server) acceptanceVerdict(pending *pendingBroadcast) appmessage.TransactionStatus {
	if s.transactionStatusUnavailable {
		return appmessage.TransactionStatusUnknown
	}
	response, err := s.backgroundRPCClient.GetTransactionStatus(pending.transactionID)
	if err != nil {
		if errors.Is(err, router.ErrTimeout) {
			s.transactionStatusUnavailable = true
			log.Infof("The node took too long to report the status of transaction %s; pending transactions "+
				"are checked by resubmitting them for the rest of this session: %s", pending.transactionID, err)
		} else {
			log.Debugf("Could not ask the node whether transaction %s was accepted: %s", pending.transactionID, err)
		}
		return appmessage.TransactionStatusUnknown
	}
	return response.Status
}
