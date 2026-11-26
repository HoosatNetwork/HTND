package main

import (
	"context"
	"strings"
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
)

type signingTestClient struct {
	pb.HtnwalletdClient
	unsignedTransaction []byte
	creates             int
	broadcasts          int
}

func (c *signingTestClient) CreateUnsignedTransactions(context.Context, *pb.CreateUnsignedTransactionsRequest, ...grpc.CallOption) (*pb.CreateUnsignedTransactionsResponse, error) {
	c.creates++
	return &pb.CreateUnsignedTransactionsResponse{UnsignedTransactions: [][]byte{c.unsignedTransaction}}, nil
}

func (c *signingTestClient) Broadcast(context.Context, *pb.BroadcastRequest, ...grpc.CallOption) (*pb.BroadcastResponse, error) {
	c.broadcasts++
	return &pb.BroadcastResponse{}, nil
}

// TestSendDoesNotRebuildAfterSignError pins that send stops when it cannot sign what the daemon built
// - the keys file is not the daemon's. It used to ask the daemon for the transaction again, ten more
// times, two seconds apart: the same keys failed on each, and each transaction the daemon built took
// a new change address.
func TestSendDoesNotRebuildAfterSignError(t *testing.T) {
	params := &dagconfig.MainnetParams
	const password = "password"
	newKeysFile := func() *keys.File {
		mnemonic, err := libhtnwallet.CreateMnemonic()
		if err != nil {
			t.Fatalf("CreateMnemonic: %+v", err)
		}
		keysFile, err := keys.NewFileFromMnemonic(params, mnemonic, password)
		if err != nil {
			t.Fatalf("NewFileFromMnemonic: %+v", err)
		}
		return keysFile
	}
	daemonKeysFile, otherKeysFile := newKeysFile(), newKeysFile()

	// What a daemon running on daemonKeysFile builds.
	const derivationPath = "m/0/0"
	address, err := libhtnwallet.Address(params, daemonKeysFile.ExtendedPublicKeys, daemonKeysFile.MinimumSignatures,
		derivationPath, false)
	if err != nil {
		t.Fatalf("Address: %+v", err)
	}
	scriptPublicKey, err := txscript.PayToAddrScript(address)
	if err != nil {
		t.Fatalf("PayToAddrScript: %+v", err)
	}
	unsignedTransaction, err := libhtnwallet.CreateUnsignedTransaction(daemonKeysFile.ExtendedPublicKeys,
		daemonKeysFile.MinimumSignatures, []*libhtnwallet.Payment{{Address: address, Amount: 100_000}},
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

	daemon := &signingTestClient{unsignedTransaction: unsignedTransaction}
	err = sendWithClient(&sendConfig{
		Password:     password,
		ToAddress:    address.String(),
		SendAmount:   "0.001",
		NetworkFlags: config.NetworkFlags{ActiveNetParams: params},
	}, otherKeysFile, daemon)
	if err == nil || !strings.Contains(err.Error(), "Public key doesn't match any of the transaction public keys") {
		t.Fatalf("send returned %v, want the original signing error", err)
	}
	if daemon.creates != 1 {
		t.Errorf("send requested %d builds after the signing error, want 1", daemon.creates)
	}
	if daemon.broadcasts != 0 {
		t.Errorf("send broadcast %d times without a signature", daemon.broadcasts)
	}
}
