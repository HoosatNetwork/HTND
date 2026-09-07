package utxosurvey

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// Read loads a survey file. A malformed line is reported rather than skipped: a survey read wrong
// is worse than one that refuses to be read, because every conclusion below is a count.
func Read(path string) ([]Record, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var records []Record
	scanner := bufio.NewScanner(file)
	// Records carry every alternate match of every missing outpoint, so a single line can be large.
	scanner.Buffer(make([]byte, 0, 64*1024), 64*1024*1024)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var record Record
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			return nil, fmt.Errorf("survey %s line %d is not valid JSON: %w", path, lineNumber, err)
		}
		records = append(records, record)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return records, nil
}

// OutpointCluster is one outpoint and every block that could not resolve it. An outpoint that
// appears in many blocks is one coin poisoning a run, not many independent failures - the single
// most important thing to know before counting anything else.
type OutpointCluster struct {
	Outpoint string
	Blocks   int

	// Preimages is every distinct SerializeUTXO preimage seen for this outpoint across all records.
	// More than one means the coin exists and the copies disagree on its identity bytes.
	Preimages []string

	// Sources names where those copies were found, so a preimage disagreement can be attributed.
	Sources []string

	FoundInMergesetAdds      bool
	FoundInParentSet         bool
	AlwaysAbsentFromPastView bool
	AbsentEverywhere         bool
}

// CascadeSeed is a coin whose absence is not explained by this node having starved the transaction
// that would have created it - the point where following the cascade upstream stops. Repairing a
// seed removes everything downstream of it; repairing anything downstream removes only itself.
type CascadeSeed struct {
	Outpoint string

	// StarvedDirectly is how many transactions could not be accepted because this exact coin was
	// missing. StarvedDownstream adds the transactions starved by coins those transactions would have
	// created, transitively - the full blast radius of this one absent coin.
	StarvedDirectly   int
	StarvedDownstream int
}

// CreatedThenAbsent is a coin an earlier record in the same run says was created, which a later
// record could not resolve. It answers a question no single block's record can reach:
// MissingOutpoint.FoundInMergesetAdds looks only at the failing block's own mergeset, so a coin
// created fifty blocks earlier and then lost reads as ORIGINAL_MISSING - an inherited snapshot gap -
// when it is in fact NEW_MISSING, a coin this node created and then dropped. Those two point at
// completely different code, so the difference decides what gets fixed.
//
// SpentInBetween separates the two readings. A coin created, spent by an accepted transaction, and
// only then reported absent is an ordinary double-spend rejection. A coin created, never spent, and
// then absent was lost.
type CreatedThenAbsent struct {
	Outpoint       string
	CreatedAtBlock string
	AbsentAtBlock  string
	SpentInBetween bool
}

