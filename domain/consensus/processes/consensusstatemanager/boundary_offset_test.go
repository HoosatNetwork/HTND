package consensusstatemanager

import (
	"math/big"
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/blockheader"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/multiset"
	"github.com/pkg/errors"
)

// Fakes for the four stores blockInheritsKnownUTXOCommitmentOffset, confirmBaselineOffsetIfBoundaryBlock
// and UTXOSetHealth read. Each is keyed by block hash so a single csm can be given the shape of a real
// post-import DAG: a pruning point whose own multiset matches its own header, and a child of it that
// does not.

type fixedPruningPointStore struct {
	model.PruningStore
	pruningPoint *externalapi.DomainHash
}

func (s fixedPruningPointStore) HasPruningPoint(model.DBReader, *model.StagingArea) (bool, error) {
	return s.pruningPoint != nil, nil
}

func (s fixedPruningPointStore) PruningPoint(model.DBReader, *model.StagingArea) (*externalapi.DomainHash, error) {
	if s.pruningPoint == nil {
		return nil, errors.New("no pruning point")
	}
	return s.pruningPoint, nil
}

type byHashGHOSTDAGDataStore struct {
	model.GHOSTDAGDataStore
	data map[externalapi.DomainHash]*externalapi.BlockGHOSTDAGData
}

func (s byHashGHOSTDAGDataStore) Get(_ model.DBReader, _ *model.StagingArea,
	blockHash *externalapi.DomainHash, _ bool,
) (*externalapi.BlockGHOSTDAGData, error) {
	if data, ok := s.data[*blockHash]; ok {
		return data, nil
	}
	return nil, errors.New("no GHOSTDAG data")
}

type byHashMultisetStore struct {
	model.MultisetStore
	sets map[externalapi.DomainHash]model.Multiset
}

func (s byHashMultisetStore) Get(_ model.DBReader, _ *model.StagingArea,
	blockHash *externalapi.DomainHash,
) (model.Multiset, error) {
	if ms, ok := s.sets[*blockHash]; ok {
		return ms, nil
	}
	return nil, errors.New("no multiset")
}

type byHashHeaderStore struct {
	model.BlockHeaderStore
	headers map[externalapi.DomainHash]externalapi.BlockHeader
}

func (s byHashHeaderStore) BlockHeader(_ model.DBReader, _ *model.StagingArea,
	blockHash *externalapi.DomainHash,
) (externalapi.BlockHeader, error) {
	if h, ok := s.headers[*blockHash]; ok {
		return h, nil
	}
	return nil, errors.New("no header")
}

func boundaryTestHash(b byte) *externalapi.DomainHash {
	return externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{b})
}

func boundaryTestHeader(utxoCommitment *externalapi.DomainHash) externalapi.BlockHeader {
	return blockheader.NewImmutableBlockHeader(1, nil, &externalapi.DomainHash{}, &externalapi.DomainHash{},
		utxoCommitment, 0, 0, 0, 1, 0, big.NewInt(0), &externalapi.DomainHash{})
}

// buildBoundaryTestCSM stages the shape verifyAndRepairImportedPruningPointUTXOSet leaves an imported
// pruning point in: the pruning point's own stored multiset matches its own header (msMatch), a
// healthy block elsewhere in the DAG whose selected parent's multiset also matches (so signal 2 is
// false for it), and the boundary block whose selected parent IS the pruning point.
func buildBoundaryTestCSM(t *testing.T) (csm *consensusStateManager, pruningPoint, boundaryBlock,
	healthyBlock, healthyBlockParent *externalapi.DomainHash,
) {
	t.Helper()

	genesisHash := boundaryTestHash(0xff)
	pruningPoint = boundaryTestHash(1)
	boundaryBlock = boundaryTestHash(2)
	healthyBlock = boundaryTestHash(3)
	healthyBlockParent = boundaryTestHash(4)

	msMatch := multiset.New()
	matchingCommitment := msMatch.Hash()

	csm = &consensusStateManager{
		genesisHash:  genesisHash,
		pruningStore: fixedPruningPointStore{pruningPoint: pruningPoint},
		ghostdagDataStore: byHashGHOSTDAGDataStore{data: map[externalapi.DomainHash]*externalapi.BlockGHOSTDAGData{
			*boundaryBlock: externalapi.NewBlockGHOSTDAGData(1, big.NewInt(1), pruningPoint, nil, nil, nil, 1),
			*healthyBlock:  externalapi.NewBlockGHOSTDAGData(1, big.NewInt(1), healthyBlockParent, nil, nil, nil, 1),
		}},
		multisetStore: byHashMultisetStore{sets: map[externalapi.DomainHash]model.Multiset{
			*pruningPoint:       msMatch,
			*healthyBlockParent: msMatch,
		}},
		blockHeaderStore: byHashHeaderStore{headers: map[externalapi.DomainHash]externalapi.BlockHeader{
			*pruningPoint:       boundaryTestHeader(matchingCommitment),
			*healthyBlockParent: boundaryTestHeader(matchingCommitment),
		}},
	}
	return csm, pruningPoint, boundaryBlock, healthyBlock, healthyBlockParent
}

