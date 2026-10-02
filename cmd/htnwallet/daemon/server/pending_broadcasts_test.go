package server

import (
	"net"
	"sync"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter/server/grpcserver/protowire"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/rpcclient"
	"github.com/HoosatNetwork/HTND/v2/version"
	"google.golang.org/grpc"
)

// pendingBroadcastNode is a node RPC server whose mempool either holds one transaction or not, and that
// answers a SubmitTransaction with submitRejection, or accepts it when that is empty.
type pendingBroadcastNode struct {
	protowire.UnimplementedRPCServer

	mu              sync.Mutex
	inMempool       bool
	submitRejection string
	mempoolQueries  int
	submissions     int
}

func (n *pendingBroadcastNode) MessageStream(stream protowire.RPC_MessageStreamServer) error {
	for {
		request, err := stream.Recv()
		if err != nil {
			return nil
		}
		appMessage, err := request.ToAppMessage()
		if err != nil {
			return err
		}
		n.mu.Lock()
		var response appmessage.Message
		switch message := appMessage.(type) {
		case *appmessage.GetInfoRequestMessage:
			response = appmessage.NewGetInfoResponseMessage("", 0, version.Version(), false, true, true, 0, 0, "")
		case *appmessage.GetMempoolEntryRequestMessage:
			n.mempoolQueries++
			entryResponse := &appmessage.GetMempoolEntryResponseMessage{}
			if n.inMempool {
				entryResponse.Entry = &appmessage.MempoolEntry{Transaction: &appmessage.RPCTransaction{}}
			} else {
				entryResponse.Error = appmessage.RPCErrorf("Transaction %s was not found", message.TxID)
			}
			response = entryResponse
		case *appmessage.SubmitTransactionRequestMessage:
			n.submissions++
			id := consensushashing.TransactionID(mustDomainTransaction(message.Transaction)).String()
			submitResponse := appmessage.NewSubmitTransactionResponseMessage(id)
			if n.submitRejection != "" {
				submitResponse.Error = appmessage.RPCErrorf("Rejected transaction %s: %s", id, n.submitRejection)
			} else {
				n.inMempool = true
			}
			response = submitResponse
		}
		n.mu.Unlock()
		if response == nil {
			continue
		}
		protoResponse, err := protowire.FromAppMessage(response)
		if err != nil {
			return err
		}
		if err := stream.Send(protoResponse); err != nil {
			return nil
		}
	}
}

func mustDomainTransaction(transaction *appmessage.RPCTransaction) *externalapi.DomainTransaction {
	domainTransaction, err := appmessage.RPCTransactionToDomainTransaction(transaction)
	if err != nil {
		panic(err)
	}
	return domainTransaction
}

