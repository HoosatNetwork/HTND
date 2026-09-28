package muhashjournal

import (
	"bufio"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/multiset"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/utxo"
)

// Read loads a journal. A malformed line is an error rather than skipped: an analysis over a
// silently shortened journal would point at the wrong block.
func Read(path string) ([]*Record, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var records []*Record
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 1024*1024), 256*1024*1024)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		record := &Record{}
		if err := json.Unmarshal([]byte(line), record); err != nil {
			return nil, fmt.Errorf("journal %s line %d is not valid JSON: %w", path, lineNumber, err)
		}
		records = append(records, record)
	}
	return records, scanner.Err()
}

// ParentMultisetState rebuilds the MuHash state the record's ops were applied to.
func (r *Record) ParentMultisetState() (model.Multiset, error) {
	serialized, err := hex.DecodeString(r.ParentMultisetSerialized)
	if err != nil {
		return nil, err
	}
	return multiset.FromBytes(serialized)
}

// Replay applies the record's ops to its parent multiset and returns the result.
func (r *Record) Replay() (model.Multiset, error) {
	ms, err := r.ParentMultisetState()
	if err != nil {
		return nil, err
	}
	for i, op := range r.Ops {
		data, err := hex.DecodeString(op.Preimage)
		if err != nil {
			return nil, fmt.Errorf("op %d: %w", i, err)
		}
		switch op.Op {
		case OpAdd:
			ms.Add(data)
		case OpRemove:
			ms.Remove(data)
		default:
			return nil, fmt.Errorf("op %d: unknown operation %q", i, op.Op)
		}
	}
	return ms, nil
}

// Verify replays the record and reports whether it reproduces the recorded result. A record that
// does not is not a faithful account of what was hashed, and nothing concluded from it holds.
func (r *Record) Verify() error {
	ms, err := r.Replay()
	if err != nil {
		return err
	}
	if got := ms.Hash().String(); got != r.ResultMultiset {
		return fmt.Errorf("replaying %d ops on the parent gives %s, but the record says %s", len(r.Ops), got,
			r.ResultMultiset)
	}
	return nil
}

// EntryBlocks returns the records where a divergence entered: the parent multiset reproduced the
// parent's header and the result does not reproduce the block's own. Every later mismatch that
// inherits from one of these is a symptom; these are the blocks whose ops are worth reading.
func EntryBlocks(records []*Record) []*Record {
	var entries []*Record
	for _, record := range records {
		if record.HeaderCommitment != "" && record.ParentMatchesHeader() && !record.MatchesHeader() {
			entries = append(entries, record)
		}
	}
	return entries
}

// Hypothesis is one change to a record's ops that makes its result reproduce the header.
type Hypothesis struct {
	Description string
	Op          *Op
}