// Summary is the answer to the questions the survey exists to ask, computed rather than eyeballed.
type Summary struct {
	Records int

	ByError          map[string]int
	ByClassification map[string]int
	ByStage          map[string]int

	// DAABands counts failures per 10,000 DAA scores, which is what separates "one block" from
	// "a dense band" without needing a plot.
	DAABands map[uint64]int

	// ImportRecords are the pruning-utxo-import records. A run that has one is a run whose every
	// later failure has to be read as inheriting from it.
	ImportRecords []Record

	// OffsetOriginBlocks are records whose selected parent's stored multiset agrees with its own
	// header, i.e. blocks where the offset appears rather than blocks carrying one. On a run whose
	// import was already offset this is normally empty; a block here is where drift entered.
	OffsetOriginBlocks []Record

	// RepeatedOutpoints are outpoints more than one block could not resolve, most-blocked first.
	RepeatedOutpoints []OutpointCluster

	// DisagreeingPreimages are outpoints whose copies do not serialize identically. These are the
	// proof of a handling mismatch: the coin is present, the nodes disagree on its spelling.
	DisagreeingPreimages []OutpointCluster

	// AbsentEverywhere are outpoints no source holds and nothing accepted creates, excluding the
	// coins absent only from the failing block's own past view.
	AbsentEverywhere []OutpointCluster

	// DeltaReasons counts the ways a block's own UTXO delta disagreed with its acceptance data.
	DeltaReasons map[string]int

	// CreatedThenLost are coins an earlier record created, no record spent, and a later record could
	// not resolve. These are NEW_MISSING at run scope whatever the per-block classification said.
	CreatedThenLost []CreatedThenAbsent

	// CreatedThenSpentThenAbsent counts coins that were spent in between - ordinary double-spend
	// rejections rather than losses - so the two are never conflated.
	CreatedThenSpentThenAbsent int

	// AmbiguousCreation counts unresolvable coins whose creating transaction does not have one settled
	// creation in this run: it was accepted by more than one block, or accepted and also rejected
	// somewhere. On a DAG that is ordinary - a reorg re-accepts a transaction on the new chain, and
	// two blocks with byte-identical coinbases share a transaction ID - but it means "created at" has
	// no single answer, so nothing can be concluded about the coin going absent later.
	AmbiguousCreation int

	// SpendHistoryIncomplete is true when any record hit its accepted-spends cap, which makes "no
	// spend recorded" weaker than "no spend happened" and CreatedThenLost an upper bound.
	SpendHistoryIncomplete bool

	// VerifiedBlocks is how many blocks passed every UTXO check, from the checkpoint records. It is
	// what makes a clean run distinguishable from an unwatched one.
	VerifiedBlocks int

	// Runs is how many distinct node processes wrote into this survey file. More than one means the
	// file spans several syncs, and the run-scope analysis was performed within each rather than
	// across them.
	Runs int

	// RejectionReasons counts, over the whole run, why merge-set transactions were not accepted.
	RejectionReasons map[string]int

	// LostAfterCreation are unresolvable coins this node created exactly once, no record spent, and
	// which no single block's vanished acceptance explains - what is left after every ordinary
	// explanation has been taken away.
	LostAfterCreation int

	// LostByVanishedAcceptance counts coins excluded from the above because the block that accepted
	// them also accounts for several other apparent losses. A whole block's acceptance disappearing at
	// once is a reorg - the block left the selected chain and the coins it created correctly ceased to
	// exist - not this node losing coins one at a time. On a live survey 38 apparent losses collapsed
	// to a handful of blocks this way, nine of them to one block.
	LostByVanishedAcceptance int

	// LostWithAmbiguousCreation counts coins excluded because their creating transaction was accepted
	// by more than one block, so "created once and then absent" is not a statement this run can make.
	// The created-then-absent pass has always excluded these; this one did not, and reported them as
	// losses.
	LostWithAmbiguousCreation int

	// DoubleSpendMissing are unresolvable coins that were created and then legitimately spent before
	// the block that went looking for them. Ordinary DAG behaviour and the commonest confounder here:
	// counted separately so it can never be mistaken for damage.
	DoubleSpendMissing int

	// SelfInflictedMissing are unresolvable coins whose creating transaction this node rejected for a
	// missing input AND never managed to accept anywhere. They are the gap spreading: an earlier absent coin made that transaction
	// unacceptable, so the coins it would have created were never created either, and they are
	// indistinguishable from an inherited gap in every other measurement.
	SelfInflictedMissing int

	// InheritedMissing are unresolvable coins whose creating transaction this run never saw at all -
	// created below the pruning point or outside the surveyed window. These are the gap that arrived
	// with the imported set.
	InheritedMissing int

	// CascadeSeeds are the coins the cascade terminates at: absent, spent by a transaction this node
	// could not accept, and NOT themselves created by any transaction this node starved. Everything
	// else in the cascade is downstream of one of these. Ordered by how many starved transactions
	// trace back to each.
	CascadeSeeds []CascadeSeed

	// CascadeDepth is the longest chain of starved transactions walked back from any coin - 1 means
	// every starved transaction was starved by a seed directly, higher means the gap is feeding on
	// coins it destroyed itself.
	CascadeDepth int

	// SpendHistoryAbsent is true when no record carries any accepted-spend data at all, while records
	// do carry accepted transactions. A survey written before the field existed looks exactly like a
	// run in which nothing was ever spent, and the difference is the whole of CreatedThenLost: with no
	// spend history, an ordinary double-spend rejection is indistinguishable from a lost coin. The
	// count is then not evidence of anything and must not be read as NEW_MISSING.
	SpendHistoryAbsent bool
}

