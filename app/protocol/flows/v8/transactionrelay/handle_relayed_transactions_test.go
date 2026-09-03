package transactionrelay_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/app/protocol/flowcontext"
	"github.com/HoosatNetwork/HTND/app/protocol/flows/v8/transactionrelay"

	"github.com/HoosatNetwork/HTND/app/protocol/protocolerrors"
	"github.com/HoosatNetwork/HTND/domain"
	"github.com/HoosatNetwork/HTND/domain/consensus"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/testutils"
	"github.com/HoosatNetwork/HTND/domain/dagconfig"
	"github.com/HoosatNetwork/HTND/domain/miningmanager/mempool"
	"github.com/HoosatNetwork/HTND/infrastructure/logger"
	"github.com/HoosatNetwork/HTND/util/panics"

	"github.com/HoosatNetwork/HTND/app/appmessage"
	"github.com/HoosatNetwork/HTND/infrastructure/config"
	"github.com/HoosatNetwork/HTND/infrastructure/network/netadapter"
	"github.com/HoosatNetwork/HTND/infrastructure/network/netadapter/router"
)

type mocTransactionsRelayContext struct {
	netAdapter                  *netadapter.NetAdapter
	domain                      domain.Domain
	sharedRequestedTransactions *flowcontext.SharedRequestedTransactions

	// Zero values keep the default "synced, not doing IBD" behaviour the existing tests rely on.
	notNearlySynced bool
	ibdRunning      bool
}

func (m *mocTransactionsRelayContext) NetAdapter() *netadapter.NetAdapter {
	return m.netAdapter
}

func (m *mocTransactionsRelayContext) Domain() domain.Domain {
	return m.domain
}

func (m *mocTransactionsRelayContext) SharedRequestedTransactions() *flowcontext.SharedRequestedTransactions {
	return m.sharedRequestedTransactions
}

func (m *mocTransactionsRelayContext) EnqueueTransactionIDsForPropagation(_ []*externalapi.DomainTransactionID) error {
	return nil
}

func (m *mocTransactionsRelayContext) OnTransactionAddedToMempool() {
}

func (m *mocTransactionsRelayContext) IsNearlySynced() (bool, error) {
	return !m.notNearlySynced, nil
}

func (m *mocTransactionsRelayContext) IsIBDRunning() bool {
	return m.ibdRunning
}

