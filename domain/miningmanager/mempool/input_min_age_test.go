package mempool

import (
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/testapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/testutils"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/utxo"
	"github.com/HoosatNetwork/HTND/v2/domain/consensusreference"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
	"github.com/pkg/errors"
)

// fixedVirtualDAAScoreConsensus reports a fixed virtual DAA score and nothing else.
type fixedVirtualDAAScoreConsensus struct {
	externalapi.Consensus
	virtualDAAScore uint64
}

func (c fixedVirtualDAAScoreConsensus) GetVirtualDAAScore() (uint64, error) {
	return c.virtualDAAScore, nil
}

// The coinbase of 0d44a4cf was stamped at DAA 231080611 by 35b4eeaf and spent at virtual DAA 231080716.
const reorgCaseStamp = 231080611

func transactionSpending(entries ...externalapi.UTXOEntry) *externalapi.DomainTransaction {
	inputs := make([]*externalapi.DomainTransactionInput, len(entries))
	for i, entry := range entries {
		inputs[i] = &externalapi.DomainTransactionInput{
			PreviousOutpoint: externalapi.DomainOutpoint{
				TransactionID: *externalapi.NewDomainTransactionIDFromByteArray(&[externalapi.DomainHashSize]byte{byte(i + 1)}),
				Index:         uint32(i),
			},
			UTXOEntry: entry,
		}
	}
	return &externalapi.DomainTransaction{
		Inputs:  inputs,
		Outputs: []*externalapi.DomainTransactionOutput{{Value: 1, ScriptPublicKey: &externalapi.ScriptPublicKey{}}},
	}
}

func coinbaseEntry(daaScore uint64) externalapi.UTXOEntry {
	return utxo.NewUTXOEntry(1_266_666_667, &externalapi.ScriptPublicKey{}, true, daaScore)
}

func regularEntry(daaScore uint64) externalapi.UTXOEntry {
	return utxo.NewUTXOEntry(1_266_666_667, &externalapi.ScriptPublicKey{}, false, daaScore)
}

func newMempoolAtVirtualDAAScore(virtualDAAScore uint64) *mempool {
	var fake externalapi.Consensus = fixedVirtualDAAScoreConsensus{virtualDAAScore: virtualDAAScore}
	fakePointer := &fake
	return New(DefaultConfig(&dagconfig.MainnetParams), consensusreference.NewConsensusReference(&fakePointer)).(*mempool)
}

func TestInputMinAgeDefault(t *testing.T) {
	config := DefaultConfig(&dagconfig.MainnetParams)
	if config.InputMinAgeDAAScore != 1000 {
		t.Fatalf("default InputMinAgeDAAScore is %d, want 1000 to match htnwallet", config.InputMinAgeDAAScore)
	}
}

// TestInputMinAge pins the policy boundary. A non-coinbase input is refused while virtual DAA score <
// its DAA score + min age and accepted from exactly that score; a coinbase input is held to maturity +
// min age. The refusal must be RejectImmatureSpend, never RejectInvalid, the only code the relay flow
// punishes the sending peer for.
func TestInputMinAge(t *testing.T) {
	maturity := dagconfig.MainnetParams.BlockCoinbaseMaturity
	regularBoundary := uint64(reorgCaseStamp) + defaultInputMinAgeDAAScore
	coinbaseBoundary := regularBoundary + maturity

	tests := []struct {
		name         string
		virtual      uint64
		tx           *externalapi.DomainTransaction
		wantRejected bool
	}{
		{"coinbase at the DAA it was actually spent at", 231080716, transactionSpending(coinbaseEntry(reorgCaseStamp)), true},
		{"coinbase exactly at consensus maturity", reorgCaseStamp + maturity, transactionSpending(coinbaseEntry(reorgCaseStamp)), true},
		{"coinbase at the non-coinbase boundary", regularBoundary, transactionSpending(coinbaseEntry(reorgCaseStamp)), true},
		{"coinbase at maturity+minAge-1", coinbaseBoundary - 1, transactionSpending(coinbaseEntry(reorgCaseStamp)), true},
		{"coinbase at maturity+minAge", coinbaseBoundary, transactionSpending(coinbaseEntry(reorgCaseStamp)), false},
		{"non-coinbase input just created", reorgCaseStamp + 1, transactionSpending(regularEntry(reorgCaseStamp)), true},
		{"non-coinbase input at minAge-1", regularBoundary - 1, transactionSpending(regularEntry(reorgCaseStamp)), true},
		{"non-coinbase input at minAge", regularBoundary, transactionSpending(regularEntry(reorgCaseStamp)), false},
		{"one young input among old ones", regularBoundary,
			transactionSpending(regularEntry(reorgCaseStamp-5), regularEntry(reorgCaseStamp+1), regularEntry(reorgCaseStamp-9)), true},
		{"young coinbase among old regular inputs", coinbaseBoundary - 1,
			transactionSpending(regularEntry(reorgCaseStamp), regularEntry(reorgCaseStamp), coinbaseEntry(reorgCaseStamp)), true},
		{"old coinbase among old regular inputs", coinbaseBoundary,
			transactionSpending(regularEntry(reorgCaseStamp), coinbaseEntry(reorgCaseStamp)), false},
		{"input without a UTXO entry", coinbaseBoundary * 2, transactionSpending(regularEntry(reorgCaseStamp), nil), true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mp := newMempoolAtVirtualDAAScore(test.virtual)

			err := mp.checkInputMinAge(test.tx)
			assertMinAgeVerdict(t, "checkInputMinAge", err, test.wantRejected)

			// validateTransactionInContext is where admission applies it, for local and relayed
			// transactions alike, and for orphans being promoted. Only rejection is asserted there:
			// an accepted transaction goes on to checks this test does not set up for. Admission never
			// gets there with an input lacking its UTXO entry, and its other checks assume one.
			if test.wantRejected && allInputsHaveEntries(test.tx) {
				for _, isLocal := range []bool{false, true} {
					err := mp.validateTransactionInContext(test.tx, isLocal)
					assertMinAgeVerdict(t, "validateTransactionInContext", err, true)
				}
			}

			index, required, found := inputBelowMinAge(test.tx, test.virtual, maturity, defaultInputMinAgeDAAScore)
			if found != test.wantRejected {
				t.Fatalf("inputBelowMinAge found = %t, want %t", found, test.wantRejected)
			}
			if found && test.tx.Inputs[index].UTXOEntry != nil {
				want := regularBoundary
				if test.tx.Inputs[index].UTXOEntry.IsCoinbase() {
					want = coinbaseBoundary
				}
				want += test.tx.Inputs[index].UTXOEntry.BlockDAAScore() - reorgCaseStamp
				if required != want {
					t.Fatalf("inputBelowMinAge = input %d, required %d; want %d", index, required, want)
				}
			}
		})
	}
}

