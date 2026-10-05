package consensus

import (
	"sync"
	"time"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/ruleerrors"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/multiset"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/utxo"
	"github.com/pkg/errors"
)

// servedUTXOSetMismatchRecheckInterval is how long a mismatch is remembered before the served set is
// hashed again. A match is remembered until the pruning point moves: the bucket only changes for the
// same pruning point when VerifyCurrentPruningPointUTXOSet repairs it, and a repair moves it towards
// the header, not away from it. A mismatch can be repaired, so it is re-checked - but not on every
// request, because a node with a bad set is the one peers keep asking, and each check walks the
// whole set.
const servedUTXOSetMismatchRecheckInterval = 10 * time.Minute

// servedUTXOSetCheck memoises CheckUTXOHealth. mu is held across the walk, so concurrent requests
// from several peers wait for one walk instead of each starting their own.
type servedUTXOSetCheck struct {
	mu        sync.Mutex
	last      *externalapi.ServedUTXOSetHealth
	checkedAt time.Time
}

// CheckUTXOHealth reports whether the pruning point UTXO set this node would serve hashes to the UTXO
// commitment in pruningPointHash's header. See externalapi.ServedUTXOSetHealth for why this is not
// UTXOSetHealth.
//
// The set is hashed without holding s.lock. Walking it takes seconds on mainnet, and block processing
// must not wait for that. The walk reads through one database iterator, which sees a single snapshot,
// and the pruning point and the in-progress flag are read before and after it: if either changed, the
// snapshot may predate or straddle a rewrite of the bucket, and the answer is discarded as not Ready.
func (s *consensus) CheckUTXOHealth(pruningPointHash *externalapi.DomainHash) (*externalapi.ServedUTXOSetHealth, error) {
	s.servedUTXOSetCheck.mu.Lock()
	defer s.servedUTXOSetCheck.mu.Unlock()

	headerCommitment, updating, err := s.servedUTXOSetState(pruningPointHash)
	if err != nil {
		return nil, err
	}
	notReady := &externalapi.ServedUTXOSetHealth{
		Ready:            false,
		PruningPoint:     pruningPointHash,
		HeaderCommitment: headerCommitment,
	}
	if updating {
		return notReady, nil
	}

	last := s.servedUTXOSetCheck.last
	if last != nil && last.PruningPoint.Equal(pruningPointHash) &&
		(last.Verified || time.Since(s.servedUTXOSetCheck.checkedAt) < servedUTXOSetMismatchRecheckInterval) {
		return last, nil
	}

	setMultiset, entryCount, err := s.hashServedPruningPointUTXOSet()
	if err != nil {
		return nil, err
	}

	_, updating, err = s.servedUTXOSetState(pruningPointHash)
	if errors.Is(err, ruleerrors.ErrWrongPruningPointHash) || (err == nil && updating) {
		return notReady, nil
	}
	if err != nil {
		return nil, err
	}

	health := &externalapi.ServedUTXOSetHealth{
		Verified:         setMultiset.Equal(headerCommitment),
		Ready:            true,
		PruningPoint:     pruningPointHash,
		SetMultiset:      setMultiset,
		HeaderCommitment: headerCommitment,
		EntryCount:       entryCount,
	}
	s.servedUTXOSetCheck.last = health
	s.servedUTXOSetCheck.checkedAt = time.Now()

	if health.Verified {
		log.Infof("The pruning point %s UTXO set this node serves (%d entries) matches its header "+
			"commitment %s", pruningPointHash, entryCount, headerCommitment)
	} else {
		log.Warnf("The pruning point %s UTXO set this node serves (%d entries) hashes to %s, but its "+
			"header commits to %s - not serving it; re-checking in %s", pruningPointHash, entryCount,
			setMultiset, headerCommitment, servedUTXOSetMismatchRecheckInterval)
	}
	return health, nil
}

// servedUTXOSetState reads, under s.lock, the UTXO commitment in pruningPointHash's header and
// whether the pruning point UTXO set is part way through being rewritten. It returns
// ErrWrongPruningPointHash when pruningPointHash is not the current pruning point.
func (s *consensus) servedUTXOSetState(pruningPointHash *externalapi.DomainHash) (
	headerCommitment *externalapi.DomainHash, updating bool, err error,
) {
	s.lock.Lock()
	defer s.lock.Unlock()

	stagingArea := model.NewStagingArea()

	currentPruningPoint, err := s.pruningStore.PruningPoint(s.databaseContext, stagingArea)
	if err != nil {
		return nil, false, err
	}
	if !pruningPointHash.Equal(currentPruningPoint) {
		return nil, false, errors.Wrapf(ruleerrors.ErrWrongPruningPointHash, "expected pruning point %s but got %s",
			pruningPointHash, currentPruningPoint)
	}

	header, err := s.blockHeaderStore.BlockHeader(s.databaseContext, stagingArea, pruningPointHash)
	if err != nil {
		return nil, false, err
	}

	updating, err = s.pruningStore.HadStartedUpdatingPruningPointUTXOSet(s.databaseContext)
	if err != nil {
		return nil, false, err
	}
	return header.UTXOCommitment(), updating, nil
}

// hashServedPruningPointUTXOSet hashes every entry in the bucket GetPruningPointUTXOs serves from.
// It does not take s.lock; see CheckUTXOHealth.
func (s *consensus) hashServedPruningPointUTXOSet() (*externalapi.DomainHash, uint64, error) {
	iterator, err := s.pruningStore.PruningPointUTXOIterator(s.databaseContext)
	if err != nil {
		return nil, 0, err
	}
	defer iterator.Close()

	setMultiset := multiset.New()
	var entryCount uint64
	for ok := iterator.First(); ok; ok = iterator.Next() {
		outpoint, entry, err := iterator.Get()
		if err != nil {
			return nil, 0, err
		}
		serializedUTXO, err := utxo.SerializeUTXO(entry, outpoint)
		if err != nil {
			return nil, 0, err
		}
		setMultiset.Add(serializedUTXO)
		entryCount++
	}
	return setMultiset.Hash(), entryCount, nil
}
