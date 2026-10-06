package flowcontext

import (
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/domain"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/merkle"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/testutils"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/transactionhelper"
	"github.com/HoosatNetwork/HTND/v2/domain/miningmanager/mempool"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database/ldb"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter"
)

// disqualifiedCarrierFixture is a node with a payment - one input, a payment output and a change output -
// in its mempool, and a block carrying that payment which consensus disqualified when it was inserted.
type disqualifiedCarrierFixture struct {
	flowContext  *FlowContext
	domain       domain.Domain
	coinbaseData *externalapi.DomainCoinbaseData
	payment      *externalapi.DomainTransaction
	paymentID    *externalapi.DomainTransactionID
	carrier      *externalapi.DomainBlock
}

func newDisqualifiedCarrierFixture(t *testing.T, consensusConfig *consensus.Config, name string) (
	*disqualifiedCarrierFixture, func(),
) {
	consensusConfig.BlockCoinbaseMaturity = 0
	dataDir, err := os.MkdirTemp("", fmt.Sprintf("%s-%s", name, consensusConfig.Name))
	if err != nil {
		t.Fatalf("MkdirTemp: %+v", err)
	}
	db, err := ldb.NewLevelDB(dataDir, 8)
	if err != nil {
		t.Fatalf("NewLevelDB: %+v", err)
	}
	teardown := func() {
		_ = db.Close()
		_ = os.RemoveAll(dataDir)
	}
	mempoolConfig := mempool.DefaultConfig(&consensusConfig.Params)
	mempoolConfig.InputMinAgeDAAScore = 0
	domainInstance, err := domain.New(consensusConfig, mempoolConfig, db)
	if err != nil {
		teardown()
		t.Fatalf("domain.New: %+v", err)
	}

	f := &disqualifiedCarrierFixture{
		flowContext: New(nil, domainInstance, nil, &netadapter.NetAdapter{}, nil),
		domain:      domainInstance,
	}
	f.flowContext.lastRebroadcastTime = time.Now()
	scriptPublicKey, _ := testutils.OpTrueScript()
	f.coinbaseData = &externalapi.DomainCoinbaseData{ScriptPublicKey: scriptPublicKey, ExtraData: []byte{}}

	// Fund the payment with the coinbase of the virtual selected parent, which virtual accepts.
	var fundingBlock *externalapi.DomainBlock
	for range 2 {
		fundingBlock = f.mineOnVirtual(t, nil)
	}
	f.payment, err = testutils.CreateTransaction(fundingBlock.Transactions[transactionhelper.CoinbaseTransactionIndex], 10000)
	if err != nil {
		t.Fatalf("CreateTransaction: %+v", err)
	}
	total := f.payment.Outputs[0].Value
	change := *f.payment.Outputs[0]
	f.payment.Outputs[0].Value = total / 2
	change.Value = total - total/2
	f.payment.Outputs = append(f.payment.Outputs, &change)
	f.paymentID = consensushashing.TransactionID(f.payment)

	// A wallet submission over RPC is high priority by default (rpchandlers.HandleSubmitTransaction).
	_, err = domainInstance.MiningManager().ValidateAndInsertTransaction(f.payment, true, false, true)
	if err != nil {
		t.Fatalf("ValidateAndInsertTransaction: %+v", err)
	}

	// The carrier pays its miner one sompi too much, so it is disqualified when it is inserted - the same
	// outcome as a UTXO commitment from another lineage. It gets its own copy of the payment, as a block
	// received from a peer does.
	f.carrier, err = domainInstance.Consensus().BuildBlock(f.coinbaseData,
		[]*externalapi.DomainTransaction{f.payment.Clone()})
	if err != nil {
		t.Fatalf("BuildBlock: %+v", err)
	}
	asReceived(f.carrier)
	f.carrier.Transactions[transactionhelper.CoinbaseTransactionIndex].Outputs[0].Value++
	mutableHeader := f.carrier.Header.ToMutable()
	mutableHeader.SetHashMerkleRoot(merkle.CalculateHashMerkleRoot(f.carrier.Transactions))
	f.carrier.Header = mutableHeader.ToImmutable()
	err = domainInstance.Consensus().ValidateAndInsertBlock(f.carrier, true, true)
	if err != nil {
		t.Fatalf("ValidateAndInsertBlock(carrier): %+v", err)
	}
	carrierInfo, err := domainInstance.Consensus().GetBlockInfo(consensushashing.BlockHash(f.carrier))
	if err != nil {
		t.Fatalf("GetBlockInfo: %+v", err)
	}
	if carrierInfo.BlockStatus != externalapi.StatusDisqualifiedFromChain {
		t.Fatalf("carrier status is %s, want %s", carrierInfo.BlockStatus, externalapi.StatusDisqualifiedFromChain)
	}
	return f, teardown
}

// mineOnVirtual builds a block on virtual's parents with the given transactions, inserts it and runs
// OnNewBlock on it, as a node does with a block it received.
func (f *disqualifiedCarrierFixture) mineOnVirtual(t *testing.T, transactions []*externalapi.DomainTransaction) *externalapi.DomainBlock {
	block, err := f.domain.Consensus().BuildBlock(f.coinbaseData, transactions)
	if err != nil {
		t.Fatalf("BuildBlock: %+v", err)
	}
	asReceived(block)
	err = f.domain.Consensus().ValidateAndInsertBlock(block, true, true)
	if err != nil {
		t.Fatalf("ValidateAndInsertBlock: %+v", err)
	}
	err = f.flowContext.OnNewBlock(block)
	if err != nil {
		t.Fatalf("OnNewBlock: %+v", err)
	}
	return block
}