// Summarize clusters a whole run. It answers, in order: how many failures and of what kind; whether
// they are one block or a band; whether the pruning-point import was already offset; whether later
// failures keep tripping over the same coins; and whether those coins are missing or merely spelled
// differently.
func Summarize(records []Record) *Summary {
	summary := &Summary{
		Records:          len(records),
		ByError:          map[string]int{},
		ByClassification: map[string]int{},
		ByStage:          map[string]int{},
		DAABands:         map[uint64]int{},
		DeltaReasons:     map[string]int{},
	}

	type outpointAccumulator struct {
		blocks                    int
		preimages                 map[string]struct{}
		sources                   map[string]struct{}
		foundInMergesetAdds       bool
		foundInParentSet          bool
		everNotAbsentFromPastView bool
		everHadAMatch             bool
	}
	accumulators := map[string]*outpointAccumulator{}
	order := []string{}

	for _, record := range records {
		if record.IBDStage == StageVerified {
			summary.VerifiedBlocks += record.VerifiedBlocks
			summary.Records--
			continue
		}
		summary.ByError[record.Error]++
		summary.ByClassification[record.Classification]++
		summary.ByStage[record.IBDStage]++
		summary.DAABands[record.DAAScore/10000*10000]++

		if record.IBDStage == StagePruningUTXOImport {
			summary.ImportRecords = append(summary.ImportRecords, record)
		}
		// A block whose parent agrees with its own header did not inherit its offset.
		if record.IBDStage == StageChainReplay && record.ParentStoredMultiset != "" &&
			record.ParentHeaderUTXOCommitment != "" &&
			record.ParentStoredMultiset == record.ParentHeaderUTXOCommitment {
			summary.OffsetOriginBlocks = append(summary.OffsetOriginBlocks, record)
		}

		for _, element := range record.ExtraAddsNotInHeaderView {
			summary.DeltaReasons[element.Reason]++
		}
		for _, element := range record.ExtraRemovesNotInHeaderView {
			summary.DeltaReasons[element.Reason]++
		}

		for _, missing := range record.MissingOutpoints {
			key := fmt.Sprintf("%s:%d", missing.TxID, missing.Index)
			accumulator, seen := accumulators[key]
			if !seen {
				accumulator = &outpointAccumulator{
					preimages: map[string]struct{}{},
					sources:   map[string]struct{}{},
				}
				accumulators[key] = accumulator
				order = append(order, key)
			}
			accumulator.blocks++
			accumulator.foundInMergesetAdds = accumulator.foundInMergesetAdds || missing.FoundInMergesetAdds
			accumulator.foundInParentSet = accumulator.foundInParentSet || missing.FoundInParentSet
			if !missing.AbsentFromBlocksPastView {
				accumulator.everNotAbsentFromPastView = true
			}
			for _, match := range missing.AlternateMatches {
				accumulator.everHadAMatch = true
				if match.SerializedUTXO != "" {
					accumulator.preimages[match.SerializedUTXO] = struct{}{}
				}
				accumulator.sources[match.Source] = struct{}{}
			}
		}
	}

	for _, key := range order {
		accumulator := accumulators[key]
		cluster := OutpointCluster{
			Outpoint:                 key,
			Blocks:                   accumulator.blocks,
			Preimages:                sortedKeys(accumulator.preimages),
			Sources:                  sortedKeys(accumulator.sources),
			FoundInMergesetAdds:      accumulator.foundInMergesetAdds,
			FoundInParentSet:         accumulator.foundInParentSet,
			AlwaysAbsentFromPastView: !accumulator.everNotAbsentFromPastView,
		}
		cluster.AbsentEverywhere = !accumulator.everHadAMatch && !cluster.FoundInParentSet &&
			!cluster.FoundInMergesetAdds && !cluster.AlwaysAbsentFromPastView

		if cluster.Blocks > 1 {
			summary.RepeatedOutpoints = append(summary.RepeatedOutpoints, cluster)
		}
		if len(cluster.Preimages) > 1 {
			summary.DisagreeingPreimages = append(summary.DisagreeingPreimages, cluster)
		}
		if cluster.AbsentEverywhere {
			summary.AbsentEverywhere = append(summary.AbsentEverywhere, cluster)
		}
	}
	sort.SliceStable(summary.RepeatedOutpoints, func(i, j int) bool {
		return summary.RepeatedOutpoints[i].Blocks > summary.RepeatedOutpoints[j].Blocks
	})

	// Scoped per run: a survey file is appended to, so one file routinely holds several syncs, and
	// after a --reset-db the database between them is not even the same database. Comparing across
	// that boundary turns every coin the next sync had not re-created yet into a lost coin.
	byRun := map[string][]Record{}
	runOrder := []string{}
	for _, record := range records {
		if _, seen := byRun[record.RunID]; !seen {
			runOrder = append(runOrder, record.RunID)
		}
		byRun[record.RunID] = append(byRun[record.RunID], record)
	}
	summary.Runs = len(runOrder)
	for _, id := range runOrder {
		summarizeCreatedThenAbsent(byRun[id], summary)
		summarizeGapOrigin(byRun[id], summary)
		summarizeCascade(byRun[id], summary)
	}

	return summary
}

