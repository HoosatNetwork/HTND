// Package utxosurvey records every UTXO-verification failure a node hits, as JSONL, instead of only
// the first one.
//
// The motivating problem: on a chain built on an incomplete imported pruning-point UTXO set, the
// node's existing diagnostics report the first ErrBadUTXOCommitment loudly and then go quiet -
// logToleratedIssue warns once per step label and debug-logs the rest, and once a block is
// disqualified every descendant takes ResolveBlockStatus's cascade branch, which never calls
// verifyUTXO at all. A single failure tells you nothing about whether the node is losing coins or
// merely disagreeing with its peers about how a coin is spelled; a whole IBD's worth of failures,
// each carrying the outpoints involved and where else those outpoints can be found, does.
//
// This package is pure instrumentation: it is off unless HTND_UTXO_SURVEY names an output file, it
// never influences validation, and a failure to write is logged and swallowed rather than
// propagated.
//
// Environment:
//
//	HTND_UTXO_SURVEY            path of the JSONL file to append records to. Unset/empty: disabled.
//	HTND_UTXO_SURVEY_MAX        stop after this many records (default 5000; 0 means unlimited).
//	HTND_UTXO_SURVEY_MAX_TXIDS  cap on each per-record transaction-ID list (default 128; 0 unlimited).
//	HTND_UTXO_SURVEY_DEEP       how many records may pay for an O(UTXO-set) recomputation of the
//	                            selected parent's multiset (default 0 - those are multi-minute scans).
package utxosurvey

import (
	"bufio"
	"encoding/json"
	"os"
	"strconv"
	"sync"
)

// Classification is the A/B/C verdict for one failing block. Written into Record.Classification.
const (
	// ClassificationOriginalMissing - a coin that should already have existed in the imported
	// pruning-point set (or in the chain between it and the selected parent) is absent, and is not
	// created by anything in this block's own acceptance data. The snapshot or its transfer dropped it.
	ClassificationOriginalMissing = "ORIGINAL_MISSING"

	// ClassificationNewMissing - a coin this block's own (or a recent mergeset block's) accepted
	// transactions create is absent when a spend or the commitment needs it. Acceptance apply,
	// coinbase collision, or the selected-tip diff dropped the create.
	ClassificationNewMissing = "NEW_MISSING"

	// ClassificationHandlingMismatch - the coin is present, but not under the identity bytes the
	// validator expected: a different BlockDAAScore, amount, script version or isCoinbase, i.e. a
	// different SerializeUTXO preimage. Nothing was destroyed; two nodes disagree on spelling.
	ClassificationHandlingMismatch = "HANDLING_MISMATCH"

	// ClassificationCommitmentOnly - no spend failed; only the header commitment and the calculated
	// commitment disagree.
	ClassificationCommitmentOnly = "COMMITMENT_ONLY"

	// ClassificationUnknown - the record does not carry enough to place it in any of the above.
	ClassificationUnknown = "UNKNOWN"
)

// IBD stages a record can be attributed to.
const (
	StageHeaders           = "headers"
	StageBodies            = "bodies"
	StagePruningUTXOImport = "pruning-utxo-import"
	StageChainReplay       = "chain-replay"
)

// Sources an outpoint can be found under in AlternateMatch.Source.
const (
	SourceVirtualUTXOSet      = "virtual-utxo-set"
	SourcePruningPointUTXOSet = "pruning-point-utxo-set"
	SourcePastDiffToAdd       = "past-utxo-diff-toAdd"
	SourcePastDiffToRemove    = "past-utxo-diff-toRemove"
	SourceMergesetAcceptance  = "mergeset-acceptance-output"
	SourceImportedPruningSet  = "imported-pruning-point-utxo-set"
	SourceSelectedParentDiff  = "selected-parent-diff-toAdd"
)

// AlternateMatch is one place the survey found an outpoint that a block reported as missing,
// together with the exact bytes that place would feed into MuHash. Two matches for one outpoint
// whose SerializedUTXO differ are the byte-level proof of Phase 3c: the coin exists, and the
// disagreement is about its identity, not its existence.
type AlternateMatch struct {
	Source          string `json:"source"`
	Amount          uint64 `json:"amount"`
	ScriptVersion   uint16 `json:"scriptVersion"`
	ScriptPublicKey string `json:"scriptPublicKey"`
	IsCoinbase      bool   `json:"isCoinbase"`
	BlockDAAScore   uint64 `json:"blockDAAScore"`
	SerializedUTXO  string `json:"serializedUTXO"`
}

