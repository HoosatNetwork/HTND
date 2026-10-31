package pruningstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"math"
	"math/bits"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/database"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
)

// JournalState separates selected position from materialized position. Preparing never
// changes live data. Ready freezes canonical redo. Complete commits the last data chunk
// and its materialized index together; only a current Complete set may be scanned.
type JournalState struct {
	Version                int
	Phase                  string
	BaseIndex, TargetIndex uint64
	Base, Target           string
	Method                 string
	Added, Removed         int
	Count                  uint64
	Checksum               []byte
	Cursor                 []byte
	Applied                uint64
	// Bytes is the canonical record size (keys+values) of the sealed journal. While
	// preparing it holds the previous job's size, used only as the disk estimate.
	Bytes uint64
}

// Journal has exactly one owner per consensus instance. No store cache or staging area
// is touched off-lock. A fixed namespace is reused only after the previous COMPLETE.
type Journal struct {
	journalHooks
	DB      model.DBManager
	Store   model.PruningStore
	prefix  model.DBBucket
	state   model.DBKey
	records model.DBBucket
	live    model.DBBucket
	Policy  JournalDiskPolicy
	// Archival nodes delete no history (Config.IsArchival), so no relief is assumed.
	Archival bool
}

// ErrJournalDiskReserve is returned, before the write it guards, when free space is
// below the declared policy. The job stays resumable; nothing partial is published.
var ErrJournalDiskReserve = errors.New("pruning journal: journal disk reserve not met")

// JournalDiskPolicy is the declared free-space requirement checked before every
// journal write: Floor + Margin x (estimated job bytes still to be written).
//
// Each canonical record byte is written up to four times before compaction can
// reclaim anything: the journal record and its WAL copy (build), the live-set
// write and its WAL copy (apply). Margin 4 covers exactly that; compaction output
// and concurrent foreground writes are covered by the floor, not by the margin.
// Floor 1 GiB keeps headroom for block processing and Pebble compaction while the
// job runs. Both defaults are proposals pending aged-datadir measurements (P04/P12);
// a single free-space check is not a reservation against other writers.
//
// SealedFloor alone guards a sealed (READY) job. Its redo records are already on disk,
// apply rewrites only those keys, and the finish that follows deletes history, which
// frees space. Waiting at the full requirement would also block that deletion, so a
// sealed job proceeds whenever this floor is free. On an archival node the finish
// keeps every block body, so there sealed jobs keep the full requirement.
type JournalDiskPolicy struct {
	Floor       uint64
	Margin      uint64
	SealedFloor uint64
}

const (
	DefaultJournalDiskFloor       = 1 << 30
	DefaultJournalDiskMargin      = 4
	DefaultJournalSealedDiskFloor = 1 << 30
)

// WithDefaults fills zero fields; zero means "not configured".
func (p JournalDiskPolicy) WithDefaults() JournalDiskPolicy {
	if p.Floor == 0 {
		p.Floor = DefaultJournalDiskFloor
	}
	if p.Margin == 0 {
		p.Margin = DefaultJournalDiskMargin
	}
	if p.SealedFloor == 0 {
		p.SealedFloor = DefaultJournalSealedDiskFloor
	}
	return p
}

// Required is saturating: an absurd estimate refuses instead of overflowing.
func (p JournalDiskPolicy) Required(remaining uint64) uint64 {
	if remaining > (math.MaxUint64-p.Floor)/p.Margin {
		return math.MaxUint64
	}
	return p.Floor + p.Margin*remaining
}

const (
	JournalBatchBytes = 1 << 20
)

// The manifest key and the records bucket are an on-disk format once released: a
// renamed key orphans the journal of every datadir that has it. The manifest carries
// its own Version for format changes.
var (
	journalStateKeyName      = []byte("pruning-point-utxo-set-journal-state")
	journalRecordsBucketName = []byte("pruning-point-utxo-set-journal")
)