// summarizeCreatedThenAbsent walks the run in order and finds coins that some record says were
// created, that no record says were spent, and that a later record could not resolve.
//
// This is the A-versus-B question, and it cannot be answered one record at a time. A record's
// FoundInMergesetAdds only covers the block's own mergeset, so a coin created earlier in the same
// sync and then dropped is filed as ORIGINAL_MISSING - "the pruning point snapshot never had it" -
// which points the investigation at the import when the loss actually happened here, on this node,
// while it was syncing. Only the run as a whole shows the creation and the absence together.
//
// Records are consumed in file order, which is resolution order, so "created before it went
// missing" is decided by position rather than by DAA score - a block's DAA score says when it was
// mined, not when this node resolved it.
func summarizeCreatedThenAbsent(records []Record, summary *Summary) {
	type creation struct {
		blockHash string
		index     int
	}
	createdAt := map[string]creation{}
	spentAt := map[string]int{}
	// A transaction accepted by more than one block, or accepted and also rejected, has no single
	// creation point in this run. Both happen normally on a DAG and both make "created here, absent
	// later" meaningless for the coins involved.
	acceptedCount := map[string]int{}
	everRejected := map[string]bool{}

	// First pass: when each coin was created and when it was first spent by an accepted transaction.
	anySpendsRecorded, anyAcceptanceRecorded := false, false
	for i, record := range records {
		if record.AcceptedSpendsTruncated > 0 {
			summary.SpendHistoryIncomplete = true
		}
		if len(record.AcceptedSpends) > 0 {
			anySpendsRecorded = true
		}
		if len(record.AcceptedTxIDs) > 0 {
			anyAcceptanceRecorded = true
		}
		for _, spend := range record.AcceptedSpends {
			if _, seen := spentAt[spend]; !seen {
				spentAt[spend] = i
			}
		}
		for _, transactionID := range record.RejectedOrRedTxIDs {
			everRejected[transactionID] = true
		}
		for _, transactionID := range record.AcceptedTxIDs {
			acceptedCount[transactionID]++
			// A record lists the transactions it accepted, not the outpoints they create, so a coin is
			// keyed back to its creating transaction and matched by transaction ID below.
			if _, seen := createdAt[transactionID]; !seen {
				createdAt[transactionID] = creation{blockHash: record.BlockHash, index: i}
			}
		}
	}

	summary.SpendHistoryAbsent = anyAcceptanceRecorded && !anySpendsRecorded

	// Second pass: every unresolvable coin whose creating transaction was accepted earlier.
	reported := map[string]struct{}{}
	for i, record := range records {
		for _, missing := range record.MissingOutpoints {
			created, wasCreated := createdAt[missing.TxID]
			if !wasCreated || created.index >= i {
				continue
			}
			key := fmt.Sprintf("%s:%d", missing.TxID, missing.Index)
			if _, alreadyReported := reported[key]; alreadyReported {
				continue
			}
			reported[key] = struct{}{}

			spendIndex, wasSpent := spentAt[key]
			// A spend recorded after this block tripped over the coin does not explain anything, but a
			// spend recorded BY this block does: the commonest shape of all is one mergeset containing
			// two transactions that spend the same coin, where the first is accepted and the second
			// correctly fails with a missing input. That lands the spend and the failure in the same
			// record, so the bound has to include it - requiring a strictly earlier record reports the
			// most ordinary double spend there is as a lost coin.
			spentInBetween := wasSpent && spendIndex > created.index && spendIndex <= i
			if spentInBetween {
				summary.CreatedThenSpentThenAbsent++
				continue
			}
			// Checked only after the spend, because a recorded spend explains the coin outright
			// whatever else is true of its creation, and shelving those as "cannot say" would hide the
			// evidence that this pass detects what it claims to detect.
			if acceptedCount[missing.TxID] > 1 || everRejected[missing.TxID] {
				summary.AmbiguousCreation++
				continue
			}
			summary.CreatedThenLost = append(summary.CreatedThenLost, CreatedThenAbsent{
				Outpoint:       key,
				CreatedAtBlock: created.blockHash,
				AbsentAtBlock:  record.BlockHash,
				SpentInBetween: false,
			})
		}
	}
}