// MissingOutpoint is one outpoint a block could not resolve, plus everything cheap that is known
// about where else it lives.
type MissingOutpoint struct {
	TxID  string `json:"txid"`
	Index uint32 `json:"index"`

	// SpentByTx is the transaction in this block that tried to spend it.
	SpentByTx string `json:"spentByTx"`

	// ExpectedBlockDAAScore is the score the entry would carry if this block's own acceptance data
	// created it (utxo.AcceptedUTXOBlockDAAScore of the merging block). Null when nothing in this
	// block claims to create it.
	ExpectedBlockDAAScore *uint64 `json:"expectedBlockDAAScore"`

	FoundInParentSet                  bool `json:"foundInParentSet"`
	FoundInMergesetAdds               bool `json:"foundInMergesetAdds"`
	FoundUnderDifferentDAAScore       bool `json:"foundUnderDifferentDAAScore"`
	FoundUnderDifferentAmountOrScript bool `json:"foundUnderDifferentAmountOrScript"`

	// AlreadySpentInThisPast records the benign case: the outpoint is absent because this block's
	// own past UTXO diff already removed it (a double spend, or a spend of something spent by an
	// ancestor), not because anything lost it. Distinguishes a real gap from correct behaviour.
	AlreadySpentInThisPast bool `json:"alreadySpentInThisPast"`

	AlternateMatches []AlternateMatch `json:"alternateMatches"`
}

// DiffElement is one entry of a block's own UTXO delta that its acceptance data does not account
// for (or vice versa) - the "extra add/remove" of a COMMITMENT_ONLY failure.
type DiffElement struct {
	TxID           string `json:"txid"`
	Index          uint32 `json:"index"`
	Amount         uint64 `json:"amount"`
	IsCoinbase     bool   `json:"isCoinbase"`
	BlockDAAScore  uint64 `json:"blockDAAScore"`
	SerializedUTXO string `json:"serializedUTXO"`
	Reason         string `json:"reason"`
}

// Record is one failing block. The field set is fixed so that a whole run can be clustered with
// ordinary JSONL tooling without knowing which code path produced any given line.
type Record struct {
	BlockHash      string `json:"blockHash"`
	SelectedParent string `json:"selectedParent"`
	DAAScore       uint64 `json:"daaScore"`
	BlueScore      uint64 `json:"blueScore"`
	IsChainBlock   bool   `json:"isChainBlock"`
	IBDStage       string `json:"ibdStage"`

	// Error lists every check that failed for this block, not just the first, e.g.
	// "ErrBadUTXOCommitment+missing-input".
	Error string `json:"error"`

	// ErrorDetails is each failure's full message, deduplicated. The label in Error names the rule
	// that fired; the message is where the rule put its evidence, and for some rules that evidence is
	// the finding. ErrImmatureSpend is the case that forced this field: its label says only that a
	// coinbase was spent too early, while its message carries the coinbase's DAA score, the spending
	// block's, and the required maturity - the three numbers that decide whether the entry was
	// stamped under a different rule than the producer used, which is a handling mismatch, or whether
	// acceptance diverged upstream so a different block merged it, which is not.
	ErrorDetails []string `json:"errorDetails"`

	HeaderUTXOCommitment     string `json:"headerUTXOCommitment"`
	CalculatedUTXOCommitment string `json:"calculatedUTXOCommitment"`

	ParentStoredMultiset     string `json:"parentStoredMultiset"`
	ParentRecomputedMultiset string `json:"parentRecomputedMultiset"`

	// ParentHeaderUTXOCommitment is not in the original schema but is free to collect and is what
	// decides whether this block merely inherits an offset it did not create.
	ParentHeaderUTXOCommitment string `json:"parentHeaderUTXOCommitment"`

	AcceptanceTxCount  int      `json:"acceptanceTxCount"`
	AcceptedTxIDs      []string `json:"acceptedTxIds"`
	RejectedOrRedTxIDs []string `json:"rejectedOrRedTxIds"`

	// AcceptedTxIDsTruncated/RejectedOrRedTxIDsTruncated say how many IDs were dropped by
	// HTND_UTXO_SURVEY_MAX_TXIDS, so a clustering pass can tell a short list from a capped one.
	AcceptedTxIDsTruncated      int `json:"acceptedTxIdsTruncated"`
	RejectedOrRedTxIDsTruncated int `json:"rejectedOrRedTxIdsTruncated"`

	CoinbaseTxID string `json:"coinbaseTxId"`

	MissingOutpoints []MissingOutpoint `json:"missingOutpoints"`

	ExtraAddsNotInHeaderView    []DiffElement `json:"extraAddsNotInHeaderView"`
	ExtraRemovesNotInHeaderView []DiffElement `json:"extraRemovesNotInHeaderView"`

	Classification string `json:"classification"`
	Notes          string `json:"notes"`
}

type writer struct {
	mu       sync.Mutex
	loaded   bool
	path     string
	file     *os.File
	buffered *bufio.Writer
	written  int
	max      int
	maxTxIDs int
	deep     int
	failed   bool
	// logf is set by the owning package so this one needn't depend on a logger.
	logf func(format string, args ...any)
}