// WhatIf searches for a single change to a record's ops that makes its result match target -
// normally the block's own header commitment. It tries, for each op, undoing it, applying it twice,
// re-stamping it with each candidate DAA score, and flipping its coinbase flag; and, for the whole
// record, re-stamping every created entry with each candidate DAA score. The candidates are the
// merging block's DAA score and those of every merge-set block, which are the stamps a node could
// plausibly have used instead.
//
// A hit is strong evidence: MuHash collisions do not happen by accident, so a hypothesis that
// reproduces the header IS what the miner did differently. No hit means the difference is not a
// single-element change of these kinds - a coin neither side's ops name, or several changes.
//
// maxTrials bounds the work (each trial finalizes a MuHash); 0 means no bound.
func WhatIf(record *Record, target string, maxTrials int) ([]Hypothesis, int, error) {
	result, err := record.Replay()
	if err != nil {
		return nil, 0, err
	}
	candidates := candidateDAAScores(record)
	trials := 0
	var hits []Hypothesis
	try := func(description string, op *Op, change func(ms model.Multiset) error) error {
		if maxTrials != 0 && trials >= maxTrials {
			return nil
		}
		trials++
		ms := result.Clone()
		if err := change(ms); err != nil {
			return err
		}
		if ms.Hash().String() == target {
			hits = append(hits, Hypothesis{Description: description, Op: op})
		}
		return nil
	}

	for i := range record.Ops {
		op := &record.Ops[i]
		data, err := hex.DecodeString(op.Preimage)
		if err != nil {
			return nil, trials, fmt.Errorf("op %d: %w", i, err)
		}
		undo, redo := inverse(op.Op), op.Op
		if err := try(fmt.Sprintf("op %d (%s %s) did not happen", i, op.Op, op.Outpoint), op, func(ms model.Multiset) error {
			return apply(ms, undo, data)
		}); err != nil {
			return nil, trials, err
		}
		if err := try(fmt.Sprintf("op %d (%s %s) happened twice", i, op.Op, op.Outpoint), op, func(ms model.Multiset) error {
			return apply(ms, redo, data)
		}); err != nil {
			return nil, trials, err
		}
		entry, outpoint, err := utxo.DeserializeUTXO(data)
		if err != nil {
			continue
		}
		for _, candidate := range candidates {
			if candidate == entry.BlockDAAScore() {
				continue
			}
			restamped, err := utxo.SerializeUTXO(utxo.NewUTXOEntry(entry.Amount(), entry.ScriptPublicKey(),
				entry.IsCoinbase(), candidate), outpoint)
			if err != nil {
				return nil, trials, err
			}
			description := fmt.Sprintf("op %d (%s %s) carried DAA score %d instead of %d", i, op.Op, op.Outpoint,
				candidate, entry.BlockDAAScore())
			if err := try(description, op, func(ms model.Multiset) error {
				if err := apply(ms, undo, data); err != nil {
					return err
				}
				return apply(ms, redo, restamped)
			}); err != nil {
				return nil, trials, err
			}
		}
		flipped, err := utxo.SerializeUTXO(utxo.NewUTXOEntry(entry.Amount(), entry.ScriptPublicKey(),
			!entry.IsCoinbase(), entry.BlockDAAScore()), outpoint)
		if err != nil {
			return nil, trials, err
		}
		if err := try(fmt.Sprintf("op %d (%s %s) had coinbase=%t", i, op.Op, op.Outpoint, !entry.IsCoinbase()), op,
			func(ms model.Multiset) error {
				if err := apply(ms, undo, data); err != nil {
					return err
				}
				return apply(ms, redo, flipped)
			}); err != nil {
			return nil, trials, err
		}
	}

	for _, candidate := range candidates {
		if candidate == record.DAAScore {
			continue
		}
		description := fmt.Sprintf("every created entry carried DAA score %d instead of the merging block's %d",
			candidate, record.DAAScore)
		if err := try(description, nil, func(ms model.Multiset) error {
			for _, op := range record.Ops {
				if op.Op != OpAdd || op.DAAScore != record.DAAScore {
					continue
				}
				data, err := hex.DecodeString(op.Preimage)
				if err != nil {
					return err
				}
				entry, outpoint, err := utxo.DeserializeUTXO(data)
				if err != nil {
					return err
				}
				restamped, err := utxo.SerializeUTXO(utxo.NewUTXOEntry(entry.Amount(), entry.ScriptPublicKey(),
					entry.IsCoinbase(), candidate), outpoint)
				if err != nil {
					return err
				}
				ms.Remove(data)
				ms.Add(restamped)
			}
			return nil
		}); err != nil {
			return nil, trials, err
		}
	}
	return hits, trials, nil
}

func candidateDAAScores(record *Record) []uint64 {
	seen := map[uint64]struct{}{}
	var candidates []uint64
	add := func(score uint64) {
		if _, ok := seen[score]; ok {
			return
		}
		seen[score] = struct{}{}
		candidates = append(candidates, score)
	}
	add(record.DAAScore)
	for _, block := range record.MergeSet {
		add(block.DAAScore)
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i] < candidates[j] })
	return candidates
}