// summarizeGapOrigin splits the run's unresolvable coins into the two that matter: coins this node
// failed to create because it had already lost their transaction's input, and coins it never saw
// created at all.
//
// They look identical everywhere else. Both are simply absent, with no entry anywhere and nothing in
// the failing block's mergeset creating them, so both land under ORIGINAL_MISSING and both read as
// "the snapshot never had it". The difference decides what a fix has to do: an inherited gap needs a
// correct set from outside, while a self-inflicted one means the node is still manufacturing new gaps
// from the old one and will do it again to any clean set it is given.
func summarizeGapOrigin(records []Record, summary *Summary) {
	// Position of the first time each fact is seen, in resolution order. Ordering is the whole
	// measurement: a transaction accepted after a coin went missing explains nothing about why it was
	// missing, and neither does a spend that happened later.
	firstAccepted := map[string]int{}
	firstSpent := map[string]int{}
	acceptedTimes := map[string]int{}
	acceptingBlock := map[string]string{}
	starvedAndNeverAccepted := map[string]struct{}{}
	position := 0
	for _, record := range records {
		for _, transactionID := range record.AcceptedTxIDs {
			acceptedTimes[transactionID]++
			if _, seen := firstAccepted[transactionID]; !seen {
				firstAccepted[transactionID] = position
				acceptingBlock[transactionID] = record.BlockHash
			}
			position++
		}
		for _, spend := range record.AcceptedSpends {
			if _, seen := firstSpent[spend]; !seen {
				firstSpent[spend] = position
			}
			position++
		}
		for _, transactionID := range record.RejectedForMissingInputTxIDs {
			starvedAndNeverAccepted[transactionID] = struct{}{}
		}
		for reason, count := range record.RejectionReasons {
			if summary.RejectionReasons == nil {
				summary.RejectionReasons = map[string]int{}
			}
			summary.RejectionReasons[reason] += count
		}
	}
	// A transaction rejected for a missing input in one block and accepted in another is ordinary DAG
	// duplicate handling, not a starved transaction: the coins it creates exist. Only one this node
	// never managed to accept anywhere actually failed to create anything.
	for transactionID := range firstAccepted {
		delete(starvedAndNeverAccepted, transactionID)
	}

	// Apparent losses are attributed to the block that accepted them first, so that a block whose
	// whole acceptance vanished can be recognised as one event rather than counted as many losses.
	type apparentLoss struct {
		outpoint       string
		acceptingBlock string
	}
	var apparentLosses []apparentLoss

	counted := map[string]struct{}{}
	secondPosition := 0
	for _, record := range records {
		secondPosition += len(record.AcceptedTxIDs) + len(record.AcceptedSpends)
		for _, missing := range record.MissingOutpoints {
			key := fmt.Sprintf("%s:%d", missing.TxID, missing.Index)
			if _, already := counted[key]; already {
				continue
			}
			counted[key] = struct{}{}

			acceptedAt, wasAccepted := firstAccepted[missing.TxID]
			if wasAccepted && acceptedAt < secondPosition {
				// The coin was created. Either it was spent before this block wanted it - an ordinary
				// double spend - or it went missing after being created, which may be a real loss.
				spentAt, wasSpent := firstSpent[key]
				if wasSpent && spentAt > acceptedAt && spentAt <= secondPosition {
					summary.DoubleSpendMissing++
					continue
				}
				if acceptedTimes[missing.TxID] > 1 {
					// Accepted by more than one block: "created once and then absent" is not something
					// this run can say about it. The created-then-absent pass excludes these too.
					summary.LostWithAmbiguousCreation++
					continue
				}
				apparentLosses = append(apparentLosses,
					apparentLoss{outpoint: key, acceptingBlock: acceptingBlock[missing.TxID]})
				continue
			}
			if _, starved := starvedAndNeverAccepted[missing.TxID]; starved {
				summary.SelfInflictedMissing++
				continue
			}
			if !wasAccepted {
				summary.InheritedMissing++
			}
		}
	}

	// A block whose whole acceptance disappears at once is a reorg: it left the selected chain and the
	// coins it created correctly ceased to exist. Counting each of those coins as a separate loss
	// turns one ordinary event into a pile of findings - on a live survey 38 apparent losses collapsed
	// to a handful of blocks, nine of them to a single block. Only a coin whose accepting block is not
	// implicated in several others is left standing as a loss.
	const vanishedAcceptanceThreshold = 3
	perBlock := map[string]int{}
	for _, loss := range apparentLosses {
		perBlock[loss.acceptingBlock]++
	}
	for _, loss := range apparentLosses {
		if loss.acceptingBlock != "" && perBlock[loss.acceptingBlock] >= vanishedAcceptanceThreshold {
			summary.LostByVanishedAcceptance++
			continue
		}
		summary.LostAfterCreation++
	}
}

