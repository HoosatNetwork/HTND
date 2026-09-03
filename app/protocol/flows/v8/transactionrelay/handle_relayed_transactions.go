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
}

// HandleRelayedTransactions listens to appmessage.MsgInvTransaction messages, requests their corresponding transactions if they
// are missing, adds them to the mempool and propagates them to the rest of the network.
func HandleRelayedTransactions(context TransactionsRelayContext, incomingRoute *router.Route, outgoingRoute *router.Route) error {
	flow := &handleRelayedTransactionsFlow{
		TransactionsRelayContext: context,
		incomingRoute:            incomingRoute,
		outgoingRoute:            outgoingRoute,
		invsQueue:                make([]*appmessage.MsgInvTransaction, 0),
	}
	return flow.start()
}

// relayReadinessPollInterval is how often the flow re-checks whether the node has become able to
// process relayed transactions while it is holding off. It only runs while relay is suspended, so
// the cost is negligible; it is deliberately not sub-second, which is what made the previous
// attempt at holding invs (the since-removed 250ms sleep in blockrelay) show up as IBD overhead.
const relayReadinessPollInterval = 1 * time.Second

func (flow *handleRelayedTransactionsFlow) start() error {
	for {
		// Hold, rather than discard, while this node cannot process relayed transactions.
		if err := flow.waitUntilReadyToRelay(); err != nil {
			return err
		}

		inv, err := flow.readInv()
		if err != nil {
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

// waitUntilReadyToRelay blocks while this node is performing IBD and is not yet nearly synced.
//
// Relayed transactions cannot be usefully handled in that state: the UTXO set is far behind, so
// fillInputsAndGetMissingParents fails to resolve almost every input, the stream would land in the
// bounded orphan pool and churn it, and the node is not mining so nothing consumes the mempool.
//
// What it must NOT do is throw the invs away, which is what this used to do. A transaction inv is
// advertised once per peer; unlike a block inv - which AddOrphanRootsToQueue recovers later, when a
// descendant arrives and the missing ancestors are walked back - a discarded transaction inv is
// never re-sent, so the transaction simply never reaches this node. Those transactions are still
// wanted once the node catches up, so the flow waits instead and picks them up from the incoming
// route afterwards. The route is bounded (DefaultMaxMessages), and the netadapter already drops
// CmdInvTransaction on a full route with a debug log rather than disconnecting the peer, so a long
// IBD degrades to the old behaviour instead of building an unbounded backlog.
//
// The IsIBDRunning() condition matters as much as the sync one. IsNearlySynced() only asks whether
// the selected tip's timestamp falls inside the DAA window - it says nothing about IBD - so on its
// own it also suspended relay for a node that finished IBD long ago, holds a complete UTXO set, and
// can validate transactions perfectly well, but whose virtual stalled momentarily (a
// disqualification cascade, a slow restorePastUTXO walk, clock skew, a brief loss of peers). That
// node has no reason to hold anything back.
func (flow *handleRelayedTransactionsFlow) waitUntilReadyToRelay() error {
	waiting := false
	for {
		if !flow.IsIBDRunning() {
			break
		}
		isNearlySynced, err := flow.IsNearlySynced()
		if err != nil {
			return err
		}
		if isNearlySynced {
			break
		}

		if !waiting {
			waiting = true
			log.Infof("Transaction relay is on hold while IBD is in progress - incoming transaction " +
				"invs are being held until this node is nearly synced, not discarded.")
		}

		select {
		case <-flow.incomingRoute.Closed():
			// The peer went away while we were holding off. Return rather than loop forever on the
			// timer: nothing will ever arrive on this route again.
			return errors.Wrapf(router.ErrRouteClosed, "route '%s' was closed while transaction relay "+
				"was waiting for the node to become nearly synced", flow.incomingRoute.Name())
		case <-time.After(relayReadinessPollInterval):
		}
	}

	if waiting {
		log.Infof("Transaction relay resumed - the node is nearly synced; processing the transaction " +
			"invs that were held.")
	}
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

	return false
}

func (flow *handleRelayedTransactionsFlow) readInv() (*appmessage.MsgInvTransaction, error) {
	if len(flow.invsQueue) > 0 {
		var inv *appmessage.MsgInvTransaction
		inv, flow.invsQueue = flow.invsQueue[0], flow.invsQueue[1:]
		return inv, nil
	}

	msg, err := flow.incomingRoute.Dequeue()
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
