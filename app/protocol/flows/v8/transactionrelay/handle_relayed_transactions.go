package transactionrelay

import (
	"time"

	"github.com/HoosatNetwork/HTND/app/appmessage"
	"github.com/HoosatNetwork/HTND/app/protocol/common"
	"github.com/HoosatNetwork/HTND/app/protocol/flowcontext"
	"github.com/HoosatNetwork/HTND/app/protocol/protocolerrors"
	"github.com/HoosatNetwork/HTND/domain"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/domain/miningmanager/mempool"
	"github.com/HoosatNetwork/HTND/infrastructure/network/netadapter"
	"github.com/HoosatNetwork/HTND/infrastructure/network/netadapter/router"
	"github.com/pkg/errors"
)

// TransactionsRelayContext is the interface for the context needed for the
// HandleRelayedTransactions and HandleRequestedTransactions flows.
type TransactionsRelayContext interface {
	NetAdapter() *netadapter.NetAdapter
	Domain() domain.Domain
	SharedRequestedTransactions() *flowcontext.SharedRequestedTransactions
	OnTransactionAddedToMempool()
	EnqueueTransactionIDsForPropagation(transactionIDs []*externalapi.DomainTransactionID) error
	IsNearlySynced() (bool, error)
	IsIBDRunning() bool
}

type handleRelayedTransactionsFlow struct {
	TransactionsRelayContext
	incomingRoute, outgoingRoute *router.Route
	invsQueue                    []*appmessage.MsgInvTransaction

	// pendingTransactions holds transactions that were fetched from the peer while this node was not
	// yet able to process them, in arrival order. pendingTransactionIDs indexes it for deduplication.
	pendingTransactions   []*externalapi.DomainTransaction
	pendingTransactionIDs map[externalapi.DomainTransactionID]struct{}
}

// HandleRelayedTransactions listens to appmessage.MsgInvTransaction messages, requests their corresponding transactions if they
// are missing, adds them to the mempool and propagates them to the rest of the network.
func HandleRelayedTransactions(context TransactionsRelayContext, incomingRoute *router.Route, outgoingRoute *router.Route) error {
	flow := &handleRelayedTransactionsFlow{
		TransactionsRelayContext: context,
		incomingRoute:            incomingRoute,
		outgoingRoute:            outgoingRoute,
		invsQueue:                make([]*appmessage.MsgInvTransaction, 0),
		pendingTransactionIDs:    make(map[externalapi.DomainTransactionID]struct{}),
	}
	return flow.start()
}

// pendingRetryInterval is how often the flow wakes to re-check whether it can now process the
// transactions it is holding, when no new inv happens to arrive to drive the loop.
const pendingRetryInterval = 1 * time.Second

// maxPendingRelayedTransactions bounds the transactions held while this node cannot process them.
// A long IBD can outlast a great many relayed transactions, and they are held as full transactions
// rather than as ids, so this cannot be unbounded. On overflow the OLDEST held transaction is
// dropped: the newest are the likeliest to still be valid by the time the node catches up.
const maxPendingRelayedTransactions = 10_000

func (flow *handleRelayedTransactionsFlow) start() error {
	for {
		// Drain anything held from earlier, if the node has since become able to process it.
		if err := flow.processPendingTransactions(); err != nil {
			return err
		}

		inv, err := flow.readInv()
		if err != nil {
			// A read timeout is not a failure - it is how the loop wakes up to retry the held
			// transactions when the peer has gone quiet. Only reached while something is held.
			if errors.Is(err, router.ErrTimeout) {
				continue
			}
			return err
		}

		requestedIDs, err := flow.requestInvTransactions(inv)
		if err != nil {
			return err
		}

		err = flow.receiveTransactions(requestedIDs)
		if err != nil {
			return err
		}
	}
}

// readyToProcessTransactions reports whether relayed transactions can be handed to the mempool now.
//
// They cannot be, while this node is performing IBD and is not yet nearly synced: the UTXO set is
// far behind, so fillInputsAndGetMissingParents fails to resolve almost every input, the stream
// would land in the bounded orphan pool and churn it, and the node is not mining so nothing consumes
// the mempool.
//
// The IsIBDRunning() condition matters as much as the sync one. IsNearlySynced() only asks whether
// the selected tip's timestamp falls inside the DAA window - it says nothing about IBD - so on its
// own it also suspended processing for a node that finished IBD long ago, holds a complete UTXO set,
// and can validate transactions perfectly well, but whose virtual stalled momentarily (a
// disqualification cascade, a slow restorePastUTXO walk, clock skew, a brief loss of peers). That
// node has no reason to defer anything.
func (flow *handleRelayedTransactionsFlow) readyToProcessTransactions() (bool, error) {
	if !flow.IsIBDRunning() {
		return true, nil
	}
	return flow.IsNearlySynced()
}