func (f *disqualifiedCarrierFixture) paymentIsInMempool() bool {
	_, _, found := f.domain.MiningManager().GetTransaction(f.paymentID, true, true)
	return found
}

// requirePaymentGetsAccepted mines the mempool twice - once to carry the payment, once to merge that
// block - and requires the payment's input to be spent and both of its outputs to exist.
func (f *disqualifiedCarrierFixture) requirePaymentGetsAccepted(t *testing.T) {
	candidates, _ := f.domain.MiningManager().AllTransactions(true, false)
	if !slices.ContainsFunc(candidates, func(tx *externalapi.DomainTransaction) bool {
		return consensushashing.TransactionID(tx).Equal(f.paymentID)
	}) {
		t.Fatalf("the payment is not a block candidate")
	}
	f.mineOnVirtual(t, candidates)
	f.mineOnVirtual(t, nil)

	outpoints := []*externalapi.DomainOutpoint{&f.payment.Inputs[0].PreviousOutpoint}
	for i := range f.payment.Outputs {
		outpoints = append(outpoints, externalapi.NewDomainOutpoint(f.paymentID, uint32(i)))
	}
	entries, _, ok, err := f.domain.Consensus().GetVirtualUTXOEntries(outpoints, time.Minute)
	if err != nil || !ok {
		t.Fatalf("GetVirtualUTXOEntries: ok=%t err=%+v", ok, err)
	}
	if entries[0] != nil {
		t.Fatalf("the payment's input is still unspent")
	}
	for i, entry := range entries[1:] {
		if entry == nil {
			t.Fatalf("output %d of the payment does not exist", i)
		}
	}
}

// TestDisqualifiedBlockKeepsItsTransactionsInTheMempool pins that a block disqualified when it arrives
// does not take its transactions out of the mempool. Nothing ever merges such a block, so removing them
// stranded a payment: its input stayed unspent, its payment and change outputs never appeared, and with
// it gone from the mempool nothing resent it.
func TestDisqualifiedBlockKeepsItsTransactionsInTheMempool(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		f, teardown := newDisqualifiedCarrierFixture(t, consensusConfig, "TestDisqualifiedBlockKeepsItsTransactionsInTheMempool")
		defer teardown()

		err := f.flowContext.OnNewBlock(f.carrier)
		if err != nil {
			t.Fatalf("OnNewBlock: %+v", err)
		}
		if !f.paymentIsInMempool() {
			t.Fatalf("the disqualified carrier took the payment out of the mempool")
		}
		// It keeps its priority, so the node keeps rebroadcasting it.
		rebroadcast, err := f.domain.MiningManager().RevalidateHighPriorityTransactions()
		if err != nil {
			t.Fatalf("RevalidateHighPriorityTransactions: %+v", err)
		}
		if !slices.ContainsFunc(rebroadcast, func(tx *externalapi.DomainTransaction) bool {
			return consensushashing.TransactionID(tx).Equal(f.paymentID)
		}) {
			t.Fatalf("the payment is no longer rebroadcast")
		}

		f.requirePaymentGetsAccepted(t)
		if f.paymentIsInMempool() {
			t.Fatalf("the payment is still in the mempool after it was accepted")
		}
	})
}

// TestTransactionsOfLaterDisqualifiedBlockAreRestored pins the other half: a block whose transactions
// were removed from the mempool and that consensus disqualified afterwards - one that was pending
// verification when it arrived - has them put back on the next block. The removal is done by hand here,
// as OnNewBlock would have done for a block that arrived pending; the disqualification reaches the mining
// manager through consensus.Config.OnDisqualification, as wired by domain.New.
func TestTransactionsOfLaterDisqualifiedBlockAreRestored(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		f, teardown := newDisqualifiedCarrierFixture(t, consensusConfig, "TestTransactionsOfLaterDisqualifiedBlockAreRestored")
		defer teardown()

		_, err := f.domain.MiningManager().HandleNewBlockTransactions(f.carrier.Transactions)
		if err != nil {
			t.Fatalf("HandleNewBlockTransactions: %+v", err)
		}
		if f.paymentIsInMempool() {
			t.Fatalf("setup: the payment is still in the mempool")
		}

		f.mineOnVirtual(t, nil)
		if !f.paymentIsInMempool() {
			t.Fatalf("the payment of the disqualified carrier was not restored to the mempool")
		}

		f.requirePaymentGetsAccepted(t)

		// Once accepted it spends its input, so restoring it again is refused and nothing comes back.
		f.domain.MiningManager().NoteDisqualifiedBlock(consensushashing.BlockHash(f.carrier))
		restored, err := f.domain.MiningManager().RestoreTransactionsOfDisqualifiedBlocks()
		if err != nil {
			t.Fatalf("RestoreTransactionsOfDisqualifiedBlocks: %+v", err)
		}
		if len(restored) != 0 || f.paymentIsInMempool() {
			t.Fatalf("an accepted payment was restored to the mempool")
		}
	})
}

// asReceived drops the UTXO entries BuildBlock fills into a block's inputs. A block that arrives from a
// peer has none - they are not serialized - and consensus refuses a block that has them.
func asReceived(block *externalapi.DomainBlock) *externalapi.DomainBlock {
	for _, transaction := range block.Transactions {
		for _, input := range transaction.Inputs {
			input.UTXOEntry = nil
		}
	}
	return block
}
