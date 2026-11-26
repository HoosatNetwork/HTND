package server

import (
	"encoding/hex"
	"fmt"
	"net"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/daemon/pb"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/libhtnwallet"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/libhtnwallet/serialization"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/txscript"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter/server/grpcserver/protowire"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/rpcclient"
	"github.com/HoosatNetwork/HTND/v2/util"
	"github.com/HoosatNetwork/HTND/v2/version"
	"google.golang.org/grpc"
)

// TestMLDSA44CoinsWaitForActivation pins that the wallet does not spend, or count as available, a
// coin on an ML-DSA-44 address before ML-DSA-44 is active.
//
// Such a coin cannot be spent by any block until activation. The wallet selects its coins by amount,
// so a send that selected a large ML-DSA-44 coin used to be refused by the node. A retry could select
// the same coin again, while the balance still showed it as available.
func TestMLDSA44CoinsWaitForActivation(t *testing.T) {
	const beforeActivation, afterActivation = uint64(1 + inputMinAgeDAAScore + 100), uint64(feeTestVirtualDAAScore)
	schnorr := &walletAddress{index: 1, keyChain: libhtnwallet.ExternalKeychain}
	ml := &walletAddress{index: 2, keyChain: libhtnwallet.ExternalKeychain, mldsa44: true}
	w := newMLWallet(t, 1, 1) // mainnet
	if libhtnwallet.MLDSA44Active(w.params, beforeActivation) || !libhtnwallet.MLDSA44Active(w.params, afterActivation) {
		t.Fatalf("the test's DAA scores are not on both sides of the ML-DSA-44 activation of %s", w.params.Name)
	}
	// The ML-DSA-44 coin is the wallet's largest.
	w.fund(t, coinSpec{ml, 500_000_000}, coinSpec{schnorr, 300_000_000})
	destination := w.address(t, &walletAddress{index: 3, keyChain: libhtnwallet.ExternalKeychain})
	w.s.addressSet[w.address(t, ml).String()] = ml
	send := func(amount uint64, from []string, virtualDAAScore uint64) (sentTransactionInputs, error) {
		transactions, err := w.s.buildSendTransaction(destination, amount, false, from, true, nil, virtualDAAScore)
		if err != nil {
			return sentTransactionInputs{}, err
		}
		return countInputs(t, [][]byte{transactions.transaction}), nil
	}

	// Before activation: the secp256k1 coin pays what it can, and the ML-DSA-44 coin is out of reach.
	inputs, err := send(200_000_000, nil, beforeActivation)
	if err != nil || inputs.mldsa44 != 0 || inputs.other != 1 {
		t.Fatalf("before activation, a send the secp256k1 coin covers spends %+v (err %v); want that coin alone", inputs, err)
	}
	if _, err := send(400_000_000, nil, beforeActivation); err == nil || !strings.Contains(err.Error(), "Insufficient funds") {
		t.Fatalf("before activation, a send that needs the ML-DSA-44 coin got %v, want insufficient funds", err)
	}
	if _, err := send(100_000_000, []string{w.address(t, ml).String()}, beforeActivation); err == nil {
		t.Fatalf("before activation, a send from the ML-DSA-44 address was built")
	}

	// After activation the same sends use the ML-DSA-44 coin.
	inputs, err = send(400_000_000, nil, afterActivation)
	if err != nil || inputs.mldsa44 != 1 {
		t.Fatalf("after activation, a send that needs the ML-DSA-44 coin spends %+v (err %v)", inputs, err)
	}
	if inputs, err = send(100_000_000, []string{w.address(t, ml).String()}, afterActivation); err != nil || inputs.mldsa44 != 1 || inputs.other != 0 {
		t.Fatalf("after activation, a send from the ML-DSA-44 address spends %+v (err %v)", inputs, err)
	}

	// The balance: the ML-DSA-44 coin is pending before activation, available after it.
	mlAddress, schnorrAddress := w.address(t, ml).String(), w.address(t, schnorr).String()
	w.s.addressSet = walletAddressSet{mlAddress: ml, schnorrAddress: schnorr}
	entries := []*appmessage.UTXOsByAddressesEntry{
		{Address: mlAddress, UTXOEntry: &appmessage.RPCUTXOEntry{
			Amount: 500_000_000, BlockDAAScore: 1, ScriptPublicKey: &appmessage.RPCScriptPublicKey{},
		}},
		{Address: schnorrAddress, UTXOEntry: &appmessage.RPCUTXOEntry{
			Amount: 300_000_000, BlockDAAScore: 1, ScriptPublicKey: &appmessage.RPCScriptPublicKey{},
		}},
	}
	for _, test := range []struct {
		virtualDAAScore            uint64
		wantAvailable, wantPending uint64
	}{
		{beforeActivation, 0, 500_000_000},
		{afterActivation, 500_000_000, 0},
	} {
		balances := balancesMapType{mlAddress: {available: 500_000_000}, schnorrAddress: {available: 300_000_000}}
		if err := w.s.movePendingOutOfAvailable(balances, entries, test.virtualDAAScore); err != nil {
			t.Fatalf("movePendingOutOfAvailable: %+v", err)
		}
		if got := *balances[mlAddress]; got.available != test.wantAvailable || got.pending != test.wantPending {
			t.Errorf("at DAA %d the ML-DSA-44 coin is %d available and %d pending, want %d and %d",
				test.virtualDAAScore, got.available, got.pending, test.wantAvailable, test.wantPending)
		}
		if got := *balances[schnorrAddress]; got.available != 300_000_000 || got.pending != 0 {
			t.Errorf("at DAA %d the secp256k1 coin is %d available and %d pending", test.virtualDAAScore, got.available, got.pending)
		}
	}
}

