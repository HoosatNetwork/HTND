package mempool

import (
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
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

func TestCoinbaseReorgSafetyMarginDefault(t *testing.T) {
	config := DefaultConfig(&dagconfig.MainnetParams)
	if config.CoinbaseReorgSafetyMarginDAAScore != 1000 {
		t.Fatalf("default CoinbaseReorgSafetyMarginDAAScore is %d, want 1000 to match htnwallet",
			config.CoinbaseReorgSafetyMarginDAAScore)
	}
}

// TestCoinbaseReorgSafetyMargin pins the policy boundary: a transaction spending a coinbase output is
// refused while virtual DAA score < stamp + maturity + margin and accepted from exactly that score, and
// non-coinbase inputs are never affected. The refusal must not be RejectInvalid, the only code the relay
// flow punishes the sending peer for.
func TestCoinbaseReorgSafetyMargin(t *testing.T) {
	maturity := dagconfig.MainnetParams.BlockCoinbaseMaturity
	boundary := uint64(reorgCaseStamp) + maturity + defaultCoinbaseReorgSafetyMarginDAAScore

	tests := []struct {
		name         string
		virtual      uint64
		tx           *externalapi.DomainTransaction
		wantRejected bool
	}{
		{"coinbase at the DAA it was actually spent at", 231080716, transactionSpending(coinbaseEntry(reorgCaseStamp)), true},
		{"coinbase exactly at consensus maturity", reorgCaseStamp + maturity, transactionSpending(coinbaseEntry(reorgCaseStamp)), true},
		{"coinbase at maturity+margin-1", boundary - 1, transactionSpending(coinbaseEntry(reorgCaseStamp)), true},
		{"coinbase at maturity+margin", boundary, transactionSpending(coinbaseEntry(reorgCaseStamp)), false},
		{"non-coinbase input just created", reorgCaseStamp + 1, transactionSpending(regularEntry(reorgCaseStamp)), false},
		{"young coinbase among regular inputs", boundary - 1,
			transactionSpending(regularEntry(reorgCaseStamp), regularEntry(reorgCaseStamp), coinbaseEntry(reorgCaseStamp)), true},
		{"old coinbase among regular inputs", boundary,
			transactionSpending(regularEntry(reorgCaseStamp), coinbaseEntry(reorgCaseStamp)), false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mp := newMempoolAtVirtualDAAScore(test.virtual)

			err := mp.checkCoinbaseReorgSafetyMargin(test.tx)
			assertCoinbaseMarginVerdict(t, "checkCoinbaseReorgSafetyMargin", err, test.wantRejected)

			// validateTransactionInContext is where admission applies it, for local and relayed
			// transactions alike. Only rejection is asserted there: an accepted transaction goes on
			// to checks this test does not set up for.
			if test.wantRejected {
				for _, isLocal := range []bool{false, true} {
					err := mp.validateTransactionInContext(test.tx, isLocal)
					assertCoinbaseMarginVerdict(t, "validateTransactionInContext", err, true)
				}
			}

			index, required, found := coinbaseInputWithinReorgSafetyMargin(test.tx, test.virtual, maturity,
				defaultCoinbaseReorgSafetyMarginDAAScore)
			if found != test.wantRejected {
				t.Fatalf("coinbaseInputWithinReorgSafetyMargin found = %t, want %t", found, test.wantRejected)
			}
			if found && (required != boundary || !test.tx.Inputs[index].UTXOEntry.IsCoinbase()) {
				t.Fatalf("coinbaseInputWithinReorgSafetyMargin = input %d, required %d; want a coinbase input and %d",
					index, required, boundary)
			}
		})
	}
}

// With the margin set to 0 the policy adds nothing to consensus maturity.
func TestCoinbaseReorgSafetyMarginDisabled(t *testing.T) {
	mp := newMempoolAtVirtualDAAScore(reorgCaseStamp + 1)
	mp.config.CoinbaseReorgSafetyMarginDAAScore = 0
	if err := mp.checkCoinbaseReorgSafetyMargin(transactionSpending(coinbaseEntry(reorgCaseStamp))); err != nil {
		t.Fatalf("margin 0 still refused a coinbase spend: %s", err)
	}
}

func assertCoinbaseMarginVerdict(t *testing.T, what string, err error, wantRejected bool) {
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
		t.Fatalf("%s rejected with code %s (found %t), want %s", what, code, found, RejectImmatureSpend)
	}
	if code == RejectInvalid {
		t.Fatalf("%s rejected with RejectInvalid, which bans the relaying peer", what)
	}
}