// TestHandleRelayedTransactionsNotFound tests the flow of  HandleRelayedTransactions when the peer doesn't
// have the requested transactions in the mempool.
func TestHandleRelayedTransactionsNotFound(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		log := logger.RegisterSubSystem("PROT")
		spawn := panics.GoroutineWrapperFunc(log)
		factory := consensus.NewFactory()
		tc, teardown, err := factory.NewTestConsensus(consensusConfig, "TestHandleRelayedTransactionsNotFound")
		if err != nil {
			t.Fatalf("Error setting up test consensus: %+v", err)
		}
		defer teardown(false)

		sharedRequestedTransactions := flowcontext.NewSharedRequestedTransactions()
		adapter, err := netadapter.NewNetAdapter(config.DefaultConfig())
		if err != nil {
			t.Fatalf("Failed to create a NetAdapter: %v", err)
		}
		domainInstance, err := domain.New(consensusConfig, mempool.DefaultConfig(&consensusConfig.Params), tc.Database())
		if err != nil {
			t.Fatalf("Failed to set up a domain instance: %v", err)
		}
		context := &mocTransactionsRelayContext{
			netAdapter:                  adapter,
			domain:                      domainInstance,
			sharedRequestedTransactions: sharedRequestedTransactions,
		}
		incomingRoute := router.NewRoute("incoming")
		defer incomingRoute.Close()
		peerIncomingRoute := router.NewRoute("outgoing")
		defer peerIncomingRoute.Close()

		txID1 := externalapi.NewDomainTransactionIDFromByteArray(&[externalapi.DomainHashSize]byte{
			0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
			0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
			0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
			0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01,
		})
		txID2 := externalapi.NewDomainTransactionIDFromByteArray(&[externalapi.DomainHashSize]byte{
			0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
			0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
			0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
			0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x02,
		})
		txIDs := []*externalapi.DomainTransactionID{txID1, txID2}
		invMessage := appmessage.NewMsgInvTransaction(txIDs)
		err = incomingRoute.Enqueue(invMessage)
		if err != nil {
			t.Fatalf("Unexpected error from incomingRoute.Enqueue: %v", err)
		}
		// The goroutine is representing the peer's actions.
		spawn("peerResponseToTheTransactionsRequest", func() {
			msg, err := peerIncomingRoute.Dequeue()
			if err != nil {
				t.Fatalf("Dequeue: %v", err)
			}
			inv := msg.(*appmessage.MsgRequestTransactions)

			if len(txIDs) != len(inv.IDs) {
				t.Fatalf("TestHandleRelayedTransactions: expected %d transactions ID, but got %d", len(txIDs), len(inv.IDs))
			}

			for i, id := range inv.IDs {
				if txIDs[i].String() != id.String() {
					t.Fatalf("TestHandleRelayedTransactions: expected equal txID: expected %s, but got %s", txIDs[i].String(), id.String())
				}
				err = incomingRoute.Enqueue(appmessage.NewMsgTransactionNotFound(txIDs[i]))
				if err != nil {
					t.Fatalf("Unexpected error from incomingRoute.Enqueue: %v", err)
				}
			}
			// Insert an unexpected message type to stop the infinity loop.
			err = incomingRoute.Enqueue(&appmessage.MsgAddresses{})
			if err != nil {
				t.Fatalf("Unexpected error from incomingRoute.Enqueue: %v", err)
			}
		})

		err = transactionrelay.HandleRelayedTransactions(context, incomingRoute, peerIncomingRoute)
		// Since we inserted an unexpected message type to stop the infinity loop,
		// we expect the error will be infected from this specific message and also the
		// error will count as a protocol message.
		if protocolErr := (protocolerrors.ProtocolError{}); err == nil || !errors.As(err, &protocolErr) {
			t.Fatalf("Expected to protocol error")
		} else {
			if !protocolErr.ShouldBan {
				t.Fatalf("Exepcted shouldBan true, but got false.")
			}
			if !strings.Contains(err.Error(), "unexpected Addresses [code 3] message in the block relay flow while expecting an inv message") {
				t.Fatalf("Unexpected error: expected: an error due to existence of an Addresses message "+
					"in the block relay flow, but got: %v", protocolErr.Cause)
			}
		}
	})
}

// TestOnClosedIncomingRoute verifies that an appropriate error message will be returned when
// trying to dequeue a message from a closed route.
func TestOnClosedIncomingRoute(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		factory := consensus.NewFactory()
		tc, teardown, err := factory.NewTestConsensus(consensusConfig, "TestOnClosedIncomingRoute")
		if err != nil {
			t.Fatalf("Error setting up test consensus: %+v", err)
		}
		defer teardown(false)

		sharedRequestedTransactions := flowcontext.NewSharedRequestedTransactions()
		adapter, err := netadapter.NewNetAdapter(config.DefaultConfig())
		if err != nil {
			t.Fatalf("Failed to creat a NetAdapter : %v", err)
		}
		domainInstance, err := domain.New(consensusConfig, mempool.DefaultConfig(&consensusConfig.Params), tc.Database())
		if err != nil {
			t.Fatalf("Failed to set up a domain instance: %v", err)
		}
		context := &mocTransactionsRelayContext{
			netAdapter:                  adapter,
			domain:                      domainInstance,
			sharedRequestedTransactions: sharedRequestedTransactions,
		}
		incomingRoute := router.NewRoute("incoming")
		outgoingRoute := router.NewRoute("outgoing")
		defer outgoingRoute.Close()

		txID := externalapi.NewDomainTransactionIDFromByteArray(&[externalapi.DomainHashSize]byte{
			0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
			0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
			0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
			0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01,
		})
		txIDs := []*externalapi.DomainTransactionID{txID}

		err = incomingRoute.Enqueue(&appmessage.MsgInvTransaction{TxIDs: txIDs})
		if err != nil {
			t.Fatalf("Unexpected error from incomingRoute.Enqueue: %v", err)
		}
		incomingRoute.Close()
		err = transactionrelay.HandleRelayedTransactions(context, incomingRoute, outgoingRoute)
		if err == nil || !errors.Is(err, router.ErrRouteClosed) {
			t.Fatalf("Unexpected error: expected: %v, got : %v", router.ErrRouteClosed, err)
		}
	})
}

