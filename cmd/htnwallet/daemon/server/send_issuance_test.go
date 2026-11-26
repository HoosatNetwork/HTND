package server

import (
	"context"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/daemon/pb"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/keys"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/libhtnwallet"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/libhtnwallet/serialization"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/txscript"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter/router"
	"github.com/HoosatNetwork/HTND/v2/util"
)

// sendTestNode supplies UTXOs and captures submissions without opening a socket.
type sendTestNode struct {
	walletBackgroundNode
	entries []*appmessage.UTXOsByAddressesEntry
	submit  func(*appmessage.RPCTransaction, string) (*appmessage.SubmitTransactionResponseMessage, error)
}

func (n *sendTestNode) Reconnect() error { return nil }

func (n *sendTestNode) GetBlockDAGInfo() (*appmessage.GetBlockDAGInfoResponseMessage, error) {
	return &appmessage.GetBlockDAGInfoResponseMessage{VirtualDAAScore: feeTestVirtualDAAScore}, nil
}

func (n *sendTestNode) GetMempoolEntriesByAddresses([]string, bool, bool) (*appmessage.GetMempoolEntriesByAddressesResponseMessage, error) {
	return &appmessage.GetMempoolEntriesByAddressesResponseMessage{}, nil
}

func (n *sendTestNode) GetUTXOsByAddresses([]string, uint32) (*appmessage.GetUTXOsByAddressesResponseMessage, error) {
	return &appmessage.GetUTXOsByAddressesResponseMessage{Entries: n.entries}, nil
}

func (n *sendTestNode) SubmitTransactionWithPriority(tx *appmessage.RPCTransaction, id string, _ bool, _ *bool) (*appmessage.SubmitTransactionResponseMessage, error) {
	return n.submit(tx, id)
}

func attachSendTestNode(t *testing.T, w *feeWallet) *sendTestNode {
	t.Helper()
	node := &sendTestNode{}
	for _, coin := range w.s.utxosSortedByAmount {
		address := w.address(t, coin.address).String()
		w.s.addressSet[address] = coin.address
		script := coin.UTXOEntry.ScriptPublicKey()
		node.entries = append(node.entries, &appmessage.UTXOsByAddressesEntry{
			Address: address,
			Outpoint: &appmessage.RPCOutpoint{
				TransactionID: coin.Outpoint.TransactionID.String(),
				Index:         coin.Outpoint.Index,
			},
			UTXOEntry: &appmessage.RPCUTXOEntry{
				Amount: coin.UTXOEntry.Amount(),
				ScriptPublicKey: &appmessage.RPCScriptPublicKey{
					Script:  hex.EncodeToString(script.Script),
					Version: script.Version,
				},
				BlockDAAScore: coin.UTXOEntry.BlockDAAScore(),
			},
		})
	}
	w.s.rpcClient = node
	w.s.backgroundRPCClient = node
	w.s.nextSyncStartIndex = 100
	w.s.firstSyncDone.Store(true)
	w.s.usedOutpoints = make(map[externalapi.DomainOutpoint]time.Time)
	return node
}

func freshSendFixture(t *testing.T, password string) (w *feeWallet, receive, change util.Address, node *sendTestNode) {
	t.Helper()
	w = newSchnorrWallet(t)
	if password != "" {
		file, err := keys.NewFileFromMnemonic(w.params, w.mnemonics[0], password)
		if err != nil {
			t.Fatal(err)
		}
		if err := file.SetPath(w.params, w.s.keysFile.Path(), true); err != nil {
			t.Fatal(err)
		}
		w.s.keysFile = file
	}
	if err := w.s.keysFile.Save(); err != nil {
		t.Fatal(err)
	}
	coinAddress := &walletAddress{index: 1, keyChain: libhtnwallet.ExternalKeychain}
	w.fund(t, same(coinAddress, 1_000_000)...)
	receive = w.address(t, coinAddress)
	change = w.address(t, &walletAddress{index: 1, keyChain: libhtnwallet.InternalKeychain})
	return w, receive, change, attachSendTestNode(t, w)
}