// summarizeCascade walks the starvation chain back to where it starts.
//
// Each starved transaction names the coins it could not find. A coin is itself created by some
// transaction; if that transaction was also starved, the coin is a link and not a cause. Following
// that relation upstream terminates at coins nothing in this run explains - the seeds - and those
// are the only coins whose repair removes anything but themselves. Everything else in the cascade
// disappears on its own once its seed is restored.
func summarizeCascade(records []Record, summary *Summary) {
	// txid -> the coins that transaction could not find
	//
	// Deduplicated per transaction rather than accumulated across every block that carried it. The
	// same transaction appears in many blocks on a DAG, and each occurrence reports whatever was
	// missing in that block's view; unioning them attributes to one transaction a set of misses no
	// single evaluation of it ever had, which manufactures edges - and, through them, cycles that
	// cannot exist in a real dependency graph, since a transaction cannot spend its own output. On a
	// live survey that left 180 chains unresolvable as "cycle or too deep".
	starvedBy := map[string]map[string]struct{}{}
	// outpoint -> the transactions it starved
	starves := map[string][]string{}
	seenEdge := map[string]struct{}{}
	for _, record := range records {
		for _, starved := range record.StarvedTransactions {
			for _, outpoint := range starved.MissingOutpoints {
				if starvedBy[starved.TxID] == nil {
					starvedBy[starved.TxID] = map[string]struct{}{}
				}
				starvedBy[starved.TxID][outpoint] = struct{}{}
				edge := starved.TxID + "<-" + outpoint
				if _, duplicate := seenEdge[edge]; duplicate {
					continue
				}
				seenEdge[edge] = struct{}{}
				starves[outpoint] = append(starves[outpoint], starved.TxID)
			}
		}
	}
	if len(starves) == 0 {
		return
	}

	// A coin is a seed unless the transaction that would have created it was itself starved.
	seeds := map[string]struct{}{}
	for outpoint := range starves {
		creatingTx, _, found := strings.Cut(outpoint, ":")
		if !found {
			continue
		}
		if _, wasStarved := starvedBy[creatingTx]; !wasStarved {
			seeds[outpoint] = struct{}{}
		}
	}

	// Index the absent coins by the transaction that would have created them, once. Walking the
	// cascade without this means re-scanning every absent coin for every step of every walk, which on
	// a real survey does not finish.
	outpointsByCreatingTx := map[string][]string{}
	for outpoint := range starves {
		if creatingTx, _, found := strings.Cut(outpoint, ":"); found {
			outpointsByCreatingTx[creatingTx] = append(outpointsByCreatingTx[creatingTx], outpoint)
		}
	}

	// Rank first, walk second. The blast radius of a seed is only interesting for the seeds that
	// starve the most transactions, and a survey where every absence is inherited produces tens of
	// thousands of seeds that each starve one or two - walking all of them means re-traversing the
	// same shared subgraph tens of thousands of times, which does not finish. Ranking by direct
	// starvation is cheap, deterministic, and puts the seeds worth understanding at the top.
	for outpoint := range seeds {
		summary.CascadeSeeds = append(summary.CascadeSeeds, CascadeSeed{
			Outpoint: outpoint, StarvedDirectly: len(starves[outpoint]),
		})
	}
	sort.SliceStable(summary.CascadeSeeds, func(i, j int) bool {
		a, b := summary.CascadeSeeds[i], summary.CascadeSeeds[j]
		if a.StarvedDirectly != b.StarvedDirectly {
			return a.StarvedDirectly > b.StarvedDirectly
		}
		return a.Outpoint < b.Outpoint
	})

	const seedsToWalk = 50
	for i := range summary.CascadeSeeds {
		if i >= seedsToWalk {
			break
		}
		seed := &summary.CascadeSeeds[i]
		visited := map[string]struct{}{seed.Outpoint: {}}
		frontier := append([]string{}, starves[seed.Outpoint]...)
		depth := 0
		for len(frontier) > 0 && depth < 64 {
			depth++
			var next []string
			for _, starvedTx := range frontier {
				// Every coin that transaction would have created is now absent too, so anything
				// spending one of them was starved by this same seed. A coin is visited once, so a
				// shared subgraph is walked once per seed rather than revisited.
				for _, downstreamOutpoint := range outpointsByCreatingTx[starvedTx] {
					if _, seen := visited[downstreamOutpoint]; seen {
						continue
					}
					visited[downstreamOutpoint] = struct{}{}
					seed.StarvedDownstream += len(starves[downstreamOutpoint])
					next = append(next, starves[downstreamOutpoint]...)
				}
			}
			frontier = next
		}
		if depth > summary.CascadeDepth {
			summary.CascadeDepth = depth
		}
	}
}

