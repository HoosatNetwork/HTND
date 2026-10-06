package pruningmanager

import (
	"strings"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/utxo"
)

func reportTestOutpoint(b byte) externalapi.DomainOutpoint {
	return *externalapi.NewDomainOutpoint(
		externalapi.NewDomainTransactionIDFromByteArray(&[externalapi.DomainHashSize]byte{b}), uint32(b))
}

func reportTestEntry(amount, daaScore uint64) externalapi.UTXOEntry {
	return utxo.NewUTXOEntry(amount, &externalapi.ScriptPublicKey{Script: []byte{1, 2}}, false, daaScore)
}

func reportTestDiff(t *testing.T, toAdd, toRemove map[externalapi.DomainOutpoint]externalapi.UTXOEntry) externalapi.UTXODiff {
	diff, err := utxo.NewUTXODiffFromCollections(utxo.NewUTXOCollection(toAdd), utxo.NewUTXOCollection(toRemove))
	if err != nil {
		t.Fatalf("NewUTXODiffFromCollections: %s", err)
	}
	return diff
}

// TestDescribeUTXODiffDisagreementNamesEachKind pins that the two derivations are compared entry by
// entry, in both directions, and that a DAA-score-only difference is named as such - the stamp is
// part of the commitment preimage and has been the cause of a mismatch before.
func TestDescribeUTXODiffDisagreementNamesEachKind(t *testing.T) {
	shared, onlyA, onlyB, restamped, removedOnlyB :=
		reportTestOutpoint(1), reportTestOutpoint(2), reportTestOutpoint(3), reportTestOutpoint(4), reportTestOutpoint(5)
	diffA := reportTestDiff(t, map[externalapi.DomainOutpoint]externalapi.UTXOEntry{
		shared:    reportTestEntry(10, 100),
		onlyA:     reportTestEntry(20, 100),
		restamped: reportTestEntry(30, 100),
	}, map[externalapi.DomainOutpoint]externalapi.UTXOEntry{})
	diffB := reportTestDiff(t, map[externalapi.DomainOutpoint]externalapi.UTXOEntry{
		shared:    reportTestEntry(10, 100),
		onlyB:     reportTestEntry(40, 100),
		restamped: reportTestEntry(30, 97),
	}, map[externalapi.DomainOutpoint]externalapi.UTXOEntry{
		removedOnlyB: reportTestEntry(50, 90),
	})

	line := describeUTXODiffDisagreement("a", diffA, "b", diffB, 20)
	for _, want := range []string{
		"DISAGREE on 4 outpoints",
		"toAdd-only-in-a=1", "toAdd-only-in-b=1", "toAdd-entry-differs=1", "toRemove-only-in-b=1",
		"differs in DAA score by 3",
		onlyA.String(), onlyB.String(), restamped.String(), removedOnlyB.String(),
	} {
		if !strings.Contains(line, want) {
			t.Errorf("disagreement report is missing %q:\n%s", want, line)
		}
	}
	if strings.Contains(line, shared.String()) {
		t.Errorf("an outpoint both derivations agree on must not be reported:\n%s", line)
	}
}

func TestDescribeUTXODiffDisagreementAgreeingAndCapped(t *testing.T) {
	toAdd := map[externalapi.DomainOutpoint]externalapi.UTXOEntry{}
	for i := byte(1); i <= 10; i++ {
		toAdd[reportTestOutpoint(i)] = reportTestEntry(uint64(i), 1)
	}
	empty := map[externalapi.DomainOutpoint]externalapi.UTXOEntry{}
	full := reportTestDiff(t, toAdd, empty)

	if line := describeUTXODiffDisagreement("a", full, "b", full, 3); !strings.Contains(line, "AGREE on every outpoint") {
		t.Errorf("identical diffs must be reported as agreeing:\n%s", line)
	}
	line := describeUTXODiffDisagreement("a", full, "b", reportTestDiff(t, empty, empty), 3)
	if !strings.Contains(line, "DISAGREE on 10 outpoints") || !strings.Contains(line, "first 3:") {
		t.Errorf("expected the total and a capped example list:\n%s", line)
	}
}

// TestClassifyPruningPointMismatch pins which code path each combination of comparisons blames.
func TestClassifyPruningPointMismatch(t *testing.T) {
	header := externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{1})
	bucket := externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{2})
	other := externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{3})
	tests := []struct {
		previousClean bool
		perBlock      *externalapi.DomainHash
		want          string
	}{
		{false, header, "VERDICT bucket-derivation"},
		{true, nil, "VERDICT unknown"},
		{false, bucket, "VERDICT inherited:"},
		{true, bucket, "VERDICT entered-this-interval:"},
		{true, other, "VERDICT entered-this-interval-and-bucket-drift"},
		{false, other, "VERDICT inherited-and-bucket-drift"},
	}
	for _, test := range tests {
		got := classifyPruningPointMismatch(test.previousClean, test.perBlock, header, bucket)
		if !strings.HasPrefix(got, test.want) {
			t.Errorf("previousClean=%t perBlock=%v: got %q, want prefix %q", test.previousClean, test.perBlock, got, test.want)
		}
	}
}

func TestDescribeAcceptanceDataCountsRejections(t *testing.T) {
	blockHash := externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{7})
	transaction := &externalapi.DomainTransaction{Outputs: []*externalapi.DomainTransactionOutput{{Value: 1, ScriptPublicKey: &externalapi.ScriptPublicKey{}}}}
	line := describeAcceptanceData(externalapi.AcceptanceData{{
		BlockHash: blockHash,
		TransactionAcceptanceData: []*externalapi.TransactionAcceptanceData{
			{Transaction: transaction, IsAccepted: true, Fee: 5},
			{Transaction: transaction, IsAccepted: false},
			nil,
		},
	}, nil})
	for _, want := range []string{"merge set of 2 blocks", "selected parent " + blockHash.String() + ": 1/3 transactions accepted, fees 5",
		"1 transactions not accepted"} {
		if !strings.Contains(line, want) {
			t.Errorf("acceptance summary is missing %q:\n%s", want, line)
		}
	}
}

func TestUTXOSetStats(t *testing.T) {
	stats := &utxoSetStats{}
	stats.add(reportTestEntry(10, 50))
	stats.add(utxo.NewUTXOEntry(7, &externalapi.ScriptPublicKey{}, true, 20))
	if got, want := stats.String(), "2 entries totalling 17 sompi (1 coinbase entries, 7 sompi), entry DAA scores 20..50"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
