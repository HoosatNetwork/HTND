package dagtraversalmanager

import (
	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/pkg/errors"
)

type ChildIterator struct {
	dagTraversalManager model.DAGTraversalManager

	includeLowHash    bool
	highHash, lowHash *externalapi.DomainHash
	current           *externalapi.DomainHash
	err               error
	isClosed          bool
	stagingArea       *model.StagingArea
	queue             []*externalapi.DomainHash
	queueIndex        int
}

func (s *ChildIterator) First() bool {
	if s.isClosed {
		panic("Tried using a closed ChildIterator")
	}
	s.queue = []*externalapi.DomainHash{}
	s.queueIndex = 0
	s.current = nil
	s.err = nil

	if s.includeLowHash {
		s.current = s.lowHash
		// Enqueue children of lowHash so the walk can continue to highHash
		children, err := s.dagTraversalManager.Childs(s.stagingArea, s.highHash, s.lowHash)
		if err != nil && !errors.Is(err, errNoChild) {
			s.err = err
			return true
		}
		if children != nil {
			s.queue = append(s.queue, children...)
		}
		return true
	}

	// Exclusive: process lowHash (enqueue its children) without yielding it,
	// then advance to the first real item.
	children, err := s.dagTraversalManager.Childs(s.stagingArea, s.highHash, s.lowHash)
	if err != nil && !errors.Is(err, errNoChild) {
		s.current = nil
		s.err = err
		return true
	}
	if children != nil {
		s.queue = append(s.queue, children...)
	}
	return s.Next()
}

func (s *ChildIterator) Next() bool {
	if s.isClosed {
		panic("Tried using a closed ChildIterator")
	}
	if s.err != nil {
		return true
	}

	// Already yielded highHash → stop (inclusive end)
	if s.current != nil && s.current.Equal(s.highHash) {
		s.current = nil
		return false
	}

	if s.queueIndex < len(s.queue) {
		s.current = s.queue[s.queueIndex]
		s.queueIndex++

		// Reached highHash: do not enqueue further children and discard any
		// remaining queue items so the iterator stops after this yield.
		if s.current.Equal(s.highHash) {
			s.queue = s.queue[:s.queueIndex]
			return true
		}

		// Enqueue children that still lead toward highHash
		children, err := s.dagTraversalManager.Childs(s.stagingArea, s.highHash, s.current)
		if err != nil && !errors.Is(err, errNoChild) {
			s.current = nil
			s.err = err
			return true
		}
		if children != nil {
			s.queue = append(s.queue, children...)
		}
		return true
	}

	// Queue exhausted
	s.current = nil
	return false
}

func (s *ChildIterator) Get() (*externalapi.DomainHash, error) {
	if s.isClosed {
		return nil, errors.New("Tried using a closed ChildIterator")
	}
	return s.current, s.err
}

func (s *ChildIterator) Close() error {
	if s.isClosed {
		return errors.New("Tried using a closed ChildIterator")
	}
	s.isClosed = true
	s.highHash = nil
	s.lowHash = nil
	s.current = nil
	s.err = nil
	s.queue = nil
	s.queueIndex = 0
	return nil
}

// ChildIterator returns a BlockIterator that iterates from lowHash (exclusive) to highHash (inclusive)
// BFS over the reachability-tree path from lowHash toward highHash.
func (dtm *dagTraversalManager) ChildIterator(stagingArea *model.StagingArea,
	highHash, lowHash *externalapi.DomainHash, includeLowHash bool,
) (model.BlockIterator, error) {
	isLowHashInParentChainOfHighHash, err := dtm.dagTopologyManager.IsInSelectedParentChainOf(
		stagingArea, lowHash, highHash)
	if err != nil {
		return nil, err
	}

	if !isLowHashInParentChainOfHighHash {
		return nil, errors.Errorf("%s is not in the parent chain of %s", lowHash, highHash)
	}
	return &ChildIterator{
		dagTraversalManager: dtm,
		includeLowHash:      includeLowHash,
		highHash:            highHash,
		lowHash:             lowHash,
		current:             lowHash,
		stagingArea:         stagingArea,
	}, nil
}

var errNoChild = errors.New("errNoChild")

func (dtm *dagTraversalManager) Childs(stagingArea *model.StagingArea,
	highHash, lowHash *externalapi.DomainHash,
) ([]*externalapi.DomainHash, error) {
	children, err := dtm.reachabilityManager.GetChildren(stagingArea, lowHash)
	if err != nil {
		return nil, errors.Wrapf(errNoChild, "no children for %s", lowHash)
	}
	filtered := make([]*externalapi.DomainHash, 0, len(children))
	for _, child := range children {
		isAncestorOfHigh, err := dtm.dagTopologyManager.IsAncestorOf(stagingArea, child, highHash)
		if err != nil {
			return nil, err
		}
		if isAncestorOfHigh || child.Equal(highHash) {
			filtered = append(filtered, child)
		}
	}
	if len(filtered) == 0 {
		return nil, errNoChild
	}
	return filtered, nil
}