func NewJournal(db model.DBManager, prefix model.DBBucket, store model.PruningStore) *Journal {
	return &Journal{DB: db, Store: store, prefix: prefix, state: prefix.Key(journalStateKeyName),
		records: prefix.Bucket(journalRecordsBucketName), live: prefix.Bucket(pruningPointUTXOSetBucketName),
		Policy: JournalDiskPolicy{}.WithDefaults()}
}
func (j *Journal) Load() (*JournalState, error) {
	b, e := j.DB.Get(j.state)
	if database.IsNotFoundError(e) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	s := new(JournalState)
	if e = json.Unmarshal(b, s); e != nil {
		return nil, e
	}
	if s.Version != 1 || (s.Phase != "preparing" && s.Phase != "ready" && s.Phase != "complete") || s.Applied > s.Count || s.TargetIndex < s.BaseIndex ||
		(s.Phase == "complete" && s.Applied != s.Count) ||
		(s.Phase == "ready" && len(s.Checksum) != sha256.Size) {
		return nil, fmt.Errorf("invalid pruning journal manifest")
	}
	return s, nil
}
func (j *Journal) save(tx model.DBWriter, s *JournalState) error {
	b, e := json.Marshal(s)
	if e != nil {
		return e
	}
	return tx.Put(j.state, b)
}
func (j *Journal) Save(s *JournalState) error {
	if e := j.CheckReserve(s.Remaining()); e != nil {
		return e
	}
	tx, e := j.DB.BeginUnindexed()
	if e != nil {
		return e
	}
	defer tx.RollbackUnlessClosed()
	if e = j.save(tx, s); e != nil {
		return e
	}
	return journalSync(tx)
}

// Initialize is legal only after legacy pending recovery, or an exclusive successful import.
func (j *Journal) Initialize(index uint64, target *externalapi.DomainHash) error {
	return j.Save(&JournalState{Version: 1, Phase: "complete", BaseIndex: index, TargetIndex: index, Base: target.String(), Target: target.String()})
}

// Begin preserves the complete base before cleanup. A crash during cleanup/build discards
// only journal records and derives again; no live key has been written in preparing.
func (j *Journal) Begin(base *JournalState, index uint64, target *externalapi.DomainHash) (*JournalState, error) {
	if base.Phase != "complete" || index != base.TargetIndex+1 {
		return nil, fmt.Errorf("pruning journal: non-contiguous base")
	}
	s := &JournalState{Version: 1, Phase: "preparing", BaseIndex: base.TargetIndex, Base: base.Target, TargetIndex: index, Target: target.String(), Bytes: base.Bytes}
	if e := j.Save(s); e != nil {
		return nil, e
	}
	j.hit("preparing")
	return s, nil
}

// AdoptSynchronousMove records COMPLETE at the move after base once the synchronous
// fallback has written that move's diff into the live set itself (Begin was refused
// for disk space). It skips the reserve check: base writes that move unchecked, and
// only the finish that follows frees space. No records belong to the move; Bytes
// keeps the previous job's size as the next job's estimate.
func (j *Journal) AdoptSynchronousMove(base *JournalState, target *externalapi.DomainHash, method string,
	added, removed int) (*JournalState, error) {
	if base.Phase != "complete" {
		return nil, fmt.Errorf("pruning journal: non-contiguous base")
	}
	s := &JournalState{Version: 1, Phase: "complete", BaseIndex: base.TargetIndex, Base: base.Target,
		TargetIndex: base.TargetIndex + 1, Target: target.String(), Method: method, Added: added, Removed: removed,
		Bytes: base.Bytes}
	tx, e := j.DB.BeginUnindexed()
	if e != nil {
		return nil, e
	}
	defer tx.RollbackUnlessClosed()
	if e = j.save(tx, s); e != nil {
		return nil, e
	}
	if e = journalSync(tx); e != nil {
		return nil, e
	}
	j.hit("fallback")
	return s, nil
}

