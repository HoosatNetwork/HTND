package pruningmanager

import (
	"sync/atomic"

	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/multiset"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/utxo"
)

// shortHashLen is how much of a hash the one-line status summary prints. Enough to tell three
// hashes apart at a glance, short enough that the whole line fits on a terminal.
const shortHashLen = 16

// shortHash renders a hash for the status line, or "n/a" when the producer could not supply one.
func shortHash(hash *externalapi.DomainHash) string {
	if hash == nil {
		return "n/a"
	}
	full := hash.String()
	if len(full) <= shortHashLen {
		return full
	}
	return full[:shortHashLen]
}

// shouldRunStrictUTXOSetFitCheck reports whether updatePruningPoint should run the fatal
// validateUTXOSetFitsCommitment check - and, equally importantly, whether it is entitled to log
// that it is doing so.
//
// The "Validating the UTXO set fits commitment" line used to sit outside this condition, so it was
// printed on every pruning-point advancement even though the check itself is behind a hidden,
// default-off flag and therefore almost never ran. Operators reading a log had no way to tell the
// difference. Extracted so the condition that gates the claim can be asserted directly.
func (pm *pruningManager) shouldRunStrictUTXOSetFitCheck(pruningPoint *externalapi.DomainHash) bool {
	return pm.shouldSanityCheckPruningUTXOSet && !pruningPoint.Equal(pm.genesisHash)
}

// utxoSetVerificationRunning guards the background bucket scan so that a burst of triggers - a
// startup and a pruning-point advancement landing close together - cannot start two full scans
// over the same data at once.
var utxoSetVerificationRunning atomic.Bool

// RecordPruningPointUTXOSetVerification hashes the served pruning-point UTXO bucket, compares it to
// the pruning point's own header UTXO commitment, and persists the verdict.
//
// It deliberately changes nothing else. On a mismatch it logs at warn, records
// PruningPointUTXOSetUnverified, and returns the verification with a nil error: the bucket keeps
// being stored and keeps being served. Deciding what to do about a bad bucket is a later,
// separately-gated change; this call only makes the fact observable and durable.
func (pm *pruningManager) RecordPruningPointUTXOSetVerification(stagingArea *model.StagingArea) (
	*model.PruningPointUTXOSetVerification, error,
) {
	pruningPoint, err := pm.pruningStore.PruningPoint(pm.databaseContext, stagingArea)
	if err != nil {
		return nil, err
	}

	verification, err := pm.computePruningPointUTXOSetVerification(stagingArea, pruningPoint, false)
	if err != nil {
		return nil, err
	}

	// The bucket scan is not atomic with respect to pruning-point advancement. If the pruning point
	// moved while we were hashing, the entries we saw belong partly to two different sets and the
	// verdict is meaningless - a spurious "unverified" recorded here would be worse than no marker
	// at all, since the whole point of this record is that operators can trust it.
	currentPruningPoint, err := pm.pruningStore.PruningPoint(pm.databaseContext, stagingArea)
	if err != nil {
		return nil, err
	}
	if !currentPruningPoint.Equal(pruningPoint) {
		log.Infof("Pruning point moved from %s to %s while its UTXO set was being verified - discarding "+
			"the result rather than recording a verdict over a set that changed underneath the scan",
			pruningPoint, currentPruningPoint)
		return nil, nil
	}

	err = pm.pruningStore.SetPruningPointUTXOSetVerification(pm.databaseContext, verification)
	if err != nil {
		return nil, err
	}

	if verification.Status == model.PruningPointUTXOSetVerified {
		log.Infof("Pruning point %s UTXO set verified: the served bucket (%d entries) hashes to its own "+
			"header commitment %s", pruningPoint, verification.EntryCount, verification.HeaderCommitment)
		return verification, nil
	}

	log.Warnf("Pruning point %s UTXO set is UNVERIFIED: header commitment is %s but the served bucket "+
		"(%d entries) hashes to %s. This node is serving that bucket to syncing peers anyway - this "+
		"release only records the fact. Per-block multiset for the same pruning point: %s.",
		pruningPoint, verification.HeaderCommitment, verification.EntryCount,
		verification.BucketMultiset, shortHashOrNA(verification.PerBlockMultiset))

	return verification, nil
}

func shortHashOrNA(hash *externalapi.DomainHash) string {
	if hash == nil {
		return "n/a"
	}
	return hash.String()
}