// newPendingBroadcastTestServer returns a wallet server talking to node, with transaction tracked as
// broadcast at broadcastTime and its input reserved.
func newPendingBroadcastTestServer(t *testing.T, node *pendingBroadcastNode,
	transaction *externalapi.DomainTransaction, broadcastTime time.Time,
) *server {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %+v", err)
	}
	grpcServer := grpc.NewServer()
	protowire.RegisterRPCServer(grpcServer, node)
	go func() { _ = grpcServer.Serve(listener) }()
	t.Cleanup(grpcServer.Stop)

	client, err := rpcclient.NewRPCClient(listener.Addr().String())
	if err != nil {
		t.Fatalf("NewRPCClient: %+v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	client.SetTimeout(5 * time.Second)

	walletServer := &server{
		backgroundRPCClient: client,
		usedOutpoints:       map[externalapi.DomainOutpoint]time.Time{},
	}
	walletServer.usedOutpoints[transaction.Inputs[0].PreviousOutpoint] = broadcastTime
	walletServer.trackBroadcast(transaction, broadcastTime)
	return walletServer
}

func (s *server) isTrackingBroadcast(transaction *externalapi.DomainTransaction) bool {
	_, ok := s.pendingBroadcasts[*consensushashing.TransactionID(transaction)]
	return ok
}

func (s *server) reservesInput(transaction *externalapi.DomainTransaction) bool {
	_, ok := s.usedOutpoints[transaction.Inputs[0].PreviousOutpoint]
	return ok
}

// TestPendingBroadcastIsLeftAloneWithinGracePeriod pins that a fresh transaction is not checked on: a
// block carrying it may not be merged yet, and resubmitting it then would put a mined transaction back
// into the mempool.
func TestPendingBroadcastIsLeftAloneWithinGracePeriod(t *testing.T) {
	transaction := broadcastTestTransaction(1)
	now := time.Now()
	node := &pendingBroadcastNode{}
	walletServer := newPendingBroadcastTestServer(t, node, transaction, now)

	walletServer.checkPendingBroadcasts(now.Add(pendingBroadcastGracePeriod / 2))

	mempoolQueries, submissions, _ := node.counts()
	if mempoolQueries != 0 || submissions != 0 {
		t.Fatalf("the node was asked %d times and sent %d submissions within the grace period",
			mempoolQueries, submissions)
	}
	if !walletServer.isTrackingBroadcast(transaction) || !walletServer.reservesInput(transaction) {
		t.Fatalf("the transaction stopped being tracked within the grace period")
	}
}

// TestPendingBroadcastInMempoolIsNotResubmitted pins that a transaction the node still holds is left
// alone, and checked on again only after pendingBroadcastCheckInterval.
func TestPendingBroadcastInMempoolIsNotResubmitted(t *testing.T) {
	transaction := broadcastTestTransaction(1)
	broadcastTime := time.Now()
	node := &pendingBroadcastNode{inMempool: true}
	walletServer := newPendingBroadcastTestServer(t, node, transaction, broadcastTime)

	checkTime := broadcastTime.Add(pendingBroadcastGracePeriod)
	walletServer.checkPendingBroadcasts(checkTime)
	walletServer.checkPendingBroadcasts(checkTime.Add(pendingBroadcastCheckInterval / 2))

	mempoolQueries, submissions, _ := node.counts()
	if mempoolQueries != 1 || submissions != 0 {
		t.Fatalf("got %d mempool queries and %d submissions, want 1 and 0", mempoolQueries, submissions)
	}
	if !walletServer.isTrackingBroadcast(transaction) || !walletServer.reservesInput(transaction) {
		t.Fatalf("a transaction still in the mempool stopped being tracked")
	}
}

// TestLostBroadcastIsResubmitted pins the stranded case: the node dropped the transaction when a block
// carrying it arrived, and that block was never merged. The node still accepts the transaction, so the
// wallet sends it again rather than waiting for the used-outpoint expiry.
func TestLostBroadcastIsResubmitted(t *testing.T) {
	transaction := broadcastTestTransaction(1)
	broadcastTime := time.Now()
	node := &pendingBroadcastNode{}
	walletServer := newPendingBroadcastTestServer(t, node, transaction, broadcastTime)

	walletServer.checkPendingBroadcasts(broadcastTime.Add(pendingBroadcastGracePeriod))

	_, submissions, inMempool := node.counts()
	if submissions != 1 || !inMempool {
		t.Fatalf("the lost transaction was not resubmitted (%d submissions)", submissions)
	}
	if !walletServer.isTrackingBroadcast(transaction) || !walletServer.reservesInput(transaction) {
		t.Fatalf("a resubmitted transaction stopped being tracked")
	}
}

// TestSettledBroadcastReleasesItsInputs pins that a transaction the node no longer holds and refuses -
// because it was accepted, or because its input vanished - stops being tracked and frees its input at
// once, rather than an hour later.
func TestSettledBroadcastReleasesItsInputs(t *testing.T) {
	transaction := broadcastTestTransaction(1)
	broadcastTime := time.Now()
	node := &pendingBroadcastNode{submitRejection: "transaction is an orphan where orphan is disallowed"}
	walletServer := newPendingBroadcastTestServer(t, node, transaction, broadcastTime)

	walletServer.checkPendingBroadcasts(broadcastTime.Add(pendingBroadcastGracePeriod))

	if walletServer.isTrackingBroadcast(transaction) {
		t.Fatalf("a refused transaction is still tracked")
	}
	if walletServer.reservesInput(transaction) {
		t.Fatalf("a refused transaction still reserves its input")
	}
}

// TestTransientRefusalKeepsBroadcastPending pins that refusals that say nothing about the transaction's
// fate - it raced back into the mempool, or the compound rate limit - keep it tracked.
func TestTransientRefusalKeepsBroadcastPending(t *testing.T) {
	for _, rejection := range []string{
		"transaction 00 is already in the mempool",
		"compound transaction rate limit exceeded",
	} {
		transaction := broadcastTestTransaction(1)
		broadcastTime := time.Now()
		node := &pendingBroadcastNode{submitRejection: rejection}
		walletServer := newPendingBroadcastTestServer(t, node, transaction, broadcastTime)

		walletServer.checkPendingBroadcasts(broadcastTime.Add(pendingBroadcastGracePeriod))

		if !walletServer.isTrackingBroadcast(transaction) || !walletServer.reservesInput(transaction) {
			t.Fatalf("refusal %q settled the transaction", rejection)
		}
	}
}

// TestBroadcastIsDroppedAfterMaxAge pins that tracking ends without asking the node once the used-outpoint
// expiry has taken over.
func TestBroadcastIsDroppedAfterMaxAge(t *testing.T) {
	transaction := broadcastTestTransaction(1)
	broadcastTime := time.Now()
	node := &pendingBroadcastNode{}
	walletServer := newPendingBroadcastTestServer(t, node, transaction, broadcastTime)

	walletServer.checkPendingBroadcasts(broadcastTime.Add(pendingBroadcastMaxAge + time.Second))

	mempoolQueries, _, _ := node.counts()
	if mempoolQueries != 0 || walletServer.isTrackingBroadcast(transaction) {
		t.Fatalf("an expired transaction is still checked on (%d queries)", mempoolQueries)
	}
}

func (n *pendingBroadcastNode) counts() (mempoolQueries int, submissions int, inMempool bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.mempoolQueries, n.submissions, n.inMempool
}