// Clear deletes the records in bounded batches. Each batch seeks past the last key the
// previous one deleted instead of starting at First, so the iterator does not step over
// this call's own tombstones again: the cost is linear in the record count. After a
// crash the next call starts at First once and deletes whatever is left.
func (j *Journal) Clear(ctx context.Context) error {
	var last []byte
	for {
		if e := ctx.Err(); e != nil {
			return e
		}
		c, e := j.DB.Cursor(j.records)
		if e != nil {
			return e
		}
		var ok bool
		if last == nil {
			ok = c.First()
		} else {
			// last is deleted and committed, and the records have one owner: the seek
			// lands on the first record after it, or finds none left.
			e = c.Seek(j.records.Key(last))
			ok = e == nil
			if database.IsNotFoundError(e) {
				e = nil
			}
			if e != nil {
				c.Close()
				return e
			}
		}
		tx, e := j.DB.BeginUnindexed()
		if e != nil {
			c.Close()
			return e
		}
		n := 0
		for ; ok && n < JournalBatchEntries; ok = c.Next() {
			k, err := c.Key()
			if err != nil {
				e = err
				break
			}
			suffix := append([]byte(nil), k.Suffix()...)
			if e = tx.Delete(j.records.Key(suffix)); e != nil {
				break
			}
			last = suffix
			n++
		}
		ce := c.Close()
		if e == nil {
			e = ce
		}
		if e == nil {
			e = tx.Commit()
		}
		tx.RollbackUnlessClosed()
		if e != nil {
			return e
		}
		j.hit("cleanup")
		if n == 0 {
			return nil
		}
	}
}

// Build uses disk key ordering for canonicalization. Add wins over remove for a
// restamped key, exactly matching the legacy remove-then-add writer, without sorting a
// second in-memory array. Every record is an absolute put/delete and is idempotent.
func (j *Journal) Build(ctx context.Context, s *JournalState, diff externalapi.UTXODiff, method string) error {
	if s.Phase != "preparing" {
		return fmt.Errorf("pruning journal: build outside preparing")
	}
	if e := j.Clear(ctx); e != nil {
		return e
	}
	var written uint64
	for mode, collection := range []externalapi.UTXOCollection{diff.ToRemove(), diff.ToAdd()} {
		it := collection.Iterator()
		tx, e := j.DB.BeginUnindexed()
		if e != nil {
			it.Close()
			return e
		}
		n, size := 0, 0
		for ok := it.First(); ok; ok = it.Next() {
			if e = ctx.Err(); e != nil {
				break
			}
			out, entry, err := it.Get()
			if err != nil {
				e = err
				break
			}
			k, err := serializeOutpoint(out)
			if err != nil {
				e = err
				break
			}
			v := []byte{0}
			if mode == 1 {
				data, err := serializeUTXOEntry(entry)
				if err != nil {
					e = err
					break
				}
				v = append([]byte{1}, data...)
			}
			if len(k)+len(v) > JournalBatchBytes {
				e = fmt.Errorf("pruning journal: oversized record")
				break
			}
			if e = tx.Put(j.records.Key(k), v); e != nil {
				break
			}
			n++
			size += len(k) + len(v)
			written += uint64(len(k) + len(v))
			if n >= JournalBatchEntries || size >= JournalBatchBytes {
				if e = j.CheckReserve(max(s.Bytes, written)); e != nil {
					break
				}
				if e = tx.Commit(); e != nil {
					break
				}
				j.hit("journal-chunk")
				tx, e = j.DB.BeginUnindexed()
				if e != nil {
					break
				}
				n, size = 0, 0
			}
		}
		ce := it.Close()
		if e == nil {
			e = ce
		}
		if e == nil && n > 0 {
			e = j.CheckReserve(max(s.Bytes, written))
		}
		if e == nil {
			e = tx.Commit()
		}
		if tx != nil {
			tx.RollbackUnlessClosed()
		}
		if e != nil {
			return e
		}
	}
	count, sum, size, e := j.digest(ctx)
	if e != nil {
		return e
	}
	s.Count, s.Checksum, s.Method, s.Bytes = count, sum, method, size
	s.Added, s.Removed = diff.ToAdd().Len(), diff.ToRemove().Len()
	s.Phase = "ready"
	if e = j.Save(s); e != nil {
		return e
	}
	j.hit("ready")
	return nil
}
func digestRecord(h hash.Hash, k, v []byte) {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(len(k)))
	h.Write(b[:])
	h.Write(k)
	binary.BigEndian.PutUint64(b[:], uint64(len(v)))
	h.Write(b[:])
	h.Write(v)
}
func (j *Journal) digest(ctx context.Context) (uint64, []byte, uint64, error) {
	if e := ctx.Err(); e != nil {
		return 0, nil, 0, e
	}
	c, e := j.DB.Cursor(j.records)
	if e != nil {
		return 0, nil, 0, e
	}
	h := sha256.New()
	var n, size uint64
	for ok := c.First(); ok; ok = c.Next() {
		if e = ctx.Err(); e != nil {
			break
		}
		k, err := c.Key()
		if err != nil {
			e = err
			break
		}
		v, err := c.Value()
		if err != nil {
			e = err
			break
		}
		if len(v) == 0 || v[0] > 1 || (v[0] == 0 && len(v) != 1) {
			e = fmt.Errorf("invalid pruning journal record")
			break
		}
		digestRecord(h, k.Suffix(), v)
		n++
		size += uint64(len(k.Suffix()) + len(v))
	}
	ce := c.Close()
	if e == nil {
		e = ce
	}
	if e == nil {
		e = ctx.Err()
	}
	return n, h.Sum(nil), size, e
}

