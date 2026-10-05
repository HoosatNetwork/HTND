// Package muhashjournal records every element added to or removed from a block's UTXO multiset
// (the MuHash behind the header's UTXO commitment), so a commitment mismatch can be taken apart
// offline.
//
// A MuHash cannot be decomposed: two different hashes say the sets differ and nothing about which
// coin. What can be compared is the list of elements each side fed into it. A block's multiset is
// its selected parent's multiset plus a list of Add and Remove calls, one per coin spent and
// created by the merge set, and that list is small. This package writes the list - with the parent
// multiset it was applied to, the result, and the header commitment it was supposed to reproduce -
// to a JSONL file, one record per multiset computation.
//
// That gives three things a hash never can:
//
//   - A record whose parent multiset matched the parent's header and whose result does not match
//     its own header is the block where the divergence entered, with the exact elements it used.
//   - The miner computes the same thing when it builds a block template, and its record's result IS
//     the header commitment it mined. Journalling the miner too and matching its record to the
//     validating node's record for the same block puts the two element lists side by side; the
//     elements only one of them has are the root cause.
//   - The elements are exact preimages, so "what if this one coin had a different DAA score, or
//     were not there" can be tested against the header offline (see WhatIf).
//
// Enabled by htnd's --muhash-journal=<path> flag (SetPath), which appends JSONL to that file. Off,
// it costs one comparison per block. Templates and virtual are recorded along with validated
// blocks, because the miner's template records are what a validating node's records are compared
// against. Recording stops after MaxRecords records so a journal left on cannot fill the disk.
package muhashjournal

import (
	"bufio"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/utxo"
)

// Operation names as they appear in the journal.
const (
	OpAdd    = "add"
	OpRemove = "remove"
)

// Record kinds. A template or virtual record has no header of its own; its result is what a header
// built from it would commit to.
const (
	KindBlock    = "block"
	KindVirtual  = "virtual"
	KindTemplate = "template"
)

// Op is one Add or Remove on the multiset. Preimage is authoritative - it is exactly the bytes that
// were hashed - and the decoded fields beside it are for reading and grepping.
type Op struct {
	Op            string `json:"op"`
	Preimage      string `json:"preimage"`
	Outpoint      string `json:"outpoint,omitempty"`
	Amount        uint64 `json:"amount"`
	DAAScore      uint64 `json:"daaScore"`
	Coinbase      bool   `json:"coinbase"`
	ScriptVersion uint16 `json:"scriptVersion"`
	Script        string `json:"script,omitempty"`
	DecodeError   string `json:"decodeError,omitempty"`
}

// MergeSetBlock is one block of the merge set whose acceptance data produced the ops.
type MergeSetBlock struct {
	Hash     string `json:"hash"`
	DAAScore uint64 `json:"daaScore"`
	Accepted int    `json:"accepted"`
	Total    int    `json:"total"`
}

// Record is one multiset computation: ParentMultiset plus Ops gives ResultMultiset.
type Record struct {
	RunID string `json:"runId"`
	Time  string `json:"time"`
	Kind  string `json:"kind"`

	Block          string `json:"block"`
	SelectedParent string `json:"selectedParent"`
	// DAAScore is the merging block's DAA score: every created entry is stamped with it.
	DAAScore uint64 `json:"daaScore"`

	HeaderCommitment       string `json:"headerCommitment,omitempty"`
	ParentHeaderCommitment string `json:"parentHeaderCommitment,omitempty"`

	ParentMultiset string `json:"parentMultiset"`
	// ParentMultisetSerialized is the full MuHash state, so the computation can be replayed and
	// varied offline without the node's database.
	ParentMultisetSerialized string `json:"parentMultisetSerialized"`
	ResultMultiset           string `json:"resultMultiset"`

	MergeSet []MergeSetBlock `json:"mergeSet"`
	Ops      []Op            `json:"ops"`
}

// MatchesHeader reports whether the result reproduces the block's own header commitment. False
// when there is no header to compare with.
func (r *Record) MatchesHeader() bool {
	return r.HeaderCommitment != "" && r.HeaderCommitment == r.ResultMultiset
}

// ParentMatchesHeader reports whether the parent multiset reproduces the parent's header
// commitment. False when there is no parent header to compare with.
func (r *Record) ParentMatchesHeader() bool {
	return r.ParentHeaderCommitment != "" && r.ParentHeaderCommitment == r.ParentMultiset
}

