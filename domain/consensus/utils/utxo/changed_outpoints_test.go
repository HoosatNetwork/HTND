package utxo

import (
	"math/rand"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/subnetworks"
)

// TestDiffFromChangedMatchesDiffFrom pins that DiffFromChanged returns exactly what DiffFrom returns -
// the same diff, or an error exactly when DiffFrom errors - for a diff cloned from a base with
// CloneMutableRecordingChanges and then changed the ways virtual resolution changes one: spending
// coins the base adds and coins it does not mention, creating outputs, spending outputs created in the
// same pass, re-adding a coin the diff already holds (with the same and with a different DAA score),
// failed transactions that roll back, and WithDiffInPlace.
func TestDiffFromChangedMatchesDiffFrom(t *testing.T) {
	const cases = 20000
	errorCases, nonEmptyCases := 0, 0
	for seed := int64(0); seed < cases; seed++ {
		random := rand.New(rand.NewSource(seed))
		base, virtualCoins := randomBaseDiff(random)

		changing, err := CloneMutableRecordingChanges(base)
		if err != nil {
			t.Fatalf("seed %d: CloneMutableRecordingChanges: %+v", seed, err)
		}
		mutateLikeResolution(random, changing.(*mutableUTXODiff), base, virtualCoins)
		changed := changing.ToImmutable()

		full, fullErr := base.DiffFrom(changed)
		fast, ok, fastErr := DiffFromChanged(base, changed)
		if !ok {
			t.Fatalf("seed %d: DiffFromChanged did not recognise a recording diff", seed)
		}
		if (fullErr == nil) != (fastErr == nil) {
			t.Fatalf("seed %d: DiffFrom error %v, DiffFromChanged error %v\nbase: %s\nchanged: %s",
				seed, fullErr, fastErr, base, changed)
		}
		if fullErr != nil {
			errorCases++
			continue
		}
		if full.ToAdd().Len()+full.ToRemove().Len() > 0 {
			nonEmptyCases++
		}
		if !fast.Equal(full) {
			t.Fatalf("seed %d: diffs differ\nDiffFrom:        %s\nDiffFromChanged: %s\nbase: %s\nchanged: %s",
				seed, full, fast, base, changed)
		}
	}
	t.Logf("%d cases: %d errors in both, %d non-empty diffs", cases, errorCases, nonEmptyCases)
	if errorCases == 0 || nonEmptyCases < cases/2 {
		t.Fatalf("the cases do not exercise enough: %d errors, %d non-empty diffs", errorCases, nonEmptyCases)
	}
}

func TestDiffFromChangedIgnoresADiffThatDoesNotRecord(t *testing.T) {
	base := NewUTXODiff()
	notRecording := base.CloneMutable().ToImmutable()
	if _, ok, err := DiffFromChanged(base, notRecording); ok || err != nil {
		t.Fatalf("DiffFromChanged on a non-recording diff = ok %v, err %v; want false, nil", ok, err)
	}
}

// TestDiffFromChangedRefusesAnotherBase pins that a recording diff is only diffed against the diff it
// was cloned from: against any other, the outpoints it recorded are not the ones that differ.
func TestDiffFromChangedRefusesAnotherBase(t *testing.T) {
	base := NewUTXODiff()
	recording, err := CloneMutableRecordingChanges(base)
	if err != nil {
		t.Fatalf("CloneMutableRecordingChanges: %+v", err)
	}
	if _, ok, err := DiffFromChanged(NewUTXODiff(), recording.ToImmutable()); ok || err != nil {
		t.Fatalf("DiffFromChanged against another base = ok %v, err %v; want false, nil", ok, err)
	}
	if _, ok, err := DiffFromChanged(base, recording.ToImmutable()); !ok || err != nil {
		t.Fatalf("DiffFromChanged against its own base = ok %v, err %v; want true, nil", ok, err)
	}
}

func randomOutpoint(random *rand.Rand) externalapi.DomainOutpoint {
	var id [externalapi.DomainHashSize]byte
	random.Read(id[:])
	return externalapi.DomainOutpoint{
		TransactionID: externalapi.DomainTransactionID(*externalapi.NewDomainHashFromByteArray(&id)),
		Index:         uint32(random.Intn(3)),
	}
}

func randomEntry(random *rand.Rand, daaScore uint64) externalapi.UTXOEntry {
	return NewUTXOEntry(uint64(1+random.Intn(3)), &externalapi.ScriptPublicKey{Script: []byte{byte(random.Intn(2))}},
		random.Intn(4) == 0, daaScore)
}

