package pruningstore

import (
	"encoding/binary"
	"fmt"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
)

var historyDeletionJobsBucketName = []byte("history-deletion-jobs")

const historyDeletionJobVersion = 1

// HistoryDeletionJob is a durable record of history still to delete below
// DeletionPoint. The finish of the update of the pruning point at Index writes it in
// its commit; the last batch of the job deletes it. PrunedTips are the tips that
// finish removed from the tips list: with the parents of DeletionPoint they are the
// roots of the walk. Older binaries never read this bucket.
type HistoryDeletionJob struct {
	Index         uint64
	DeletionPoint *externalapi.DomainHash
	PrunedTips    []*externalapi.DomainHash
}

// serialize is version(1) | DeletionPoint(32) | n(uvarint) | PrunedTips(n x 32).
func (job *HistoryDeletionJob) serialize() []byte {
	b := append([]byte{historyDeletionJobVersion}, job.DeletionPoint.ByteSlice()...)
	b = binary.AppendUvarint(b, uint64(len(job.PrunedTips)))
	for _, tip := range job.PrunedTips {
		b = append(b, tip.ByteSlice()...)
	}
	return b
}

func deserializeHistoryDeletionJob(index uint64, b []byte) (*HistoryDeletionJob, error) {
	if len(b) < 1+externalapi.DomainHashSize || b[0] != historyDeletionJobVersion {
		return nil, fmt.Errorf("invalid history deletion job %d", index)
	}
	point, err := externalapi.NewDomainHashFromByteSlice(b[1 : 1+externalapi.DomainHashSize])
	if err != nil {
		return nil, err
	}
	rest := b[1+externalapi.DomainHashSize:]
	n, size := binary.Uvarint(rest)
	if size <= 0 || n > uint64((len(rest)-size)/externalapi.DomainHashSize) || uint64(len(rest)-size) != n*externalapi.DomainHashSize {
		return nil, fmt.Errorf("invalid history deletion job %d", index)
	}
	rest = rest[size:]
	job := &HistoryDeletionJob{Index: index, DeletionPoint: point, PrunedTips: make([]*externalapi.DomainHash, n)}
	for i := range job.PrunedTips {
		if job.PrunedTips[i], err = externalapi.NewDomainHashFromByteSlice(rest[:externalapi.DomainHashSize]); err != nil {
			return nil, err
		}
		rest = rest[externalapi.DomainHashSize:]
	}
	return job, nil
}

func (ps *pruningStore) historyDeletionJobKey(index uint64) model.DBKey {
	var keyBytes [8]byte
	binary.BigEndian.PutUint64(keyBytes[:], index)
	return ps.historyDeletionJobsBucket.Key(keyBytes[:])
}

// StageHistoryDeletionJob stages job into the commit of the finish that schedules it.
func (ps *pruningStore) StageHistoryDeletionJob(stagingArea *model.StagingArea, job *HistoryDeletionJob) {
	ps.stagingShard(stagingArea).historyDeletionJob = job
}

// HistoryDeletionJobs returns the committed jobs, lowest index first. It reads the
// database only, so it is safe without the consensus lock.
func (ps *pruningStore) HistoryDeletionJobs(dbContext model.DBReader) (jobs []*HistoryDeletionJob, err error) {
	cursor, err := dbContext.Cursor(ps.historyDeletionJobsBucket)
	if err != nil {
		return nil, err
	}
	// First/Next also return false on iteration failure. Close reports that error;
	// never let recovery or downgrade interpret a partial list as completed work.
	defer func() {
		if closeErr := cursor.Close(); err == nil && closeErr != nil {
			jobs, err = nil, closeErr
		}
	}()
	for ok := cursor.First(); ok; ok = cursor.Next() {
		key, err := cursor.Key()
		if err != nil {
			return nil, err
		}
		value, err := cursor.Value()
		if err != nil {
			return nil, err
		}
		if len(key.Suffix()) != 8 {
			return nil, fmt.Errorf("invalid history deletion job key %x", key.Suffix())
		}
		job, err := deserializeHistoryDeletionJob(binary.BigEndian.Uint64(key.Suffix()), value)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, nil
}

// DeleteHistoryDeletionJobDirect writes the deletion of job index's record into dbTx,
// the transaction of that job's last batch.
func (ps *pruningStore) DeleteHistoryDeletionJobDirect(dbTx model.DBWriter, index uint64) error {
	return dbTx.Delete(ps.historyDeletionJobKey(index))
}