func sortedKeys(set map[string]struct{}) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// String renders the summary as the classification table the investigation is supposed to produce
// before anything gets patched, with each section saying what it means rather than only what it
// counted.
func (s *Summary) String() string {
	var b strings.Builder

	fmt.Fprintf(&b, "=== UTXO survey: %d failing blocks", s.Records)
	if s.VerifiedBlocks > 0 {
		fmt.Fprintf(&b, ", %d verified and sound", s.VerifiedBlocks)
	}
	b.WriteString("\n")
	if s.Records == 0 && s.VerifiedBlocks > 0 {
		fmt.Fprintf(&b, "  Nothing failed, and this is not silence: %d blocks were checked and passed,\n"+
			"  including their UTXO commitments.\n", s.VerifiedBlocks)
	}
	if s.Runs > 1 {
		fmt.Fprintf(&b, "  Spans %d separate node runs. Counts below are over the whole file; the\n"+
			"  created-then-absent analysis is scoped within each run, because a coin created in one\n"+
			"  sync and absent in the next says nothing about either.\n", s.Runs)
	}
	if s.Records == 0 && s.VerifiedBlocks > 0 {
		return b.String()
	}
	if s.Records == 0 {
		b.WriteString("  Nothing recorded. Either no block failed, or the survey was not enabled for the run\n" +
			"  (HTND_UTXO_SURVEY) - those are very different findings, so check the node's log for the\n" +
			"  line naming the survey file before concluding the sync was clean.\n")
		return b.String()
	}

	writeCounts(&b, "by error", s.ByError)
	writeCounts(&b, "by classification", s.ByClassification)
	writeCounts(&b, "by IBD stage", s.ByStage)

	bandNoun := "bands"
	if len(s.DAABands) == 1 {
		bandNoun = "band"
	}
	fmt.Fprintf(&b, "\n--- distribution (failures per 10k DAA scores, %d %s)\n", len(s.DAABands), bandNoun)
	bands := make([]uint64, 0, len(s.DAABands))
	for band := range s.DAABands {
		bands = append(bands, band)
	}
	sort.Slice(bands, func(i, j int) bool { return bands[i] < bands[j] })
	for _, band := range bands {
		fmt.Fprintf(&b, "  %10d  %6d\n", band, s.DAABands[band])
	}
	if len(bands) == 1 {
		b.WriteString("  One band: the failures are concentrated, not spread over the sync.\n")
	}

	b.WriteString("\n--- pruning point UTXO import\n")
	if len(s.ImportRecords) == 0 {
		b.WriteString("  No import record. The failures did not start at the import - or this run did not\n" +
			"  import a pruning point at all (a resumed sync, or a survey of an existing database).\n")
	}
	for _, record := range s.ImportRecords {
		fmt.Fprintf(&b, "  %s (%s)\n    header     %s\n    calculated %s\n    %s\n",
			record.BlockHash, record.Error, record.HeaderUTXOCommitment, record.CalculatedUTXOCommitment,
			record.Notes)
		b.WriteString("  The imported set is the baseline every later record inherits: MuHash is homomorphic,\n" +
			"  so an offset here propagates to every block resolved forward, unchanged.\n")
	}

	b.WriteString("\n--- where the offset enters the chain\n")
	if len(s.OffsetOriginBlocks) == 0 {
		b.WriteString("  No block whose selected parent agrees with its own header. Every failing block\n" +
			"  inherited its offset from its parent; nothing in the surveyed range created one.\n")
	}
	for _, record := range s.OffsetOriginBlocks {
		fmt.Fprintf(&b, "  %s daaScore=%d (%s / %s) - its parent's multiset matches its parent's header,\n"+
			"    so the offset appears at this block\n",
			record.BlockHash, record.DAAScore, record.Error, record.Classification)
	}

	writeClusters(&b, "repeated missing outpoints (one coin poisoning many blocks)", s.RepeatedOutpoints,
		"  None: no outpoint failed in more than one block.\n")
	writeClusters(&b, "outpoints whose copies do not serialize identically (HANDLING, not loss)",
		s.DisagreeingPreimages,
		"  None: wherever a missing outpoint was found at all, every copy of it serialized identically.\n")
	writeClusters(&b, "outpoints absent from every source", s.AbsentEverywhere,
		"  None: every unresolvable outpoint was found somewhere, or was absent only from the failing "+
			"block's own past view.\n")

	b.WriteString("\n--- coins created earlier in this run and then unresolvable (run scope)\n")
	if len(s.CreatedThenLost) == 0 && s.CreatedThenSpentThenAbsent == 0 && s.AmbiguousCreation == 0 {
		b.WriteString("  None: no unresolvable coin was created by anything this run accepted. Every missing\n" +
			"  coin predates the surveyed range, which is what an inherited snapshot gap looks like.\n")
	} else {
		if s.SpendHistoryAbsent {
			fmt.Fprintf(&b, "  %d created earlier in this run, then unresolvable - BUT THIS RUN RECORDED NO\n"+
				"  SPENDS AT ALL, so a coin that was simply spent in between is indistinguishable from one\n"+
				"  that was lost. This number is NOT evidence of NEW_MISSING. Re-run with a build that\n"+
				"  records acceptedSpends to tell the two apart.\n", len(s.CreatedThenLost))
		} else {
			fmt.Fprintf(&b, "  %d created, never spent, then unresolvable - NEW_MISSING at run scope: this node\n"+
				"    created these coins and then could not find them, whatever the per-block classification said.\n",
				len(s.CreatedThenLost))
			fmt.Fprintf(&b, "  %d created, spent in between, then unresolvable - ordinary double-spend rejections.\n",
				s.CreatedThenSpentThenAbsent)
		}
		if s.AmbiguousCreation > 0 {
			fmt.Fprintf(&b, "  %d excluded: their creating transaction was accepted by more than one block,\n"+
				"  or accepted and also rejected, so it has no single creation point in this run (a reorg, or\n"+
				"  two blocks sharing a byte-identical coinbase). Nothing can be concluded about these.\n",
				s.AmbiguousCreation)
		}
		if s.SpendHistoryIncomplete {
			b.WriteString("  NOTE: at least one record hit its accepted-spends cap, so some coins counted as\n" +
				"  never-spent may have been spent by a transaction the survey did not record. Treat the\n" +
				"  first number as an upper bound and re-run with HTND_UTXO_SURVEY_MAX_TXIDS=0 to settle it.\n")
		}
		for i, coin := range s.CreatedThenLost {
			if i == 20 {
				fmt.Fprintf(&b, "  ... and %d more\n", len(s.CreatedThenLost)-20)
				break
			}
			fmt.Fprintf(&b, "  %s  created by %s, unresolvable at %s\n",
				coin.Outpoint, coin.CreatedAtBlock, coin.AbsentAtBlock)
		}
	}

	if s.SelfInflictedMissing+s.InheritedMissing+s.LostAfterCreation+s.DoubleSpendMissing > 0 {
		b.WriteString("\n--- where the gap came from\n")
		fmt.Fprintf(&b, "  %6d inherited: creating transaction never accepted in this run, so the coin\n"+
			"           predates the surveyed window and arrived absent with the imported set.\n", s.InheritedMissing)
		fmt.Fprintf(&b, "  %6d self-inflicted: creating transaction was rejected for a missing input and\n"+
			"           never accepted anywhere, so this node failed to create the coin itself.\n",
			s.SelfInflictedMissing)
		fmt.Fprintf(&b, "  %6d LOST: created once, never spent, then absent, and not explained by a\n"+
			"           whole block's acceptance vanishing.\n", s.LostAfterCreation)
		fmt.Fprintf(&b, "  %6d excluded as a reorg: their accepting block accounts for several apparent\n"+
			"           losses at once, which is a block leaving the chain rather than coins going missing.\n",
			s.LostByVanishedAcceptance)
		fmt.Fprintf(&b, "  %6d excluded as ambiguous: creating transaction accepted by more than one block.\n",
			s.LostWithAmbiguousCreation)
		fmt.Fprintf(&b, "  %6d ordinary double spends: created, spent, then wanted again. Not damage.\n",
			s.DoubleSpendMissing)
		if s.SelfInflictedMissing > 0 || s.LostAfterCreation > 0 {
			b.WriteString("  A non-zero self-inflicted or LOST count means repairing the UTXO set is not\n" +
				"  sufficient on its own - this node is still producing absences of its own.\n")
		} else {
			b.WriteString("  Nothing self-inflicted and nothing lost: every absence predates the surveyed\n" +
				"  window, which is what purely inherited damage looks like. Repairing the set addresses it.\n")
		}
	}

	if len(s.CascadeSeeds) > 0 {
		fmt.Fprintf(&b, "\n--- what the cascade starts from (%d seeds, longest chain %d deep)\n",
			len(s.CascadeSeeds), s.CascadeDepth)
		b.WriteString("  Coins absent for a reason other than this node having starved the transaction that\n" +
			"  would have created them. Everything else in the cascade is downstream of one of these, so\n" +
			"  these are the only coins whose repair removes more than itself.\n")
		for i, seed := range s.CascadeSeeds {
			if i == 20 {
				fmt.Fprintf(&b, "  ... and %d more\n", len(s.CascadeSeeds)-20)
				break
			}
			fmt.Fprintf(&b, "  %s  starved %d tx directly, %d downstream\n",
				seed.Outpoint, seed.StarvedDirectly, seed.StarvedDownstream)
		}
	}

	if len(s.RejectionReasons) > 0 {
		writeCounts(&b, "why merge-set transactions were not accepted", s.RejectionReasons)
	}

	if len(s.DeltaReasons) > 0 {
		writeCounts(&b, "block delta vs its own acceptance data", s.DeltaReasons)
	}

	return b.String()
}