var w = &writer{}

// loadConfigLocked reads the environment the first time anything asks. Deliberately not done in a
// package-level initializer: that would run before a test (or an embedding process) could set the
// variables, and the survey is worthless if it cannot be turned on from a test.
func (wr *writer) loadConfigLocked() {
	if wr.loaded {
		return
	}
	wr.loaded = true
	wr.path = os.Getenv("HTND_UTXO_SURVEY")
	wr.max = envInt("HTND_UTXO_SURVEY_MAX", 5000)
	wr.maxTxIDs = envInt("HTND_UTXO_SURVEY_MAX_TXIDS", 128)
	wr.deep = envInt("HTND_UTXO_SURVEY_DEEP", 0)
}

func envInt(key string, fallback int) int {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return fallback
	}
	return value
}

// SetLogger installs the callback used to report the survey's own problems (it deliberately has no
// logger dependency of its own). Safe to call more than once; the last one wins.
func SetLogger(logf func(format string, args ...any)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.logf = logf
}

func (wr *writer) log(format string, args ...any) {
	if wr.logf != nil {
		wr.logf(format, args...)
	}
}

// Enabled reports whether a survey file was configured and the record cap has not been reached.
// Every caller checks this before doing any of the work of building a record, so that a node with
// the survey off pays nothing beyond this comparison.
func Enabled() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.enabledLocked()
}

func (wr *writer) enabledLocked() bool {
	wr.loadConfigLocked()
	if wr.path == "" || wr.failed {
		return false
	}
	return wr.max == 0 || wr.written < wr.max
}

// Path returns the configured output file, or "" when the survey is off.
func Path() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.loadConfigLocked()
	return w.path
}

// MaxTxIDs is the per-record cap on each transaction-ID list.
func MaxTxIDs() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.loadConfigLocked()
	return w.maxTxIDs
}

// TakeDeepBudget claims one of the HTND_UTXO_SURVEY_DEEP permits for an O(UTXO-set) recomputation,
// reporting whether one was available. Those scans take minutes on a mature chain, so they are
// rationed rather than run per failing block.
func TakeDeepBudget() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.loadConfigLocked()
	if w.deep <= 0 {
		return false
	}
	w.deep--
	return true
}

// Write appends one record. It is safe for concurrent use, flushes on every record so that a
// killed node keeps everything it surveyed, and reports how many records the file now holds.
// Errors are logged and swallowed: instrumentation must never be able to fail a sync.
func Write(record *Record) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if !w.enabledLocked() {
		return
	}
	if w.file == nil {
		file, err := os.OpenFile(w.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			w.failed = true
			w.log("UTXO survey disabled: cannot open %s: %s", w.path, err)
			return
		}
		w.file = file
		w.buffered = bufio.NewWriter(file)
		w.log("UTXO survey recording to %s (max %d records, 0 = unlimited)", w.path, w.max)
	}

	encoded, err := json.Marshal(record)
	if err != nil {
		w.log("UTXO survey: cannot encode record for block %s: %s", record.BlockHash, err)
		return
	}
	if _, err := w.buffered.Write(append(encoded, '\n')); err != nil {
		w.failed = true
		w.log("UTXO survey disabled: write to %s failed: %s", w.path, err)
		return
	}
	if err := w.buffered.Flush(); err != nil {
		w.failed = true
		w.log("UTXO survey disabled: flush to %s failed: %s", w.path, err)
		return
	}

	w.written++
	if w.max != 0 && w.written == w.max {
		w.log("UTXO survey reached its %d-record cap; no further records will be written. "+
			"Raise HTND_UTXO_SURVEY_MAX to survey more of the sync.", w.max)
	}
}

// closeLocked flushes and closes the survey file. There is no exported Close and nothing calls one:
// every record is flushed as it is written, precisely so that a node killed mid-sync keeps
// everything it surveyed, which leaves an explicit shutdown hook nothing to do.
func (wr *writer) closeLocked() {
	if wr.file == nil {
		return
	}
	if wr.buffered != nil {
		_ = wr.buffered.Flush()
	}
	_ = wr.file.Close()
	wr.file = nil
	wr.buffered = nil
}

// Reset closes any open survey file and forgets the loaded configuration, so that the next call
// re-reads the environment. It exists for tests, which need to turn the survey on and off within
// one process; production code has no reason to call it.
func Reset() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closeLocked()
	// Field by field, not *w = writer{}: the mutex being held is one of those fields, and replacing
	// the struct wholesale unlocks a zeroed mutex on the way out.
	w.loaded = false
	w.path = ""
	w.written = 0
	w.max = 0
	w.maxTxIDs = 0
	w.deep = 0
	w.failed = false
}
