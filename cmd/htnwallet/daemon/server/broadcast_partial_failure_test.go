package server

import (
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/libhtnwallet/serialization"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/subnetworks"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter/server/grpcserver/protowire"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/rpcclient"
	"github.com/HoosatNetwork/HTND/v2/version"
	"google.golang.org/grpc"
)

// acceptFirstSubmissionNode is a node RPC server that accepts the first SubmitTransaction and rejects every later one.
type acceptFirstSubmissionNode struct {
	protowire.UnimplementedRPCServer

	mu          sync.Mutex
	submissions int
	ids         []string
}

func (n *acceptFirstSubmissionNode) MessageStream(stream protowire.RPC_MessageStreamServer) error {
	for {
		request, err := stream.Recv()
		if err != nil {
			return nil
		}
		appMessage, err := request.ToAppMessage()
		if err != nil {
			return err
		}
		var response appmessage.Message
		switch appMessage.(type) {
		case *appmessage.GetInfoRequestMessage:
			response = appmessage.NewGetInfoResponseMessage("", 0, version.Version(), false, true, true, 0, 0, "")
		case *appmessage.SubmitTransactionRequestMessage:
			n.mu.Lock()
			id := n.ids[n.submissions]
			n.submissions++
			accepted := n.submissions == 1
			n.mu.Unlock()
			submitResponse := appmessage.NewSubmitTransactionResponseMessage(id)
			if !accepted {
				submitResponse.Error = appmessage.RPCErrorf("Rejected transaction %s: test rejection", id)
			}
			response = submitResponse
		default:
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

func broadcastTestTransaction(index uint32) *externalapi.DomainTransaction {
	return &externalapi.DomainTransaction{
		Inputs: []*externalapi.DomainTransactionInput{{
			PreviousOutpoint: externalapi.DomainOutpoint{Index: index},
			SignatureScript:  []byte{},
		}},
		Outputs:      []*externalapi.DomainTransactionOutput{{Value: 1000, ScriptPublicKey: &externalapi.ScriptPublicKey{Script: []byte{0x51}}}},
		SubnetworkID: subnetworks.SubnetworkIDNative,
		Payload:      []byte{},
	}
}

// TestBroadcastFailureNamesTheTransactionsAlreadySubmitted pins that when one transaction of a broadcast request is
// rejected, the error names the transactions of that request the node already accepted. The daemon submits them one
// by one and used to return only the rejection, so the IDs of payments that were already in the node's mempool were
// lost to the caller, which could not tell what had gone out.
func TestBroadcastFailureNamesTheTransactionsAlreadySubmitted(t *testing.T) {
	first, second := broadcastTestTransaction(1), broadcastTestTransaction(2)
	firstID := consensushashing.TransactionID(first).String()
	secondID := consensushashing.TransactionID(second).String()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %+v", err)
	}
	grpcServer := grpc.NewServer()
	protowire.RegisterRPCServer(grpcServer, &acceptFirstSubmissionNode{ids: []string{firstID, secondID}})
	go func() { _ = grpcServer.Serve(listener) }()
	t.Cleanup(grpcServer.Stop)

	client, err := rpcclient.NewRPCClient(listener.Addr().String())
	if err != nil {
		t.Fatalf("NewRPCClient: %+v", err)
	}
	defer client.Close()
	client.SetTimeout(5 * time.Second)

	var transactions [][]byte
	for _, transaction := range []*externalapi.DomainTransaction{first, second} {
		serialized, err := serialization.SerializeDomainTransaction(transaction)
		if err != nil {
			t.Fatalf("SerializeDomainTransaction: %+v", err)
		}
		transactions = append(transactions, serialized)
	}

	walletServer := &server{rpcClient: client, usedOutpoints: map[externalapi.DomainOutpoint]time.Time{}}
	_, err = walletServer.broadcast(transactions, true, false, nil)
	if err == nil {
		t.Fatalf("expected the rejected second transaction to fail the broadcast")
	}
	if !strings.Contains(err.Error(), "Rejected transaction") {
		t.Fatalf("the node's rejection is missing from the error: %s", err)
	}
	if !strings.Contains(err.Error(), firstID) {
		t.Fatalf("the error does not name transaction %s, which the node already accepted: %s", firstID, err)
	}
	if _, reserved := walletServer.usedOutpoints[first.Inputs[0].PreviousOutpoint]; !reserved {
		t.Fatalf("the accepted transaction's input is no longer reserved")
	}
}
