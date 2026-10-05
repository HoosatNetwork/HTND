package consensusstatemanager

import (
	"strings"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/ruleerrors"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/utxo"
)

// TestDescribeTransactionVerdictNamesEachInputsState pins that the debug line tells a spent input
// from an absent one and lists the unresolved inputs first, since those are what explain the
// verdict and the listing is capped.
func TestDescribeTransactionVerdictNamesEachInputsState(t *testing.T) {
	outpoints := outpointsForTest(3)
	transaction := &externalapi.DomainTransaction{
		Inputs: []*externalapi.DomainTransactionInput{
			{PreviousOutpoint: *outpoints[0], UTXOEntry: utxo.NewUTXOEntry(500, &externalapi.ScriptPublicKey{}, false, 7)},
			{PreviousOutpoint: *outpoints[1]},
			{PreviousOutpoint: *outpoints[2]},
		},
		Outputs: []*externalapi.DomainTransactionOutput{{Value: 400}},
	}
	err := ruleerrors.NewErrMissingOrSpentTxOut(outpoints[1:], outpoints[1:2])
	ctx := &transactionVerdictContext{
		blockHash:         externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{9}),
		mergeSetBlockHash: externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{10}),
		blockDAAScore:     42,
	}

	line := describeTransactionVerdict("rejected: unresolved inputs", ctx, transaction, "tx", err, "")

	for _, want := range []string{
		"merging DAA score: 42",
		"3 inputs, 1 resolved totalling 500 sompi, 2 unresolved",
		"input 1 " + outpoints[1].String() + " seq 0 sigops 0: SPENT in this view",
		"input 2 " + outpoints[2].String() + " seq 0 sigops 0: ABSENT from this view",
		"amount 500, DAA score 7, coinbase false",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("verdict line is missing %q:\n%s", want, line)
		}
	}
	if strings.Index(line, "input 1 ") > strings.Index(line, "input 0 ") {
		t.Errorf("unresolved inputs must be listed before resolved ones:\n%s", line)
	}
	if strings.Contains(line, "implied fee") {
		t.Errorf("a fee cannot be implied with unresolved inputs:\n%s", line)
	}
}

// TestDescribeTransactionVerdictToleratesMissingPieces pins that the diagnostic cannot be what
// brings acceptance down: maybeAcceptTransaction tolerates nil ids and inputs, so this must too.
func TestDescribeTransactionVerdictToleratesMissingPieces(t *testing.T) {
	_ = describeTransactionVerdict("x", nil, nil, "<nil>", nil, "")
	_ = describeTransactionVerdict("x", &transactionVerdictContext{}, &externalapi.DomainTransaction{
		Inputs:  []*externalapi.DomainTransactionInput{nil},
		Outputs: []*externalapi.DomainTransactionOutput{nil},
	}, "<nil>", nil, "")
}

// TestDescribeTransactionVerdictCapsInputs pins the listing cap, so a compound transaction does
// not turn one debug line into hundreds of kilobytes.
func TestDescribeTransactionVerdictCapsInputs(t *testing.T) {
	transaction := &externalapi.DomainTransaction{}
	for _, outpoint := range outpointsForTest(maxRejectionInputsListed + 5) {
		transaction.Inputs = append(transaction.Inputs, &externalapi.DomainTransactionInput{PreviousOutpoint: *outpoint})
	}
	line := describeTransactionVerdict("x", nil, transaction, "tx", nil, "")
	if !strings.Contains(line, "... 5 more inputs not listed") {
		t.Errorf("expected the input listing to be capped:\n%s", line)
	}
}