// sentTransactionInputs counts a built transaction's inputs by kind.
type sentTransactionInputs struct{ mldsa44, other int }

func countInputs(t *testing.T, transactions [][]byte) sentTransactionInputs {
	t.Helper()
	if len(transactions) != 1 {
		t.Fatalf("got %d transactions, want 1", len(transactions))
	}
	partiallySigned, err := serialization.DeserializePartiallySignedTransaction(transactions[0])
	if err != nil {
		t.Fatalf("DeserializePartiallySignedTransaction: %+v", err)
	}
	var inputs sentTransactionInputs
	for _, input := range partiallySigned.PartiallySignedInputs {
		if libhtnwallet.IsMLDSA44Input(input) {
			inputs.mldsa44++
		} else {
			inputs.other++
		}
	}
	return inputs
}

// coinNode is a node RPC server that holds the coins in coins, by address, and reports virtualDAAScore.
type coinNode struct {
	protowire.UnimplementedRPCServer
	coins           []*appmessage.UTXOsByAddressesEntry
	balances        []*appmessage.BalancesByAddressesEntry
	virtualDAAScore atomic.Uint64
}

func (n *coinNode) MessageStream(stream protowire.RPC_MessageStreamServer) error {
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
		switch message := appMessage.(type) {
		case *appmessage.GetInfoRequestMessage:
			response = appmessage.NewGetInfoResponseMessage("", 0, version.Version(), false, true, true, 0, 0, "")
		case *appmessage.GetBlockDAGInfoRequestMessage:
			response = &appmessage.GetBlockDAGInfoResponseMessage{VirtualDAAScore: n.virtualDAAScore.Load()}
		case *appmessage.GetMempoolEntriesByAddressesRequestMessage:
			response = appmessage.NewGetMempoolEntriesByAddressesResponseMessage(nil)
		case *appmessage.GetBalancesByAddressesRequestMessage:
			var entries []*appmessage.BalancesByAddressesEntry
			for _, balance := range n.balances {
				if slices.Contains(message.Addresses, balance.Address) {
					entries = append(entries, balance)
				}
			}
			response = appmessage.NewGetBalancesByAddressesResponse(entries)
		case *appmessage.GetUTXOsByAddressesRequestMessage:
			var entries []*appmessage.UTXOsByAddressesEntry
			for _, coin := range n.coins {
				if slices.Contains(message.Addresses, coin.Address) {
					entries = append(entries, coin)
				}
			}
			response = appmessage.NewGetUTXOsByAddressesResponseMessage(entries)
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

// TestCompoundLeavesMLDSA44CoinsUntilActivation runs a compound as the daemon does - coins from the
// node, the DAA score from the node - and pins that it takes no coin of an ML-DSA-44 address before
// ML-DSA-44 is active, and takes them once it is. A compound takes the wallet's smallest coins and its
// largest, so a compound holding such a coin was refused by the node, every time.
func TestCompoundLeavesMLDSA44CoinsUntilActivation(t *testing.T) {
	const beforeActivation, afterActivation = uint64(1 + inputMinAgeDAAScore + 100), uint64(feeTestVirtualDAAScore)
	schnorr := &walletAddress{index: 1, keyChain: libhtnwallet.ExternalKeychain}
	ml := &walletAddress{index: 2, keyChain: libhtnwallet.ExternalKeychain, mldsa44: true}
	w := newMLWallet(t, 1, 1) // mainnet
	s := w.s
	s.usedOutpoints = map[externalapi.DomainOutpoint]time.Time{}
	s.nextSyncStartIndex = 1000
	s.firstSyncDone.Store(true)

	// The node's coins: the largest and the smallest are on the ML-DSA-44 address.
	node := &coinNode{}
	for i, coin := range []coinSpec{
		{ml, 600_000_000}, {ml, 1_000_000}, {schnorr, 300_000_000}, {schnorr, 2_000_000}, {schnorr, 3_000_000},
	} {
		address := w.address(t, coin.walletAddr)
		s.addressSet[address.String()] = coin.walletAddr
		node.coins = append(node.coins, testNodeCoin(t, address, byte(i+1), coin.value, 1))
	}
	connectCoinNode(t, s, node)

	// The compound pays an ML-DSA-44 address, so nothing but the activation keeps ML-DSA-44 coins out.
	destination := w.address(t, &walletAddress{index: 1, keyChain: libhtnwallet.ExternalKeychain, mldsa44: true}).String()
	compound := func(virtualDAAScore uint64) sentTransactionInputs {
		t.Helper()
		node.virtualDAAScore.Store(virtualDAAScore)
		s.usedOutpoints = map[externalapi.DomainOutpoint]time.Time{}
		transactions, err := s.createUnsignedCompoundTransaction(t.Context(), destination, nil, 10000)
		if err != nil {
			t.Fatalf("createUnsignedCompoundTransaction at DAA %d: %+v", virtualDAAScore, err)
		}
		return countInputs(t, transactions)
	}

	if inputs := compound(beforeActivation); inputs.mldsa44 != 0 || inputs.other != 3 {
		t.Fatalf("before activation the compound spends %+v, want the three secp256k1 coins only", inputs)
	}
	if inputs := compound(afterActivation); inputs.mldsa44 != 2 || inputs.other != 3 {
		t.Fatalf("after activation the compound spends %+v, want all five coins", inputs)
	}
}

func connectCoinNode(t *testing.T, s *server, node *coinNode) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %+v", err)
	}
	grpcServer := grpc.NewServer()
	protowire.RegisterRPCServer(grpcServer, node)
	go func() { _ = grpcServer.Serve(listener) }()
	t.Cleanup(grpcServer.Stop)
	rpc, err := rpcclient.NewRPCClient(listener.Addr().String())
	if err != nil {
		t.Fatalf("NewRPCClient: %+v", err)
	}
	t.Cleanup(func() { _ = rpc.Close() })
	rpc.SetTimeout(5 * time.Second)
	s.rpcClient = rpc
	s.backgroundRPCClient = rpc
}