// Recorder forwards every Add and Remove to the wrapped multiset and keeps a copy of each preimage.
// It implements utxo.MultisetWriter, so it goes where the multiset went and changes nothing about
// what is hashed.
type Recorder struct {
	inner utxo.MultisetWriter
	ops   []Op
}

// NewRecorder wraps inner.
func NewRecorder(inner utxo.MultisetWriter) *Recorder {
	return &Recorder{inner: inner}
}

// Add adds data to the wrapped multiset and records it.
func (r *Recorder) Add(data []byte) {
	r.inner.Add(data)
	r.ops = append(r.ops, NewOp(OpAdd, data))
}

// Remove removes data from the wrapped multiset and records it.
func (r *Recorder) Remove(data []byte) {
	r.inner.Remove(data)
	r.ops = append(r.ops, NewOp(OpRemove, data))
}

// Ops returns what was recorded, in order.
func (r *Recorder) Ops() []Op {
	return r.ops
}

// NewOp describes one multiset operation on the preimage data.
func NewOp(op string, data []byte) Op {
	result := Op{Op: op, Preimage: hex.EncodeToString(data)}
	entry, outpoint, err := utxo.DeserializeUTXO(data)
	if err != nil {
		result.DecodeError = err.Error()
		return result
	}
	result.Outpoint = fmt.Sprintf("%s:%d", outpoint.TransactionID, outpoint.Index)
	result.Amount = entry.Amount()
	result.DAAScore = entry.BlockDAAScore()
	result.Coinbase = entry.IsCoinbase()
	if script := entry.ScriptPublicKey(); script != nil {
		result.ScriptVersion = script.Version
		result.Script = hex.EncodeToString(script.Script)
	}
	return result
}

// MaxRecords is how many records one process writes before the journal stops. On mainnet a record
// averages about 8 KB (200,000 records measured 1.6 GB, about five hours of blocks), so this
// bounds a journal left on to roughly 16 GB, about two days. Delete or rotate the file between runs
// to record more.
const MaxRecords = 2000000

type writer struct {
	mu       sync.Mutex
	path     string
	written  int
	file     *os.File
	buffered *bufio.Writer
	failed   bool
	logf     func(format string, args ...any)
}

var w = &writer{}

var runID = fmt.Sprintf("%d-%d", time.Now().UTC().Unix(), os.Getpid())

// SetLogger installs the callback used to report the journal's own problems, so this package stays
// a leaf dependency.
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

// SetPath turns the journal on, appending to path, or off when path is empty. htnd calls it once at
// startup from --muhash-journal, before consensus is built.
func SetPath(path string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closeLocked()
	w.path = path
	w.failed, w.written = false, 0
}

func (wr *writer) enabledLocked() bool {
	return wr.path != "" && !wr.failed && wr.written < MaxRecords
}

// Enabled reports whether a multiset computation should be recorded. Callers check it before doing
// any of the recording work.
func Enabled() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.enabledLocked()
}

// Write appends one record, flushing it so a killed node keeps everything it recorded. Errors are
// logged and swallowed: instrumentation must never fail block processing.
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
			w.log("MuHash journal disabled: cannot open %s: %s", w.path, err)
			return
		}
		w.file = file
		w.buffered = bufio.NewWriter(file)
		w.log("MuHash journal recording to %s (at most %d records)", w.path, MaxRecords)
	}
	if record.RunID == "" {
		record.RunID = runID
	}
	if record.Time == "" {
		record.Time = time.Now().UTC().Format(time.RFC3339Nano)
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		w.log("MuHash journal: cannot encode record for block %s: %s", record.Block, err)
		return
	}
	if _, err := w.buffered.Write(append(encoded, '\n')); err != nil {
		w.failed = true
		w.log("MuHash journal disabled: write to %s failed: %s", w.path, err)
		return
	}
	if err := w.buffered.Flush(); err != nil {
		w.failed = true
		w.log("MuHash journal disabled: flush to %s failed: %s", w.path, err)
		return
	}
	w.written++
	if w.written == MaxRecords {
		w.log("MuHash journal reached its %d-record cap; restart with a fresh --muhash-journal file to record more",
			MaxRecords)
	}
}

func (wr *writer) closeLocked() {
	if wr.buffered != nil {
		_ = wr.buffered.Flush()
	}
	if wr.file != nil {
		_ = wr.file.Close()
	}
	wr.file, wr.buffered = nil, nil
}