// computePruningPointUTXOSetVerification gathers the hashes for one pruning point without writing
// anything. includeDiffChain additionally walks restorePastUTXO, which is expensive and has been
// observed to abort on a drifted diff chain - it is only ever requested by the flag-gated
// diagnostics path, and a failure there is recorded as "not available", never as an error.
func (pm *pruningManager) computePruningPointUTXOSetVerification(stagingArea *model.StagingArea,
	pruningPoint *externalapi.DomainHash, includeDiffChain bool,
) (*model.PruningPointUTXOSetVerification, error) {
	header, err := pm.blockHeaderStore.BlockHeader(pm.databaseContext, stagingArea, pruningPoint)
	if err != nil {
		return nil, err
	}

	bucketMultiset, entryCount, err := pm.pruningPointBucketMultiset()
	if err != nil {
		return nil, err
	}
	bucketHash := bucketMultiset.Hash()

	verification := &model.PruningPointUTXOSetVerification{
		PruningPoint:      pruningPoint,
		HeaderCommitment:  header.UTXOCommitment(),
		BucketMultiset:    bucketHash,
		Status:            model.PruningPointUTXOSetUnverified,
		EntryCount:        uint64(entryCount),
		CheckedAtDAAScore: pm.currentDAAScoreForMarker(stagingArea, header),
	}
	if header.UTXOCommitment().Equal(bucketHash) {
		verification.Status = model.PruningPointUTXOSetVerified
	}

	// Cheap: one store lookup, no scan. Left nil when unavailable so the status line can say so
	// rather than implying agreement.
	if perBlockMultiset, msErr := pm.multiSetStore.Get(pm.databaseContext, stagingArea, pruningPoint); msErr == nil {
		verification.PerBlockMultiset = perBlockMultiset.Hash()
	} else {
		log.Debugf("Could not read the per-block multiset for pruning point %s: %s", pruningPoint, msErr)
	}

	if includeDiffChain {
		verification.DiffChainMultiset = pm.diffChainMultisetOrNil(stagingArea, pruningPoint)
	}

	return verification, nil
}

// currentDAAScoreForMarker returns the virtual DAA score so a stale marker can be aged, falling
// back to the pruning point's own DAA score when virtual isn't readable. Never fails the caller -
// the marker is diagnostic, and a zero here costs nothing.
func (pm *pruningManager) currentDAAScoreForMarker(stagingArea *model.StagingArea,
	pruningPointHeader externalapi.BlockHeader,
) uint64 {
	daaScore, err := pm.daaBlocksStore.DAAScore(pm.databaseContext, stagingArea, model.VirtualBlockHash)
	if err == nil {
		return daaScore
	}
	log.Debugf("Could not read the virtual DAA score for the pruning point UTXO marker: %s", err)
	return pruningPointHeader.DAAScore()
}

// diffChainMultisetOrNil hashes restorePastUTXO's reconstruction of the pruning point. Returns nil
// on any failure - this walk is exactly the one that has been observed to disagree with both the
// bucket and the header, and it must never take down a caller that only wanted to print a summary.
func (pm *pruningManager) diffChainMultisetOrNil(stagingArea *model.StagingArea,
	pruningPoint *externalapi.DomainHash,
) *externalapi.DomainHash {
	iterator, err := pm.consensusStateManager.RestorePastUTXOSetIterator(stagingArea, pruningPoint)
	if err != nil {
		log.Debugf("Diff-chain reconstruction of pruning point %s is unavailable: %s", pruningPoint, err)
		return nil
	}
	defer iterator.Close()

	diffChainMultiset := multiset.New()
	for ok := iterator.First(); ok; ok = iterator.Next() {
		outpoint, entry, err := iterator.Get()
		if err != nil {
			log.Debugf("Diff-chain reconstruction of pruning point %s aborted mid-walk: %s", pruningPoint, err)
			return nil
		}
		serialized, err := utxo.SerializeUTXO(entry, outpoint)
		if err != nil {
			log.Debugf("Diff-chain reconstruction of pruning point %s could not serialize an entry: %s",
				pruningPoint, err)
			return nil
		}
		diffChainMultiset.Add(serialized)
	}
	return diffChainMultiset.Hash()
}