func TestSendExistingChangeSkipsInactiveMLDSA44Address(t *testing.T) {
	w := newMLWallet(t, 1, 1)
	ml := &walletAddress{index: 1, keyChain: libhtnwallet.ExternalKeychain, mldsa44: true}
	schnorr := &walletAddress{index: 2, keyChain: libhtnwallet.ExternalKeychain}
	w.fund(t, coinSpec{ml, 500_000_000}, coinSpec{schnorr, 300_000_000})
	destination := w.address(t, &walletAddress{index: 3, keyChain: libhtnwallet.ExternalKeychain})
	w.s.addressSet[w.address(t, ml).String()] = ml
	w.s.addressSet[w.address(t, schnorr).String()] = schnorr
	const activation = 258_000_000
	for _, tc := range []struct {
		name   string
		score  uint64
		change *walletAddress
		inputs sentTransactionInputs
	}{
		{"before activation", activation - 1, schnorr, sentTransactionInputs{other: 1}},
		{"at activation", activation, ml, sentTransactionInputs{mldsa44: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			txs, err := w.s.buildSendTransaction(destination, 200_000_000, false,
				[]string{w.address(t, ml).String(), w.address(t, schnorr).String()}, true, nil, tc.score)
			if err != nil {
				t.Fatalf("buildSendTransaction: %v", err)
			}
			if inputs := countInputs(t, [][]byte{txs.transaction}); inputs != tc.inputs {
				t.Fatalf("inputs: got %+v, want %+v", inputs, tc.inputs)
			}
			transaction, err := serialization.DeserializePartiallySignedTransaction(txs.transaction)
			if err != nil {
				t.Fatalf("DeserializePartiallySignedTransaction: %v", err)
			}
			changeScript, err := txscript.PayToAddrScript(w.address(t, tc.change))
			if err != nil {
				t.Fatalf("PayToAddrScript: %v", err)
			}
			outputs := transaction.Tx.Outputs
			if len(outputs) != 2 {
				t.Fatalf("got %d outputs, want payment and change", len(outputs))
			}
			if got := outputs[1].ScriptPublicKey; !got.Equal(changeScript) {
				t.Fatalf("change script: got %x, want %x", got.Script, changeScript.Script)
			}
		})
	}
}