// Apply validates the sealed journal before any mutation, including restart. Each
// private bounded transaction contains data and cursor; memory never advances past a
// failed/uncertain commit. Retrying reloads the manifest instead of re-deriving a diff.
func (j *Journal) Apply(ctx context.Context, s *JournalState) error {
	if s.Phase != "ready" {
		return fmt.Errorf("pruning journal: apply outside ready")
	}
	n, sum, _, e := j.digest(ctx)
	if e != nil {
		return e
	}
	if n != s.Count || !bytes.Equal(sum, s.Checksum) {
		return fmt.Errorf("pruning journal: journal checksum/count mismatch")
	}
	for s.Phase == "ready" {
		if e := j.CheckSealedReserve(s.Remaining()); e != nil {
			return e
		}
		if e = ctx.Err(); e != nil {
			return e
		}
		c, err := j.DB.Cursor(j.records)
		if err != nil {
			return err
		}
		ok := c.First()
		if len(s.Cursor) > 0 {
			err = c.Seek(j.records.Key(s.Cursor))
			if err == nil {
				ok = c.Next()
			} else {
				ok = false
				if !database.IsNotFoundError(err) {
					c.Close()
					return err
				}
			}
		}
		tx, err := j.DB.BeginUnindexed()
		if err != nil {
			c.Close()
			return err
		}
		next := *s
		size, count := 0, 0
		for ; ok && size < JournalBatchBytes && count < JournalBatchEntries; ok = c.Next() {
			k, err := c.Key()
			if err != nil {
				e = err
				break
			}
			v, err := c.Value()
			if err != nil {
				e = err
				break
			}
			suffix := append([]byte(nil), k.Suffix()...)
			if v[0] == 0 {
				e = tx.Delete(j.live.Key(suffix))
			} else {
				e = tx.Put(j.live.Key(suffix), v[1:])
			}
			if e != nil {
				break
			}
			next.Cursor = suffix
			next.Applied++
			size += len(suffix) + len(v)
			count++
		}
		ce := c.Close()
		if e == nil {
			e = ce
		}
		if !ok {
			if next.Applied != next.Count {
				e = fmt.Errorf("pruning journal: incomplete cursor")
			}
			next.Phase = "complete"
		}
		if e == nil {
			e = j.save(tx, &next)
		}
		if e == nil {
			j.hit("before-data-commit")
			if next.Phase == "complete" {
				e = journalSync(tx)
			} else {
				e = tx.Commit()
			}
		}
		tx.RollbackUnlessClosed()
		if e != nil {
			return e
		}
		*s = next
		j.hit("data-commit")
		if s.Phase == "complete" {
			j.hit("complete")
		}
	}
	return nil
}