// LogPruningPointUTXOSetStatus prints the one-line three-hash summary for the current pruning
// point. It runs on every boot with no flag required, so it must stay cheap: it reports the
// persisted marker rather than re-hashing the bucket, and only when the marker is missing or
// belongs to an older pruning point does it start the scan - in the background, so boot is never
// delayed by it.
func (pm *pruningManager) LogPruningPointUTXOSetStatus() {
	stagingArea := model.NewStagingArea()

	hasPruningPoint, err := pm.pruningStore.HasPruningPoint(pm.databaseContext, stagingArea)
	if err != nil || !hasPruningPoint {
		log.Debugf("Pruning point UTXO set status unavailable: no pruning point yet (%v)", err)
		return
	}
	pruningPoint, err := pm.pruningStore.PruningPoint(pm.databaseContext, stagingArea)
	if err != nil {
		log.Debugf("Pruning point UTXO set status unavailable: %s", err)
		return
	}

	headerText := "n/a"
	if header, headerErr := pm.blockHeaderStore.BlockHeader(pm.databaseContext, stagingArea, pruningPoint); headerErr == nil {
		headerText = shortHash(header.UTXOCommitment())
	} else {
		log.Debugf("Could not read the pruning point header for the status line: %s", headerErr)
	}

	perBlockText := "n/a"
	if perBlockMultiset, msErr := pm.multiSetStore.Get(pm.databaseContext, stagingArea, pruningPoint); msErr == nil {
		perBlockText = shortHash(perBlockMultiset.Hash())
	} else {
		log.Debugf("Could not read the per-block multiset for the status line: %s", msErr)
	}

	marker := pm.currentPruningPointUTXOSetMarker(pruningPoint)
	bucketText, diffChainText := "n/a", "n/a"
	status := model.PruningPointUTXOSetUnknown
	ageText := ""
	if marker != nil {
		bucketText = shortHash(marker.BucketMultiset)
		diffChainText = shortHash(marker.DiffChainMultiset)
		status = marker.Status
		ageText = " checkedAtDAA=" + uint64ToString(marker.CheckedAtDAAScore) +
			" entries=" + uint64ToString(marker.EntryCount)
	}

	log.Infof("Pruning point UTXO set: pp=%s header=%s bucket=%s perBlock=%s diffChain=%s marker=%s%s",
		pruningPoint, headerText, bucketText, perBlockText, diffChainText, status, ageText)

	if marker == nil {
		log.Infof("No current pruning point UTXO set verification on record - hashing the served bucket " +
			"in the background. The verdict will be logged when it completes and printed directly on the " +
			"next boot.")
		pm.scheduleUTXOSetVerification()
	}
}

// currentPruningPointUTXOSetMarker returns the persisted marker only when it describes the pruning
// point given. A marker for a previous pruning point is stale, and reporting it as a current
// verdict is precisely the mistake this whole record exists to prevent - see the memoised skip in
// VerifyCurrentPruningPointUTXOSet, which is what motivated storing the numbers in the first place.
func (pm *pruningManager) currentPruningPointUTXOSetMarker(pruningPoint *externalapi.DomainHash) *model.PruningPointUTXOSetVerification {
	marker, err := pm.pruningStore.PruningPointUTXOSetVerification(pm.databaseContext)
	if err != nil {
		log.Debugf("No pruning point UTXO set verification marker on record: %s", err)
		return nil
	}
	if marker.PruningPoint == nil || !marker.PruningPoint.Equal(pruningPoint) {
		log.Debugf("The recorded pruning point UTXO set verification is for %s, but the current pruning "+
			"point is %s - treating it as unknown rather than as a current verdict",
			marker.PruningPoint, pruningPoint)
		return nil
	}
	return marker
}

// scheduleUTXOSetVerification runs RecordPruningPointUTXOSetVerification off the caller's
// goroutine. Used from boot and from pruning-point advancement, neither of which should stall for
// a full UTXO-set scan.
func (pm *pruningManager) scheduleUTXOSetVerification() {
	if !utxoSetVerificationRunning.CompareAndSwap(false, true) {
		log.Debugf("A pruning point UTXO set verification is already running - not starting another")
		return
	}
	go func() {
		defer utxoSetVerificationRunning.Store(false)
		if _, err := pm.RecordPruningPointUTXOSetVerification(model.NewStagingArea()); err != nil {
			log.Warnf("Could not verify the pruning point UTXO set against its header commitment: %s. "+
				"Nothing else is affected - this check is observation only.", err)
		}
	}()
}

// uint64ToString avoids pulling strconv into the status line for two numbers.
func uint64ToString(value uint64) string {
	if value == 0 {
		return "0"
	}
	var digits [20]byte
	position := len(digits)
	for value > 0 {
		position--
		digits[position] = byte('0' + value%10)
		value /= 10
	}
	return string(digits[position:])
}
