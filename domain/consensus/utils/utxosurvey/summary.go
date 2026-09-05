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

	FoundInMergesetAdds bool
	FoundInParentSet    bool
	AlwaysAlreadySpent  bool
	AbsentEverywhere    bool
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
	// benign case of a coin this block's own past already spent.
	AbsentEverywhere []OutpointCluster

	// DeltaReasons counts the ways a block's own UTXO delta disagreed with its acceptance data.
	DeltaReasons map[string]int
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
		blocks              int
		preimages           map[string]struct{}
		sources             map[string]struct{}
		foundInMergesetAdds bool
		foundInParentSet    bool
		everNotAlreadySpent bool
		everHadAMatch       bool
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
			if !missing.AlreadySpentInThisPast {
				accumulator.everNotAlreadySpent = true
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
			Outpoint:            key,
			Blocks:              accumulator.blocks,
			Preimages:           sortedKeys(accumulator.preimages),
			Sources:             sortedKeys(accumulator.sources),
			FoundInMergesetAdds: accumulator.foundInMergesetAdds,
			FoundInParentSet:    accumulator.foundInParentSet,
			AlwaysAlreadySpent:  !accumulator.everNotAlreadySpent,
		}
		cluster.AbsentEverywhere = !accumulator.everHadAMatch && !cluster.FoundInParentSet &&
			!cluster.FoundInMergesetAdds && !cluster.AlwaysAlreadySpent

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

	return summary
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
		"  None: every unresolvable outpoint was found somewhere, or was already spent in the block's own past.\n")

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