func blockSendKeysSave(t *testing.T, w *feeWallet) func() {
	t.Helper()
	path := w.s.keysFile.Path()
	moved := filepath.Join(filepath.Dir(path), "original.json")
	if err := os.Rename(path, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	return func() {
		t.Helper()
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(moved, path); err != nil {
			t.Fatal(err)
		}
	}
}

func assertChangeTracked(t *testing.T, w *feeWallet, change util.Address, want bool) {
	t.Helper()
	if _, tracked := w.s.addressSet[change.String()]; tracked != want {
		t.Errorf("change address tracked = %t, want %t", tracked, want)
	}
}

func TestUnsignedSendCommitsChangeBeforeReturning(t *testing.T) {
	w, receive, change, _ := freshSendFixture(t, "")
	request := &pb.CreateUnsignedTransactionsRequest{Address: receive.String(), Amount: 500_000}
	restore := blockSendKeysSave(t, w)
	reply, err := w.s.CreateUnsignedTransactions(context.Background(), request)
	if err == nil || reply != nil {
		t.Errorf("unsigned handler returned %d transactions and error %v with the keys path blocked", len(reply.GetUnsignedTransactions()), err)
	}
	restore()
	assertInternalIndex(t, w, 0)
	assertChangeTracked(t, w, change, false)

	reply, err = w.s.CreateUnsignedTransactions(context.Background(), request)
	if err != nil || len(reply.GetUnsignedTransactions()) != 1 {
		t.Fatalf("unsigned handler returned %d transactions and error %v, want one transaction", len(reply.GetUnsignedTransactions()), err)
	}
	tx, err := serialization.DeserializePartiallySignedTransaction(reply.UnsignedTransactions[0])
	if err != nil {
		t.Fatal(err)
	}
	script, err := txscript.PayToAddrScript(change)
	if err != nil {
		t.Fatal(err)
	}
	if len(tx.Tx.Outputs) != 2 || !tx.Tx.Outputs[1].ScriptPublicKey.Equal(script) {
		t.Error("unsigned transaction is missing its expected change output")
	}
	assertInternalIndex(t, w, 1)
	assertChangeTracked(t, w, change, true)
}

func TestSignedSendCommitsChangeBeforeSubmitting(t *testing.T) {
	const password = "test signing password"
	for _, test := range []struct {
		name    string
		nodeErr error
	}{
		{"accepted", nil},
		{"lost node reply", router.ErrTimeout},
	} {
		t.Run(test.name, func(t *testing.T) {
			w, receive, change, node := freshSendFixture(t, password)
			request := &pb.SendRequest{ToAddress: receive.String(), Amount: 500_000, Password: password}
			submissions := 0
			restore := blockSendKeysSave(t, w)
			node.submit = func(*appmessage.RPCTransaction, string) (*appmessage.SubmitTransactionResponseMessage, error) {
				submissions++
				return nil, errors.New("unexpected submission with the keys path blocked")
			}
			reply, err := w.s.Send(context.Background(), request)
			if err == nil || reply != nil || submissions != 0 {
				t.Errorf("signed handler returned %v, error %v, and made %d submissions with the keys path blocked", reply, err, submissions)
			}
			restore()
			assertInternalIndex(t, w, 0)
			assertChangeTracked(t, w, change, false)

			changeScript, err := txscript.PayToAddrScript(change)
			if err != nil {
				t.Fatal(err)
			}
			var submittedID string
			node.submit = func(tx *appmessage.RPCTransaction, id string) (*appmessage.SubmitTransactionResponseMessage, error) {
				submissions++
				submittedID = id
				assertInternalIndex(t, w, 1)
				assertChangeTracked(t, w, change, true)
				if len(tx.Outputs) != 2 ||
					tx.Outputs[1].ScriptPublicKey.Script != hex.EncodeToString(changeScript.Script) ||
					tx.Outputs[1].ScriptPublicKey.Version != changeScript.Version {
					t.Error("submission is missing its expected change output")
				}
				if len(tx.Inputs) != 1 || tx.Inputs[0].SignatureScript == "" {
					t.Error("submission has no signed input")
				}
				if test.nodeErr != nil {
					return nil, test.nodeErr
				}
				return appmessage.NewSubmitTransactionResponseMessage(id), nil
			}
			reply, err = w.s.Send(context.Background(), request)
			if test.nodeErr != nil {
				if !errors.Is(err, test.nodeErr) || reply != nil {
					t.Errorf("signed handler returned %v, %v, want the node reply error", reply, err)
				}
			} else if err != nil || len(reply.GetTxIDs()) != 1 || reply.TxIDs[0] != submittedID || len(reply.SignedTransactions) != 1 {
				t.Errorf("signed handler returned %v, %v, want the submitted transaction", reply, err)
			}
			if submissions != 1 {
				t.Errorf("made %d submissions, want one", submissions)
			}
			assertInternalIndex(t, w, 1)
			assertChangeTracked(t, w, change, true)
		})
	}
}