func testNodeCoin(t *testing.T, address util.Address, id byte, amount, score uint64) *appmessage.UTXOsByAddressesEntry {
	t.Helper()
	script, err := txscript.PayToAddrScript(address)
	if err != nil {
		t.Fatalf("PayToAddrScript: %v", err)
	}
	return &appmessage.UTXOsByAddressesEntry{
		Address:  address.String(),
		Outpoint: &appmessage.RPCOutpoint{TransactionID: strings.Repeat(fmt.Sprintf("%02x", id), 32)},
		UTXOEntry: &appmessage.RPCUTXOEntry{
			Amount: amount, BlockDAAScore: score,
			ScriptPublicKey: &appmessage.RPCScriptPublicKey{Version: script.Version, Script: hex.EncodeToString(script.Script)},
		},
	}
}

func TestGetBalanceCountsAllInactiveMLDSA44FundsAsPending(t *testing.T) {
	w := newMLWallet(t, 1, 1)
	s := w.s
	s.nextSyncStartIndex = 1000
	s.firstSyncDone.Store(true)
	ml := &walletAddress{index: 1, keyChain: libhtnwallet.ExternalKeychain, mldsa44: true,
		mldsa44Form: libhtnwallet.MLDSA44AddressFormP2SH}
	legacy := &walletAddress{index: 2, keyChain: libhtnwallet.ExternalKeychain}
	mlAddr, legacyAddr := w.address(t, ml), w.address(t, legacy)
	s.addressSet[mlAddr.String()], s.addressSet[legacyAddr.String()] = ml, legacy
	const oneHTN, activation = 100_000_000, 258_000_000
	node := &coinNode{
		balances: []*appmessage.BalancesByAddressesEntry{
			{Address: mlAddr.String(), Balance: 3 * oneHTN},
			{Address: legacyAddr.String(), Balance: 3 * oneHTN},
		},
		coins: []*appmessage.UTXOsByAddressesEntry{
			// The node reports a total of 3 HTN but caps the ML-DSA-44 UTXO reply at its first coin.
			testNodeCoin(t, mlAddr, 1, oneHTN, 1),
			testNodeCoin(t, legacyAddr, 2, oneHTN, 1),
			testNodeCoin(t, legacyAddr, 3, 2*oneHTN, activation-2),
		},
	}
	connectCoinNode(t, s, node)

	for _, tc := range []struct {
		score, mlAvailable, mlPending uint64
	}{
		{activation - 1, 0, 3 * oneHTN},
		{activation, 3 * oneHTN, 0},
	} {
		node.virtualDAAScore.Store(tc.score)
		balance, err := s.GetBalance(t.Context(), &pb.GetBalanceRequest{})
		if err != nil {
			t.Fatalf("GetBalance at DAA %d: %v", tc.score, err)
		}
		if balance.Available != tc.mlAvailable+oneHTN || balance.Pending != tc.mlPending+2*oneHTN {
			t.Errorf("at DAA %d got total %d available, %d pending. Want %d available, %d pending",
				tc.score, balance.Available, balance.Pending, tc.mlAvailable+oneHTN, tc.mlPending+2*oneHTN)
		}
		got := make(map[string]balancesType)
		for _, entry := range balance.AddressBalances {
			got[entry.Address] = balancesType{available: entry.Available, pending: entry.Pending}
		}
		if got[mlAddr.String()] != (balancesType{tc.mlAvailable, tc.mlPending}) {
			t.Errorf("at DAA %d ML-DSA-44 balance: %+v, want %d available and %d pending",
				tc.score, got[mlAddr.String()], tc.mlAvailable, tc.mlPending)
		}
		if got[legacyAddr.String()] != (balancesType{oneHTN, 2 * oneHTN}) {
			t.Errorf("at DAA %d legacy balance: %+v, want 1 HTN available and 2 HTN pending",
				tc.score, got[legacyAddr.String()])
		}
	}
}