// randomBaseDiff returns a diff from virtual, in the shapes a selected-parent past takes, and coins
// virtual's own set holds that the diff does not mention.
func randomBaseDiff(random *rand.Rand) (externalapi.UTXODiff, map[externalapi.DomainOutpoint]externalapi.UTXOEntry) {
	diff := newMutableUTXODiff()
	virtualCoins := map[externalapi.DomainOutpoint]externalapi.UTXOEntry{}
	for range 4 + random.Intn(12) {
		outpoint := randomOutpoint(random)
		switch random.Intn(4) {
		case 0: // created since virtual
			diff.toAdd.add(&outpoint, randomEntry(random, uint64(10+random.Intn(3))))
		case 1: // a virtual coin spent since virtual
			diff.toRemove.add(&outpoint, randomEntry(random, uint64(random.Intn(3))))
		case 2: // a virtual coin restamped since virtual
			entry := randomEntry(random, uint64(random.Intn(3)))
			diff.toRemove.add(&outpoint, entry)
			diff.toAdd.add(&outpoint, NewUTXOEntry(entry.Amount(), entry.ScriptPublicKey(), entry.IsCoinbase(),
				uint64(10+random.Intn(3))))
		default: // a virtual coin the diff does not mention
			virtualCoins[outpoint] = randomEntry(random, uint64(random.Intn(3)))
		}
	}
	return diff.ToImmutable(), virtualCoins
}

// mutateLikeResolution applies random transactions, and occasionally a diff, to changing.
func mutateLikeResolution(random *rand.Rand, changing *mutableUTXODiff, base externalapi.UTXODiff,
	virtualCoins map[externalapi.DomainOutpoint]externalapi.UTXOEntry,
) {
	spendable := func() (externalapi.DomainOutpoint, externalapi.UTXOEntry, bool) {
		var candidates []externalapi.DomainOutpoint
		for outpoint := range changing.toAdd {
			candidates = append(candidates, outpoint)
		}
		for outpoint := range virtualCoins {
			candidates = append(candidates, outpoint)
		}
		for outpoint := range changing.toRemove { // spending these again must fail and roll back
			candidates = append(candidates, outpoint)
		}
		if len(candidates) == 0 {
			return externalapi.DomainOutpoint{}, nil, false
		}
		outpoint := candidates[random.Intn(len(candidates))]
		if entry, ok := changing.toAdd.Get(&outpoint); ok {
			return outpoint, entry, true
		}
		if entry, ok := virtualCoins[outpoint]; ok {
			return outpoint, entry, true
		}
		entry, _ := changing.toRemove.Get(&outpoint)
		return outpoint, entry, true
	}

	var applied []*externalapi.DomainTransaction
	for range 1 + random.Intn(8) {
		var transaction *externalapi.DomainTransaction
		if len(applied) > 0 && random.Intn(5) == 0 {
			// The same transaction again: its outputs land on outpoints the diff already holds.
			transaction = applied[random.Intn(len(applied))]
		} else {
			transaction = &externalapi.DomainTransaction{
				SubnetworkID: subnetworks.SubnetworkIDNative,
				Payload:      []byte{byte(random.Intn(256)), byte(random.Intn(256))},
			}
			for range random.Intn(3) {
				outpoint, entry, ok := spendable()
				if !ok {
					break
				}
				transaction.Inputs = append(transaction.Inputs, &externalapi.DomainTransactionInput{
					PreviousOutpoint: outpoint,
					UTXOEntry:        entry,
				})
			}
			for range 1 + random.Intn(3) {
				transaction.Outputs = append(transaction.Outputs, &externalapi.DomainTransactionOutput{
					Value:           uint64(1 + random.Intn(3)),
					ScriptPublicKey: &externalapi.ScriptPublicKey{Script: []byte{byte(random.Intn(2))}},
				})
			}
			consensushashing.TransactionID(transaction)
		}
		daaScore := uint64(20 + random.Intn(2))
		if random.Intn(4) == 0 {
			_ = changing.AddOutputsSpendingResolvedInputs(transaction, daaScore)
		} else {
			_ = changing.AddTransaction(transaction, daaScore)
		}
		applied = append(applied, transaction)
	}

	if random.Intn(5) == 0 {
		other := newMutableUTXODiff()
		outpoint := randomOutpoint(random)
		other.toAdd.add(&outpoint, randomEntry(random, 30))
		_ = changing.WithDiffInPlace(other.ToImmutable())
	}
	// Conflict shapes at outpoints the base already holds: a coin the base removes added back, or a
	// coin the base adds removed, at another DAA score and sometimes with another value, written straight into the
	// collections so they reach diffFrom whatever addEntry/removeEntry would allow.
	if random.Intn(3) == 0 {
		baseDiff := base.(*immutableUTXODiff).mutableUTXODiff
		for outpoint, entry := range baseDiff.toRemove {
			if random.Intn(2) == 0 {
				changing.recordChange(&outpoint)
				changing.toAdd.add(&outpoint, NewUTXOEntry(entry.Amount()+uint64(random.Intn(2)),
					entry.ScriptPublicKey(), entry.IsCoinbase(), entry.BlockDAAScore()+uint64(random.Intn(2))))
			}
			break
		}
		for outpoint, entry := range baseDiff.toAdd {
			if random.Intn(2) == 0 {
				changing.recordChange(&outpoint)
				changing.toRemove.add(&outpoint, NewUTXOEntry(entry.Amount()+uint64(random.Intn(2)),
					entry.ScriptPublicKey(), entry.IsCoinbase(), entry.BlockDAAScore()+uint64(random.Intn(2))))
			}
			break
		}
	}
}