// TestHandleRelayedTransactionsAlwaysFetches pins that the transaction is REQUESTED from the peer
// regardless of this node's sync state.
//
// The inv is the scarce thing: a transaction inv is advertised once per peer and never re-sent, and
// the peer only serves the transaction while it is still in its own mempool. So deferring the
// request - which is what both the original `if !isNearlySynced { continue }` and the later
// hold-the-inv version did - loses the transaction outright. Fetching is always safe; it is only
// handing the transaction to the mempool that has to wait for the node to be able to validate it.
func TestHandleRelayedTransactionsAlwaysFetches(t *testing.T) {
	tests := []struct {
		name            string
		notNearlySynced bool
		ibdRunning      bool
	}{
		{name: "synced, no IBD", notNearlySynced: false, ibdRunning: false},
		{name: "lagging, no IBD", notNearlySynced: true, ibdRunning: false},
		{name: "lagging, IBD running", notNearlySynced: true, ibdRunning: true},
		{name: "nearly synced during IBD", notNearlySynced: false, ibdRunning: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			consensusConfig := &consensus.Config{Params: dagconfig.SimnetParams}
			factory := consensus.NewFactory()
			tc, teardown, err := factory.NewTestConsensus(consensusConfig, "TestHandleRelayedTransactionsAlwaysFetches")
			if err != nil {
				t.Fatalf("Error setting up test consensus: %+v", err)
			}
			defer teardown(false)

			adapter, err := netadapter.NewNetAdapter(config.DefaultConfig())
			if err != nil {
				t.Fatalf("Failed to create a NetAdapter: %v", err)
			}
			domainInstance, err := domain.New(consensusConfig, mempool.DefaultConfig(&consensusConfig.Params), tc.Database())
			if err != nil {
				t.Fatalf("Failed to set up a domain instance: %v", err)
			}

			context := &mocTransactionsRelayContext{
				netAdapter:                  adapter,
				domain:                      domainInstance,
				sharedRequestedTransactions: flowcontext.NewSharedRequestedTransactions(),
				notNearlySynced:             test.notNearlySynced,
				ibdRunning:                  test.ibdRunning,
			}

			incomingRoute := router.NewRoute("incoming")
			defer incomingRoute.Close()
			outgoingRoute := router.NewRoute("outgoing")
			defer outgoingRoute.Close()

			txID := externalapi.NewDomainTransactionIDFromByteArray(&[externalapi.DomainHashSize]byte{
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x07,
			})
			if err := incomingRoute.Enqueue(appmessage.NewMsgInvTransaction(
				[]*externalapi.DomainTransactionID{txID})); err != nil {
				t.Fatalf("Unexpected error from incomingRoute.Enqueue: %v", err)
			}

			done := make(chan struct{})
			go func() {
				defer close(done)
				_ = transactionrelay.HandleRelayedTransactions(context, incomingRoute, outgoingRoute)
			}()

			msg, err := outgoingRoute.DequeueWithTimeout(5 * time.Second)
			if err != nil {
				t.Fatalf("notNearlySynced=%t ibdRunning=%t: expected the transaction to be requested "+
					"from the peer, but nothing was sent: %v", test.notNearlySynced, test.ibdRunning, err)
			}
			request, ok := msg.(*appmessage.MsgRequestTransactions)
			if !ok {
				t.Fatalf("expected a MsgRequestTransactions, got %s", msg.Command())
			}
			if len(request.IDs) != 1 || !request.IDs[0].Equal(txID) {
				t.Fatalf("expected a request for %s, got %v", txID, request.IDs)
			}

			incomingRoute.Close()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatalf("HandleRelayedTransactions did not return")
			}
		})
	}
}