func TestExternalSpendableUTXOsSkipsIdentifiableInactiveMLDSA44(t *testing.T) {
	w := newMLWallet(t, 1, 1)
	native := &walletAddress{index: 1, keyChain: libhtnwallet.ExternalKeychain, mldsa44: true}
	knownP2SH := &walletAddress{index: 1, keyChain: libhtnwallet.ExternalKeychain, mldsa44: true,
		mldsa44Form: libhtnwallet.MLDSA44AddressFormP2SH}
	unknownP2SH := *knownP2SH
	unknownP2SH.index = 2
	legacy := &walletAddress{index: 1, keyChain: libhtnwallet.ExternalKeychain}
	knownP2SHAddr := w.address(t, knownP2SH)
	w.s.addressSet[knownP2SHAddr.String()] = knownP2SH

	node := &coinNode{}
	addresses := []struct {
		address              util.Address
		wantBeforeActivation int
	}{
		{w.address(t, native), 0},
		{knownP2SHAddr, 0},
		{w.address(t, &unknownP2SH), 1},
		{w.address(t, legacy), 1},
	}
	for i, tc := range addresses {
		node.coins = append(node.coins, testNodeCoin(t, tc.address, byte(i+1), 100_000_000, 1))
	}
	connectCoinNode(t, w.s, node)
	const activation = 258_000_000
	for _, score := range []uint64{activation - 1, activation} {
		node.virtualDAAScore.Store(score)
		for _, tc := range addresses {
			response, err := w.s.GetExternalSpendableUTXOs(t.Context(),
				&pb.GetExternalSpendableUTXOsRequest{Address: tc.address.String()})
			if err != nil {
				t.Fatalf("GetExternalSpendableUTXOs: %v", err)
			}
			want := tc.wantBeforeActivation
			if score == activation {
				want = 1
			}
			if len(response.Entries) != want {
				t.Errorf("at DAA %d got %d spendable outputs for %s, want %d", score, len(response.Entries), tc.address, want)
			}
		}
	}
}