// Invalidate is called under the consensus lock before the owner can mutate the set.
func (j *Journal) Invalidate() { j.Store.(*pruningStore).pruningPointUTXOSetGeneration.Add(1) }

// Remaining estimates the bytes this job will still write: the previous job's size
// while preparing, the unapplied share of the sealed journal while ready, 0 after.
func (s *JournalState) Remaining() uint64 {
	switch {
	case s.Phase == "preparing":
		return s.Bytes
	case s.Phase == "ready" && s.Count > 0:
		// Bytes x Applied / Count <= Bytes, so the 128-bit quotient fits (Div64 precondition).
		hi, lo := bits.Mul64(s.Bytes, s.Applied)
		done, _ := bits.Div64(hi, lo, s.Count)
		return s.Bytes - done
	}
	return 0
}

// CheckReserve refuses a journal write of an unsealed job when free space is below
// the policy.
func (j *Journal) CheckReserve(remaining uint64) error {
	available, e := j.Available()
	if e != nil {
		return e
	}
	if required := j.Policy.Required(remaining); available < required {
		return fmt.Errorf("%w: %d bytes available, %d required (floor %d + margin %d x estimated remaining job %d)",
			ErrJournalDiskReserve, available, required, j.Policy.Floor, j.Policy.Margin, remaining)
	}
	return nil
}

// CheckSealedReserve refuses a write of a sealed (READY) job only below SealedFloor,
// or, on an archival node, below the full requirement for the remaining bytes.
func (j *Journal) CheckSealedReserve(remaining uint64) error {
	if j.Archival {
		return j.CheckReserve(remaining)
	}
	available, e := j.Available()
	if e != nil {
		return e
	}
	if available < j.Policy.SealedFloor {
		return fmt.Errorf("%w: %d bytes available, %d required (sealed job floor)",
			ErrJournalDiskReserve, available, j.Policy.SealedFloor)
	}
	return nil
}

// Available reports the backend's free bytes for the database directory.
func (j *Journal) Available() (uint64, error) {
	if c, ok := j.DB.(interface{ AvailableDiskBytes() (uint64, error) }); ok {
		return c.AvailableDiskBytes()
	}
	return 0, fmt.Errorf("pruning journal: backend has no disk space query")
}

// RemoveAfterFinish requires exclusive ownership after the background owner is joined.
// Cleanup can be interrupted: COMPLETE remains until the last delete, so the new
// binary can resume. Old binaries are allowed only after this operation and DB close.
func (j *Journal) RemoveAfterFinish(ctx context.Context) error {
	pending, e := j.Store.HadStartedUpdatingPruningPointUTXOSet(j.DB)
	if e != nil {
		return e
	}
	s, e := j.Load()
	if e != nil {
		return e
	}
	if pending || s == nil || s.Phase != "complete" {
		return fmt.Errorf("pruning journal: downgrade requires verified finished set")
	}
	if e = j.Clear(ctx); e != nil {
		return e
	}
	tx, e := j.DB.BeginUnindexed()
	if e != nil {
		return e
	}
	defer tx.RollbackUnlessClosed()
	if e = tx.Delete(j.state); e != nil {
		return e
	}
	return journalSync(tx)
}

// Phase commits sync their WAL prefix: initialization/PREPARING fence selected input;
// READY fences every redo record; COMPLETE fences materialized data before deletion.
// Intermediate data+cursor batches need atomicity, and can be replayed after loss.
func journalSync(tx model.DBTransaction) error {
	syncer, ok := tx.(interface{ CommitSync() error })
	if !ok {
		return fmt.Errorf("pruning journal: backend cannot sync")
	}
	return syncer.CommitSync()
}
