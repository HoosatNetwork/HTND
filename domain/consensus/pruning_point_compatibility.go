package consensus

import (
	"context"
	"fmt"

	consensusdatabase "github.com/HoosatNetwork/HTND/v2/domain/consensus/database"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/datastructures/pruningstore"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/processes/pruningmanager"
	"github.com/HoosatNetwork/HTND/v2/domain/prefixmanager/prefix"
	infrastructuredatabase "github.com/HoosatNetwork/HTND/v2/infrastructure/db/database"
)

type pruningDowngradeLock struct{}

func (pruningDowngradeLock) Lock()   {}
func (pruningDowngradeLock) Unlock() {}

// PreparePruningPointDowngrade is a terminal, internal/operator entry point. Stop
// network/RPC admission first and close the database immediately after it returns.
// It joins the owner, then holds consensus exclusively through drain, verification,
// atomic deletion and cleanup. Interrupted normalization must resume with this binary.
func (s *consensus) PreparePruningPointDowngrade(ctx context.Context) error {
	s.Close()
	s.lock.Lock()
	defer s.lock.Unlock()
	// The outer consensus lock excludes every mutation throughout normalization.
	if err := s.pruningManager.ScanPruningPointUTXOSet(ctx, pruningDowngradeLock{}); err != nil {
		return err
	}
	if err := pruningmanager.FinishJournalDowngrade(s.pruningManager, ctx); err != nil {
		return err
	}
	// The previous release would delete the remainder of a job itself, top-down; deleting it here leaves no job
	// record, and the normalized counts an exact blocks-count, the only count the previous release keeps.
	if err := pruningmanager.RunHistoryDeletionJobs(s.pruningManager, ctx, nil); err != nil {
		return err
	}
	return pruningmanager.NormalizeBlockCountsForDowngrade(s.pruningManager)
}

// PrepareDowngrade is what htnd --prepare-downgrade runs, with the node's own
// configuration and active prefix and no other component started. It opens the
// consensus as a node start does, finishes any unfinished
// pruning point UTXO journal job, checks the set against its commitment, runs the
// history deletion, removes the journal so the previous release can open the
// database, and stops the consensus again. It returns what it did; db stays open.
// Interrupted (ctx) or refused for disk space, it leaves a job this binary resumes.
func PrepareDowngrade(ctx context.Context, config *Config, db infrastructuredatabase.Database, dbPrefix *prefix.Prefix) (string, error) {
	before, err := pruningstore.NewJournal(consensusdatabase.New(db), consensusdatabase.MakeBucket(dbPrefix.Serialize()), nil).Load()
	if err != nil {
		return "", err
	}
	found := "no pruning point UTXO journal"
	if before != nil {
		found = fmt.Sprintf("a pruning point UTXO journal at phase %s for pruning point index %d (%d of %d records applied)",
			before.Phase, before.TargetIndex, before.Applied, before.Count)
	}
	log.Infof("Preparing the database for a downgrade; found %s", found)
	instance, _, err := NewFactory().NewConsensus(config, db, dbPrefix, nil)
	if err != nil {
		return "", err
	}
	s := instance.(*consensus)
	if err = s.PreparePruningPointDowngrade(ctx); err != nil {
		return "", err
	}
	point, err := s.PruningPoint()
	if err != nil {
		return "", err
	}
	pending, err := s.pruningStore.HadStartedUpdatingPruningPointUTXOSet(s.databaseContext)
	if err != nil {
		return "", err
	}
	done := "every pruning point UTXO set update is finished and checked against its commitment"
	if pending {
		// Only without a journal: a pending legacy update is the previous release's own format.
		done = "a legacy pruning point UTXO set update is still pending, which the previous release finishes itself"
	}
	return fmt.Sprintf("found %s; %s; the journal is removed; pruning point %s; the previous release can open this database",
		found, done, point), nil
}
