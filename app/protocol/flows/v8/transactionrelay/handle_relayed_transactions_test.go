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

// TestHandleRelayedTransactionsSyncGating pins when transaction relay is suspended, and that
// suspension HOLDS invs rather than discarding them.
//
// The guard used to be `if !isNearlySynced { continue }` - no IBD check, and the inv thrown away.
// Both halves were wrong. IsNearlySynced() only asks whether the selected tip's timestamp is inside
// the DAA window, so it also fired for a node that finished IBD long ago and can validate fine but
// whose virtual stalled briefly. And discarding is unrecoverable: unlike block invs, which
// AddOrphanRootsToQueue walks back when a descendant arrives, a transaction inv is advertised once
// per peer and never re-sent.
func TestHandleRelayedTransactionsSyncGating(t *testing.T) {
	tests := []struct {
		name            string
		notNearlySynced bool
		ibdRunning      bool
		wantRequest     bool
	}{
		{name: "synced, no IBD - relays", notNearlySynced: false, ibdRunning: false, wantRequest: true},
		{name: "lagging, no IBD - still relays", notNearlySynced: true, ibdRunning: false, wantRequest: true},
		{name: "lagging, IBD running - held", notNearlySynced: true, ibdRunning: true, wantRequest: false},
		{name: "nearly synced during IBD - relays", notNearlySynced: false, ibdRunning: true, wantRequest: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			consensusConfig := &consensus.Config{Params: dagconfig.SimnetParams}
			factory := consensus.NewFactory()
			tc, teardown, err := factory.NewTestConsensus(consensusConfig, "TestHandleRelayedTransactionsSyncGating")
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
			// Terminates the flow's loop once the inv above has been handled.
			if err := incomingRoute.Enqueue(&appmessage.MsgAddresses{}); err != nil {
				t.Fatalf("Unexpected error from incomingRoute.Enqueue: %v", err)
			}

			done := make(chan struct{})
			go func() {
				defer close(done)
				_ = transactionrelay.HandleRelayedTransactions(context, incomingRoute, outgoingRoute)
			}()

			_, err = outgoingRoute.DequeueWithTimeout(2 * time.Second)
			gotRequest := err == nil

			if gotRequest != test.wantRequest {
				t.Fatalf("notNearlySynced=%t ibdRunning=%t: requested transactions = %t, want %t",
					test.notNearlySynced, test.ibdRunning, gotRequest, test.wantRequest)
			}

			if !test.wantRequest {
				// The flow is parked waiting for the node to become ready, still holding the inv rather
				// than having discarded it. Closing the route is what releases it - which also pins that
				// a peer going away while relay is suspended does not leak the flow goroutine.
				if length := incomingRoute.Length(); length == 0 {
					t.Fatalf("expected the held inv to still be queued on the incoming route, got an empty route")
				}
				incomingRoute.Close()
			}

			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatalf("HandleRelayedTransactions did not return")
			}
		})
	}
}