// holdTransaction stores a fetched transaction until this node can process it.
func (flow *handleRelayedTransactionsFlow) holdTransaction(transaction *externalapi.DomainTransaction,
	transactionID *externalapi.DomainTransactionID,
) {
	if _, alreadyHeld := flow.pendingTransactionIDs[*transactionID]; alreadyHeld {
		return
	}

	if len(flow.pendingTransactions) >= maxPendingRelayedTransactions {
		oldest := flow.pendingTransactions[0]
		delete(flow.pendingTransactionIDs, *consensushashing.TransactionID(oldest))
		// Clear the slot before re-slicing so the dropped transaction can be collected even while the
		// backing array is still alive.
		flow.pendingTransactions[0] = nil
		flow.pendingTransactions = flow.pendingTransactions[1:]
	}

	flow.pendingTransactions = append(flow.pendingTransactions, transaction)
	flow.pendingTransactionIDs[*transactionID] = struct{}{}
}

// processPendingTransactions feeds the held transactions to the mempool, once this node can accept
// them, in the order they arrived.
//
// A rule error here is never grounds for banning, unlike in receiveTransactions. These transactions
// were fetched successfully and were valid when the peer relayed them; if one went stale while this
// node was syncing - its inputs spent, or it was mined - that is this node's latency, not the peer's
// misbehaviour.
func (flow *handleRelayedTransactionsFlow) processPendingTransactions() error {
	if len(flow.pendingTransactions) == 0 {
		return nil
	}

	ready, err := flow.readyToProcessTransactions()
	if err != nil {
		return err
	}
	if !ready {
		return nil
	}

	pending := flow.pendingTransactions
	flow.pendingTransactions = nil
	flow.pendingTransactionIDs = make(map[externalapi.DomainTransactionID]struct{})

	log.Infof("Node is ready to process transactions again - handling %d transaction(s) that were "+
		"fetched and held while it was syncing.", len(pending))

	accepted := 0
	for _, transaction := range pending {
		acceptedTransactions, err := flow.Domain().MiningManager().ValidateAndInsertTransaction(
			transaction, false, true, false)
		if err != nil {
			ruleErr := &mempool.RuleError{}
			if !errors.As(err, ruleErr) {
				return errors.Wrapf(err, "failed to process transaction %s",
					consensushashing.TransactionID(transaction))
			}
			log.Debugf("Held transaction %s was not accepted once processing resumed: %s",
				consensushashing.TransactionID(transaction), ruleErr)
			continue
		}
		accepted++

		err = flow.broadcastAcceptedTransactions(consensushashing.TransactionIDs(acceptedTransactions))
		if err != nil {
			return err
		}
		flow.OnTransactionAddedToMempool()
	}

	log.Infof("Accepted %d of %d held transaction(s); the rest had gone stale while this node synced.",
		accepted, len(pending))
	return nil
}

func (flow *handleRelayedTransactionsFlow) requestInvTransactions(
	inv *appmessage.MsgInvTransaction,
) (requestedIDs []*externalapi.DomainTransactionID, err error) {
	idsToRequest := make([]*externalapi.DomainTransactionID, 0, len(inv.TxIDs))
	for _, txID := range inv.TxIDs {
		if flow.isKnownTransaction(txID) {
			continue
		}
		exists := flow.SharedRequestedTransactions().AddIfNotExists(txID)
		if exists {
			continue
		}
		idsToRequest = append(idsToRequest, txID)
	}

	if len(idsToRequest) == 0 {
		return idsToRequest, nil
	}

	msgGetTransactions := appmessage.NewMsgRequestTransactions(idsToRequest)
	err = flow.outgoingRoute.Enqueue(msgGetTransactions)
	if err != nil {
		flow.SharedRequestedTransactions().RemoveMany(idsToRequest)
		return nil, err
	}
	return idsToRequest, nil
}

func (flow *handleRelayedTransactionsFlow) isKnownTransaction(txID *externalapi.DomainTransactionID) bool {
	// Ask the transaction memory pool if the transaction is known
	// to it in any form (main pool or orphan).
	if _, _, ok := flow.Domain().MiningManager().GetTransactionNoClone(txID, true, true); ok {
		return true
	}

	// Already fetched and waiting for this node to become able to process it. Without this, a repeat
	// inv would have it fetched from the peer a second time, since it is not in the mempool yet.
	if _, held := flow.pendingTransactionIDs[*txID]; held {
		return true
	}

	return false
}

