package flowcontext

import (
	"math/big"
	"testing"

	"github.com/HoosatNetwork/HTND/domain"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/blockheader"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/consensushashing"
)

type knownBlocksConsensus struct {
	externalapi.Consensus
	blocks map[externalapi.DomainHash]*externalapi.DomainBlock
}

func (c knownBlocksConsensus) GetBlock(hash *externalapi.DomainHash) (*externalapi.DomainBlock, bool, error) {
	block, ok := c.blocks[*hash]
	return block, ok, nil
}

type knownBlocksDomain struct {
	domain.Domain
	consensus knownBlocksConsensus
}

func (d knownBlocksDomain) Consensus() externalapi.Consensus { return d.consensus }

func testBlock(parents ...*externalapi.DomainHash) *externalapi.DomainBlock {
	return &externalapi.DomainBlock{
		Header: blockheader.NewImmutableBlockHeader(1, []externalapi.BlockLevelParents{parents},
			&externalapi.DomainHash{}, &externalapi.DomainHash{}, &externalapi.DomainHash{},
			0, 0, uint64(len(parents)), 0, 0, big.NewInt(0), &externalapi.DomainHash{}),
		PoWHash: "pow",
	}
}

// TestGetOrphanRootsSkipsKnownParents pins that an orphan's roots are only the ancestors this node is
// missing. A parent the node already holds used to be reported as a root too, so it was queued to be
// requested again.
func TestGetOrphanRootsSkipsKnownParents(t *testing.T) {
	knownParent := testBlock()
	knownParentHash := consensushashing.BlockHash(knownParent)
	missingParentHash := externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{1})

	orphan := testBlock(knownParentHash, missingParentHash)
	orphanHash := consensushashing.BlockHash(orphan)

	f := New(nil, knownBlocksDomain{consensus: knownBlocksConsensus{
		blocks: map[externalapi.DomainHash]*externalapi.DomainBlock{*knownParentHash: knownParent},
	}}, nil, nil, nil)
	f.AddOrphan(orphan)

	roots, orphanExists, err := f.GetOrphanRoots(orphanHash)
	if err != nil {
		t.Fatalf("GetOrphanRoots: %+v", err)
	}
	if !orphanExists {
		t.Fatalf("expected the orphan to exist")
	}
	if len(roots) != 1 || !roots[0].Equal(missingParentHash) {
		t.Fatalf("expected only the missing parent %s as a root, got %v", missingParentHash, roots)
	}
}