// With the minimum age set to 0 the policy adds nothing to consensus.
func TestInputMinAgeDisabled(t *testing.T) {
	mp := newMempoolAtVirtualDAAScore(reorgCaseStamp + 1)
	mp.config.InputMinAgeDAAScore = 0
	for _, tx := range []*externalapi.DomainTransaction{
		transactionSpending(coinbaseEntry(reorgCaseStamp)),
		transactionSpending(regularEntry(reorgCaseStamp)),
	} {
		if err := mp.checkInputMinAge(tx); err != nil {
			t.Fatalf("min age 0 still refused a transaction: %s", err)
		}
	}
}

func newMempoolWithTestConsensus(t *testing.T, consensusConfig *consensus.Config, name string) (*mempool, testapi.TestConsensus, func(bool)) {
	tc, teardown, err := consensus.NewFactory().NewTestConsensus(consensusConfig, name)
	if err != nil {
		t.Fatalf("Error setting up consensus: %+v", err)
	}
	tcAsConsensus := tc.(externalapi.Consensus)
	tcAsConsensusPointer := &tcAsConsensus
	mp := New(DefaultConfig(tc.DAGParams()), consensusreference.NewConsensusReference(&tcAsConsensusPointer)).(*mempool)
	return mp, tc, teardown
}

// TestInputMinAgeRefusesUnconfirmedParents pins that a transaction spending an output of a transaction
// still in the mempool, or of one this node has not seen at all, is refused - not held as an orphan -
// while the minimum age is on, and that with it off the orphan pool behaves as before.
func TestInputMinAgeRefusesUnconfirmedParents(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		mp, tc, teardown := newMempoolWithTestConsensus(t, consensusConfig, "TestInputMinAgeRefusesUnconfirmedParents")
		defer teardown(false)

		funding := testutils.CreateTransactionWithOutput(100_000)
		if err := testutils.StageTransactionOutputsToVirtual(tc, funding, 0); err != nil {
			t.Fatalf("StageTransactionOutputsToVirtual: %+v", err)
		}
		parent, err := testutils.CreateTransaction(funding, 1_000)
		if err != nil {
			t.Fatalf("CreateTransaction(parent): %+v", err)
		}
		child, err := testutils.CreateTransaction(parent, 1_000)
		if err != nil {
			t.Fatalf("CreateTransaction(child): %+v", err)
		}

		// The parent's funding output is fresh too, so admit the parent with the policy off.
		mp.config.InputMinAgeDAAScore = 0
		if _, err := mp.ValidateAndInsertTransaction(parent, false, true, false); err != nil {
			t.Fatalf("ValidateAndInsertTransaction(parent): %+v", err)
		}
		mp.config.InputMinAgeDAAScore = defaultInputMinAgeDAAScore

		// Spending the parent's output, which is only in the mempool, is refused.
		for _, isLocal := range []bool{false, true} {
			_, err = mp.ValidateAndInsertTransaction(child, isLocal, true, isLocal)
			assertMinAgeVerdict(t, "child of a mempool transaction", err, true)
		}
		// So is spending the output of a transaction nobody has seen.
		unknownParent := testutils.CreateTransactionWithOutput(50_000)
		orphan, err := testutils.CreateTransaction(unknownParent, 1_000)
		if err != nil {
			t.Fatalf("CreateTransaction(orphan): %+v", err)
		}
		_, err = mp.ValidateAndInsertTransaction(orphan, false, true, false)
		assertMinAgeVerdict(t, "child of an unknown transaction", err, true)
		if count := mp.orphansPool.orphanTransactionCount(); count != 0 {
			t.Fatalf("refused transactions were kept as orphans: %d", count)
		}

		// With the policy off, the child waits in the orphan pool as it used to.
		mp.config.InputMinAgeDAAScore = 0
		if _, err := mp.ValidateAndInsertTransaction(child, false, true, false); err != nil {
			t.Fatalf("with min age 0, ValidateAndInsertTransaction(child): %+v", err)
		}
		if count := mp.orphansPool.orphanTransactionCount(); count != 1 {
			t.Fatalf("with min age 0, expected the child as an orphan, got %d orphans", count)
		}
	})
}