// TestBlockInheritsKnownUTXOCommitmentOffsetCoversTheBoundaryBlock is HTN-208's fix option 1: the
// very first block above an imported pruning point has nothing to inherit an offset from except the
// pruning point itself, and the pruning point's own multiset was already checked against its own
// header at import time - so signal 2 (selected parent's multiset vs its header) is structurally
// blind to it. Without the fix this returns false and a genuine boundary mismatch strands the node.
func TestBlockInheritsKnownUTXOCommitmentOffsetCoversTheBoundaryBlock(t *testing.T) {
	csm, _, boundaryBlock, healthyBlock, _ := buildBoundaryTestCSM(t)
	stagingArea := model.NewStagingArea()

	if !csm.blockInheritsKnownUTXOCommitmentOffset(stagingArea, boundaryBlock) {
		t.Error("the first block above the pruning point must be treated as offset-eligible")
	}

	// Self-scoping: a block elsewhere in the DAG, whose selected parent's multiset genuinely agrees
	// with its header, must NOT be swept into the same toleration.
	if csm.blockInheritsKnownUTXOCommitmentOffset(stagingArea, healthyBlock) {
		t.Error("a block on a healthy chain must not be reported as inheriting an offset")
	}
}

// TestConfirmBaselineOffsetMakesUTXOSetHealthHonest is HTN-208's fix option 2: once the boundary
// block has demonstrably failed its own commitment check, UTXOSetHealth (and so GetInfo's
// IsUtxoSetVerified) must stop reporting the baseline as verified, even though the pruning point's
// own stored multiset still hashes correctly against its own header - that hash alone cannot tell
// the two situations apart, and re-hashing it again answers "verified" forever.
func TestConfirmBaselineOffsetMakesUTXOSetHealthHonest(t *testing.T) {
	csm, pruningPoint, boundaryBlock, _, _ := buildBoundaryTestCSM(t)
	stagingArea := model.NewStagingArea()

	healthBefore := csm.UTXOSetHealth(stagingArea)
	if !healthBefore.Checked || !healthBefore.BaselineVerified {
		t.Fatalf("expected the unmodified fixture to read as a verified baseline before the boundary "+
			"block fails, got Checked=%t BaselineVerified=%t", healthBefore.Checked, healthBefore.BaselineVerified)
	}

	csm.confirmBaselineOffsetIfBoundaryBlock(stagingArea, boundaryBlock)

	health := csm.UTXOSetHealth(stagingArea)
	if !health.Checked {
		t.Fatal("expected a checked result - the pruning point is still readable")
	}
	if health.BaselineVerified {
		t.Error("UTXOSetHealth must stop reporting the baseline verified once the boundary block has " +
			"demonstrably disagreed, or GetInfo keeps claiming a health the chain doesn't have")
	}
	if !health.StoredMultiset.Equal(health.HeaderCommitment) {
		t.Error("the pruning point's own hash still matches - only the honesty of BaselineVerified " +
			"should change, not the underlying values it was derived from")
	}
	if !csm.pruningPointBaselineIsOffset(stagingArea) {
		t.Error("pruningPointBaselineIsOffset must now report the offset too, so every later block " +
			"is covered by signal 1")
	}
	_ = pruningPoint
}

// TestConfirmBaselineOffsetIsScopedToTheBoundaryBlock pins that marking the baseline offset only
// fires for the block whose selected parent actually is the pruning point - calling it for an
// unrelated block (as could happen if a caller passed the wrong hash) must be a no-op, matching the
// self-scoping the rest of the toleration mechanism relies on.
func TestConfirmBaselineOffsetIsScopedToTheBoundaryBlock(t *testing.T) {
	csm, _, _, healthyBlock, _ := buildBoundaryTestCSM(t)
	stagingArea := model.NewStagingArea()

	csm.confirmBaselineOffsetIfBoundaryBlock(stagingArea, healthyBlock)

	health := csm.UTXOSetHealth(stagingArea)
	if !health.BaselineVerified {
		t.Error("a block whose selected parent is not the pruning point must not be able to mark the " +
			"baseline offset")
	}
}