func writeCounts(b *strings.Builder, title string, counts map[string]int) {
	fmt.Fprintf(b, "\n--- %s\n", title)
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if counts[keys[i]] != counts[keys[j]] {
			return counts[keys[i]] > counts[keys[j]]
		}
		return keys[i] < keys[j]
	})
	for _, key := range keys {
		label := key
		if label == "" {
			label = "(none)"
		}
		fmt.Fprintf(b, "  %6d  %s\n", counts[key], label)
	}
}

func writeClusters(b *strings.Builder, title string, clusters []OutpointCluster, emptyMessage string) {
	fmt.Fprintf(b, "\n--- %s\n", title)
	if len(clusters) == 0 {
		b.WriteString(emptyMessage)
		return
	}
	for i, cluster := range clusters {
		if i == 20 {
			fmt.Fprintf(b, "  ... and %d more\n", len(clusters)-20)
			break
		}
		fmt.Fprintf(b, "  %s  blocks=%d parentSet=%t mergesetAdds=%t sources=%v\n",
			cluster.Outpoint, cluster.Blocks, cluster.FoundInParentSet, cluster.FoundInMergesetAdds,
			cluster.Sources)
		for _, preimage := range cluster.Preimages {
			fmt.Fprintf(b, "      preimage %s\n", preimage)
		}
	}
}
