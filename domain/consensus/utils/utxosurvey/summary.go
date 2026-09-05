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

	// SpendHistoryIncomplete is true when any record hit its accepted-spends cap, which makes "no
	// spend recorded" weaker than "no spend happened" and CreatedThenLost an upper bound.
	SpendHistoryIncomplete bool

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

	summarizeCreatedThenAbsent(records, summary)

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
		for _, transactionID := range record.AcceptedTxIDs {
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
			// A spend that happened after this block tripped over the coin does not explain anything.
			spentInBetween := wasSpent && spendIndex > created.index && spendIndex < i
			if spentInBetween {
				summary.CreatedThenSpentThenAbsent++
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

	fmt.Fprintf(&b, "=== UTXO survey: %d failing blocks\n", s.Records)
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
	if len(s.CreatedThenLost) == 0 && s.CreatedThenSpentThenAbsent == 0 {
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
