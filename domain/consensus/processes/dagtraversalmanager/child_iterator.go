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
	s.queue = []*externalapi.DomainHash{s.lowHash}
	s.queueIndex = 0
	if s.includeLowHash {
		s.current = s.lowHash
		s.queueIndex = 1
		return true
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

	// If there are more items in the queue, get the next one
	if s.queueIndex < len(s.queue) {
		s.current = s.queue[s.queueIndex]
		s.queueIndex++

		// Enqueue all children of the current node for BFS traversal
		children, err := s.dagTraversalManager.Childs(s.stagingArea, s.current)
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

	// Queue is empty, no more items
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

// ChildIterator returns a BlockIterator that iterates from lowHash (exclusive) to highHash (inclusive) BFS over
// highHash's  parent chain
func (dtm *dagTraversalManager) ChildIterator(stagingArea *model.StagingArea,
	highHash, lowHash *externalapi.DomainHash, includeLowHash bool,
) (model.BlockIterator, error) {
	isLowHashInParentChainOfHighHash, err := dtm.dagTopologyManager.IsInSelectedParentChainOf(
		stagingArea, lowHash, highHash)
	if err != nil {
		return nil, err
	}

	if !isLowHashInParentChainOfHighHash {
		return nil, errors.Errorf("%s is not in the  parent chain of %s", lowHash, highHash)
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
	lowHash *externalapi.DomainHash,
) ([]*externalapi.DomainHash, error) {
	// Get all children of lowHash from the reachability tree
	// highHash is kept for interface compatibility but not used in BFS
	children, err := dtm.reachabilityManager.GetChildren(stagingArea, lowHash)
	if err != nil {
		return nil, errors.Wrapf(errNoChild, "no children for %s", lowHash)
	}
	if len(children) == 0 {
		return nil, errNoChild
	}
	return children, nil
}