// TestInputMinAgeRefusesOrphanPromotion pins that an orphan held from before the policy applied is not
// promoted when its parent is mined: the parent's outputs are brand new, so the orphan is dropped
// instead of entering the mempool.
func TestInputMinAgeRefusesOrphanPromotion(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		mp, tc, teardown := newMempoolWithTestConsensus(t, consensusConfig, "TestInputMinAgeRefusesOrphanPromotion")
		defer teardown(false)

		grandparent := testutils.CreateTransactionWithOutput(100_000)
		parent, err := testutils.CreateTransaction(grandparent, 1_000)
		if err != nil {
			t.Fatalf("CreateTransaction(parent): %+v", err)
		}
		child, err := testutils.CreateTransaction(parent, 1_000)
		if err != nil {
			t.Fatalf("CreateTransaction(child): %+v", err)
		}
		mp.config.InputMinAgeDAAScore = 0
		if _, err := mp.ValidateAndInsertTransaction(child, false, true, false); err != nil {
			t.Fatalf("ValidateAndInsertTransaction(child): %+v", err)
		}
		if count := mp.orphansPool.orphanTransactionCount(); count != 1 {
			t.Fatalf("expected the child as an orphan, got %d orphans", count)
		}
		mp.config.InputMinAgeDAAScore = defaultInputMinAgeDAAScore

		virtualDAAScore, err := tc.GetVirtualDAAScore()
		if err != nil {
			t.Fatalf("GetVirtualDAAScore: %+v", err)
		}
		if err := testutils.StageCreatedOutputsToVirtual(tc, parent, virtualDAAScore); err != nil {
			t.Fatalf("StageCreatedOutputsToVirtual(parent): %+v", err)
		}
		coinbase := testutils.CreateTransactionWithOutput(1)
		accepted, err := mp.HandleNewBlockTransactions([]*externalapi.DomainTransaction{coinbase, parent})
		if err != nil {
			t.Fatalf("HandleNewBlockTransactions: %+v", err)
		}
		if len(accepted) != 0 {
			t.Fatalf("an orphan spending a just-mined output was promoted (%d accepted)", len(accepted))
		}
		if _, ok := mp.transactionsPool.allTransactions[*consensushashing.TransactionID(child)]; ok {
			t.Fatalf("the orphan entered the mempool")
		}
		if count := mp.orphansPool.orphanTransactionCount(); count != 0 {
			t.Fatalf("the refused orphan is still in the orphan pool (%d orphans)", count)
		}
	})
}

func allInputsHaveEntries(transaction *externalapi.DomainTransaction) bool {
	for _, input := range transaction.Inputs {
		if input.UTXOEntry == nil {
			return false
		}
	}
	return true
}

func assertMinAgeVerdict(t *testing.T, what string, err error, wantRejected bool) {
	t.Helper()
	if !wantRejected {
		if err != nil {
			t.Fatalf("%s refused the transaction: %s", what, err)
		}
		return
	}
	if err == nil {
		t.Fatalf("%s accepted the transaction", what)
	}
	if !errors.As(err, &RuleError{}) {
		t.Fatalf("%s returned a non-rule error: %s", what, err)
	}
	code, found := extractRejectCode(err)
	if !found || code != RejectImmatureSpend {
		t.Fatalf("%s rejected with code %s (found %t), want %s: %s", what, code, found, RejectImmatureSpend, err)
	}
}

// configWithoutInputMinAge is the default configuration with the minimum input age off, for tests of
// other behaviour that chain transactions or spend outputs straight after staging them.
func configWithoutInputMinAge(params *dagconfig.Params) *Config {
	config := DefaultConfig(params)
	config.InputMinAgeDAAScore = 0
	return config
}