func inverse(op string) string {
	if op == OpAdd {
		return OpRemove
	}
	return OpAdd
}

func apply(ms model.Multiset, op string, data []byte) error {
	switch op {
	case OpAdd:
		ms.Add(data)
	case OpRemove:
		ms.Remove(data)
	default:
		return fmt.Errorf("unknown operation %q", op)
	}
	return nil
}

// Pair is a record from each of two journals describing the same block.
type Pair struct {
	A, B *Record
	// How is how they were matched: "block" when both computed the same block hash, "result" when
	// B's result is A's header commitment - B being the miner that built the block A validated.
	How string
}

// OpDifference is how the ops of a pair differ. Ops are compared as a multiset of (op, preimage):
// order does not affect a MuHash, so only presence and count matter.
type OpDifference struct {
	ParentsAgree bool
	OnlyInA      []Op
	OnlyInB      []Op
}

// Match pairs the records of two journals. Each record of a that has a header commitment is paired
// with b's record for the same block hash or, failing that, with b's record whose result equals
// that commitment. The latter is how a validating node's record is lined up with the miner's
// template record for the block it mined: the template's prospective hash is not the mined block's
// hash, but its result is exactly what the mined header commits to.
func Match(a, b []*Record) []Pair {
	byBlock := map[string]*Record{}
	byResult := map[string]*Record{}
	for _, record := range b {
		byBlock[record.Block] = record
		if _, ok := byResult[record.ResultMultiset]; !ok {
			byResult[record.ResultMultiset] = record
		}
	}
	var pairs []Pair
	for _, record := range a {
		if other, ok := byBlock[record.Block]; ok {
			pairs = append(pairs, Pair{A: record, B: other, How: "block"})
			continue
		}
		if record.HeaderCommitment == "" {
			continue
		}
		if other, ok := byResult[record.HeaderCommitment]; ok {
			pairs = append(pairs, Pair{A: record, B: other, How: "result"})
		}
	}
	sort.SliceStable(pairs, func(i, j int) bool { return pairs[i].A.DAAScore < pairs[j].A.DAAScore })
	return pairs
}

// Compare reports how a pair's ops differ.
func Compare(pair Pair) OpDifference {
	key := func(op Op) string { return op.Op + ":" + op.Preimage }
	counts := map[string]int{}
	for _, op := range pair.A.Ops {
		counts[key(op)]++
	}
	for _, op := range pair.B.Ops {
		counts[key(op)]--
	}
	difference := OpDifference{ParentsAgree: pair.A.ParentMultiset == pair.B.ParentMultiset}
	remaining := map[string]int{}
	for k, v := range counts {
		remaining[k] = v
	}
	for _, op := range pair.A.Ops {
		if remaining[key(op)] > 0 {
			difference.OnlyInA = append(difference.OnlyInA, op)
			remaining[key(op)]--
		}
	}
	for _, op := range pair.B.Ops {
		if remaining[key(op)] < 0 {
			difference.OnlyInB = append(difference.OnlyInB, op)
			remaining[key(op)]++
		}
	}
	return difference
}

// Empty reports whether the two sides applied exactly the same ops to the same parent.
func (d OpDifference) Empty() bool {
	return d.ParentsAgree && len(d.OnlyInA) == 0 && len(d.OnlyInB) == 0
}

// Describe renders an op for a report.
func (op Op) Describe() string {
	if op.DecodeError != "" {
		return fmt.Sprintf("%s <undecodable: %s> %s", op.Op, op.DecodeError, op.Preimage)
	}
	return fmt.Sprintf("%s %s amount=%d daaScore=%d coinbase=%t script=v%d:%s", op.Op, op.Outpoint, op.Amount,
		op.DAAScore, op.Coinbase, op.ScriptVersion, op.Script)
}

// HashString renders a hash for the record's string fields.
func HashString(hash *externalapi.DomainHash) string {
	if hash == nil {
		return ""
	}
	return hash.String()
}
