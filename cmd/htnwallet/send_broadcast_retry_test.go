package main

import (
	"context"
	"net"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/daemon/pb"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/keys"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/libhtnwallet"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/txscript"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/utxo"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/config"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// failingBroadcastDaemon hands out one signable transaction per request and fails every broadcast, the way a
// broadcast that timed out after the node already accepted the transaction looks to the CLI.
type failingBroadcastDaemon struct {
	pb.UnimplementedHtnwalletdServer
	unsignedTransaction []byte
	creates             atomic.Int32
	broadcasts          atomic.Int32
}

func (d *failingBroadcastDaemon) CreateUnsignedTransactions(context.Context, *pb.CreateUnsignedTransactionsRequest) (
	*pb.CreateUnsignedTransactionsResponse, error,
) {
	d.creates.Add(1)
	return &pb.CreateUnsignedTransactionsResponse{UnsignedTransactions: [][]byte{d.unsignedTransaction}}, nil
}

func (d *failingBroadcastDaemon) Broadcast(context.Context, *pb.BroadcastRequest) (*pb.BroadcastResponse, error) {
	d.broadcasts.Add(1)
	return nil, status.Error(codes.DeadlineExceeded, "broadcast timed out")
}

// TestSendDoesNotRebuildAfterBroadcastError pins that a failed broadcast ends the send. A broadcast error does
// not mean nothing was sent - the payment may already be in the node's mempool - and send used to request new
// transactions from the daemon and broadcast those, which spend other coins and pay the recipient again.
func TestSendDoesNotRebuildAfterBroadcastError(t *testing.T) {
	params := &dagconfig.MainnetParams
	const password = "password"
	mnemonic, err := libhtnwallet.CreateMnemonic()
	if err != nil {
		t.Fatalf("CreateMnemonic: %+v", err)
	}
	keysFile, err := keys.NewFileFromMnemonic(params, mnemonic, password)
	if err != nil {
		t.Fatalf("NewFileFromMnemonic: %+v", err)
	}
	keysFilePath := filepath.Join(t.TempDir(), "keys.json")
	if err := keysFile.SetPath(params, keysFilePath, true); err != nil {
		t.Fatalf("SetPath: %+v", err)
	}
	if err := keysFile.Save(); err != nil {
		t.Fatalf("Save: %+v", err)
	}

	const derivationPath = "m/0/0"
	address, err := libhtnwallet.Address(params, keysFile.ExtendedPublicKeys, keysFile.MinimumSignatures, derivationPath, false)
	if err != nil {
		t.Fatalf("Address: %+v", err)
	}
	scriptPublicKey, err := txscript.PayToAddrScript(address)
	if err != nil {
		t.Fatalf("PayToAddrScript: %+v", err)
	}
	unsignedTransaction, err := libhtnwallet.CreateUnsignedTransaction(keysFile.ExtendedPublicKeys, keysFile.MinimumSignatures,
		[]*libhtnwallet.Payment{{Address: address, Amount: 100_000}},
		[]*libhtnwallet.UTXO{{
			Outpoint: &externalapi.DomainOutpoint{
				TransactionID: *externalapi.NewDomainTransactionIDFromByteArray(&[externalapi.DomainHashSize]byte{1}),
			},
			UTXOEntry:      utxo.NewUTXOEntry(200_000, scriptPublicKey, false, 1),
			DerivationPath: derivationPath,
		}}, nil)
	if err != nil {
		t.Fatalf("CreateUnsignedTransaction: %+v", err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %+v", err)
	}
	daemon := &failingBroadcastDaemon{unsignedTransaction: unsignedTransaction}
	grpcServer := grpc.NewServer()
	pb.RegisterHtnwalletdServer(grpcServer, daemon)
	go func() { _ = grpcServer.Serve(listener) }()
	defer grpcServer.Stop()

	conf := &sendConfig{
		KeysFile:      keysFilePath,
		Password:      password,
		DaemonAddress: listener.Addr().String(),
		ToAddress:     address.String(),
		SendAmount:    "0.001",
		NetworkFlags:  config.NetworkFlags{ActiveNetParams: params},
	}
	err = send(conf)
	if err == nil {
		t.Fatalf("expected send to fail when the broadcast fails")
	}
	if creates := daemon.creates.Load(); creates != 1 {
		t.Fatalf("send requested transactions %d times after a failed broadcast, want 1 (broadcasts: %d)",
			creates, daemon.broadcasts.Load())
	}
}