func (flow *handleRelayedTransactionsFlow) readInv() (*appmessage.MsgInvTransaction, error) {
	if len(flow.invsQueue) > 0 {
		var inv *appmessage.MsgInvTransaction
		inv, flow.invsQueue = flow.invsQueue[0], flow.invsQueue[1:]
		return inv, nil
	}

	// While transactions are being held, never block indefinitely: the loop has to get control back
	// to notice that the node became able to process them, even if this peer sends nothing further.
	var msg appmessage.Message
	var err error
	if len(flow.pendingTransactions) > 0 {
		msg, err = flow.incomingRoute.DequeueWithTimeout(pendingRetryInterval)
	} else {
		msg, err = flow.incomingRoute.Dequeue()
	}
	if err != nil {
		return nil, err
	}

	inv, ok := msg.(*appmessage.MsgInvTransaction)
	if !ok {
		return nil, protocolerrors.Errorf(true, "unexpected %s message in the block relay flow while "+
			"expecting an inv message", msg.Command())
	}
	return inv, nil
}

func (flow *handleRelayedTransactionsFlow) broadcastAcceptedTransactions(acceptedTxIDs []*externalapi.DomainTransactionID) error {
	return flow.EnqueueTransactionIDsForPropagation(acceptedTxIDs)
}

// readMsgTxOrNotFound returns the next msgTx or msgTransactionNotFound in incomingRoute,
// returning only one of the message types at a time.
//
// and populates invsQueue with any inv messages that meanwhile arrive.
func (flow *handleRelayedTransactionsFlow) readMsgTxOrNotFound() (
	msgTx *appmessage.MsgTx, msgNotFound *appmessage.MsgTransactionNotFound, err error,
) {
	for {
		message, err := flow.incomingRoute.DequeueWithTimeout(common.DefaultTimeout)
		if err != nil {
			return nil, nil, err
		}

		switch message := message.(type) {
		case *appmessage.MsgInvTransaction:
			flow.invsQueue = append(flow.invsQueue, message)
		case *appmessage.MsgTx:
			return message, nil, nil
		case *appmessage.MsgTransactionNotFound:
			return nil, message, nil
		default:
			return nil, nil, errors.Errorf("unexpected message %s", message.Command())
		}
	}
}

func (flow *handleRelayedTransactionsFlow) receiveTransactions(requestedTransactions []*externalapi.DomainTransactionID) error {
	// In case the function returns earlier than expected, we want to make sure sharedRequestedTransactions is
	// clean from any pending transactions.
	defer flow.SharedRequestedTransactions().RemoveMany(requestedTransactions)

	// Evaluated once for the batch rather than per transaction: IsNearlySynced reads consensus state,
	// and the answer cannot meaningfully change within a single batch of responses.
	readyToProcess, err := flow.readyToProcessTransactions()
	if err != nil {
		return err
	}

	for _, expectedID := range requestedTransactions {
		msgTx, msgTxNotFound, err := flow.readMsgTxOrNotFound()
		if err != nil {
			return err
		}
		if msgTxNotFound != nil {
			if !msgTxNotFound.ID.Equal(expectedID) {
				return protocolerrors.Errorf(true, "expected transaction %s, but got %s",
					expectedID, msgTxNotFound.ID)
			}

			continue
		}
		tx := appmessage.MsgTxToDomainTransaction(msgTx)
		txID := consensushashing.TransactionID(tx)
		// log.Infof("Received relayed transaction %s", txID)
		if !txID.Equal(expectedID) {
			return protocolerrors.Errorf(true, "expected transaction %s, but got %s",
				expectedID, txID)
		}

		// Fetch always, process only when able. The inv is what is scarce here: a transaction inv is
		// advertised once per peer and never re-sent, and the peer only serves the transaction while it
		// is still in its own mempool - so deferring the REQUEST loses the transaction outright. The
		// bytes, once fetched, keep. Hold them and hand them to the mempool when this node can
		// actually validate them.
		if !readyToProcess {
			flow.holdTransaction(tx, txID)
			continue
		}

		// isLocalSubmission=false: this came from a peer, so node-local submission policy (the
		// compound-transaction rate limiter) must not apply to it.
		acceptedTransactions, err := flow.Domain().MiningManager().ValidateAndInsertTransaction(tx, false, true, false)
		if err != nil {
			ruleErr := &mempool.RuleError{}
			if !errors.As(err, ruleErr) {
				return errors.Wrapf(err, "failed to process transaction %s", txID)
			}

			shouldBan := false
			if txRuleErr := (&mempool.TxRuleError{}); errors.As(ruleErr.Err, txRuleErr) {
				if txRuleErr.RejectCode == mempool.RejectInvalid {
					shouldBan = true
				}
			}

			if !shouldBan {
				continue
			}

			return protocolerrors.Errorf(true, "rejected transaction %s: %s", txID, ruleErr)
		}
		err = flow.broadcastAcceptedTransactions(consensushashing.TransactionIDs(acceptedTransactions))
		if err != nil {
			return err
		}
		flow.OnTransactionAddedToMempool()
	}
	return nil
}
