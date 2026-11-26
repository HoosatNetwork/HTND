package main

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/daemon/pb"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/libhtnwallet"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/txscript"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/utxo"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/config"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

type autoCompoundTestClient struct {
	pb.HtnwalletdClient
	t           *testing.T
	transaction []byte
	trailers    metadata.MD
}

func (c *autoCompoundTestClient) CreateUnsignedCompoundTransaction(_ context.Context,
	_ *pb.CreateUnsignedCompoundTransactionRequest, opts ...grpc.CallOption,
) (*pb.CreateUnsignedCompoundTransactionResponse, error) {
	for _, opt := range opts {
		if trailer, ok := opt.(grpc.TrailerCallOption); ok {
			*trailer.TrailerAddr = c.trailers
		}
	}
	return &pb.CreateUnsignedCompoundTransactionResponse{UnsignedTransactions: [][]byte{c.transaction}}, nil
}

func (c *autoCompoundTestClient) Broadcast(_ context.Context, request *pb.BroadcastRequest,
	_ ...grpc.CallOption,
) (*pb.BroadcastResponse, error) {
	if len(request.Transactions) != 1 {
		c.t.Fatalf("broadcast got %d transactions, want one", len(request.Transactions))
	}
	if _, err := libhtnwallet.ExtractTransaction(request.Transactions[0], false); err != nil {
		c.t.Fatalf("extracting the signed compound: %v", err)
	}
	return &pb.BroadcastResponse{TxIDs: []string{"compound-id"}}, nil
}

func TestAutoCompoundSurfacesSuccessfulSkip(t *testing.T) {
	params := &dagconfig.MainnetParams
	mnemonic, err := libhtnwallet.CreateMnemonic()
	if err != nil {
		t.Fatal(err)
	}
	xpub, err := libhtnwallet.MasterPublicKeyFromMnemonic(params, mnemonic, false)
	if err != nil {
		t.Fatal(err)
	}
	const path = "m/0/0"
	address, err := libhtnwallet.Address(params, []string{xpub}, 1, path, false)
	if err != nil {
		t.Fatal(err)
	}
	script, err := txscript.PayToAddrScript(address)
	if err != nil {
		t.Fatal(err)
	}
	entry := utxo.NewUTXOEntry(1_000_000, script, false, 1)
	unsigned, err := libhtnwallet.CreateUnsignedTransaction([]string{xpub}, 1,
		[]*libhtnwallet.Payment{{Address: address, Amount: 1_980_000}},
		[]*libhtnwallet.UTXO{
			{Outpoint: &externalapi.DomainOutpoint{Index: 1}, UTXOEntry: entry, DerivationPath: path},
			{Outpoint: &externalapi.DomainOutpoint{Index: 2}, UTXOEntry: entry, DerivationPath: path},
		}, nil)
	if err != nil {
		t.Fatal(err)
	}
	conf := &autoCompoundConfig{ToAddress: address.String(), NetworkFlags: config.NetworkFlags{ActiveNetParams: params}}
	notice := "2 ML-DSA-44 coins were left out because " + address.String() + " is a classical address"
	client := &autoCompoundTestClient{t: t, transaction: unsigned, trailers: metadata.Pairs("htnwallet-compound-skip", notice)}

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	original := os.Stdout
	defer func() { os.Stdout = original }()
	os.Stdout = writer
	err = compoundOnce(conf, client, []string{mnemonic}, nil, false)
	os.Stdout = original
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(output), notice) || !strings.Contains(string(output), "txs/compound-id") {
		t.Fatalf("expected both the skip notice and the broadcast ID in %q", output)
	}
}
