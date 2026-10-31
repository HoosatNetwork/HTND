package flowcontext

import (
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/consensushashing"
)

// TestAddOrphanRefusesBlockWhoseParentsArrived: the relay flow refuses a block for a missing parent, asks the
// peer for a block locator, and only then adds the block to the orphan set. A parent that arrives in between
// has already run UnorphanBlocks, which found no orphan child. The block used to be added anyway, with no
// missing ancestor left to request, and stayed in the orphan set - with every block later built on it - until
// an IBD fetched them all again. AddOrphan must refuse such a block so that the caller processes it again.
func TestAddOrphanRefusesBlockWhoseParentsArrived(t *testing.T) {
	parent := testBlock()
	parentHash := consensushashing.BlockHash(parent)
	child := testBlock(parentHash)
	childHash := consensushashing.BlockHash(child)

	for _, tc := range []struct {
		name       string
		blocks     map[externalapi.DomainHash]*externalapi.DomainBlock
		headerOnly map[externalapi.DomainHash]bool
		wantAdded  bool
	}{
		{name: "parent still missing", wantAdded: true},
		{name: "parent header only", wantAdded: true,
			blocks:     map[externalapi.DomainHash]*externalapi.DomainBlock{*parentHash: parent},
			headerOnly: map[externalapi.DomainHash]bool{*parentHash: true}},
		{name: "parent arrived", wantAdded: false,
			blocks: map[externalapi.DomainHash]*externalapi.DomainBlock{*parentHash: parent}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := New(nil, knownBlocksDomain{consensus: knownBlocksConsensus{blocks: tc.blocks, headerOnly: tc.headerOnly}},
				nil, nil, nil)
			// The parent's own UnorphanBlocks ran before the child reached the orphan set, as in the race.
			if _, err := f.UnorphanBlocks(parent); err != nil {
				t.Fatalf("UnorphanBlocks: %+v", err)
			}
			added, err := f.AddOrphan(child)
			if err != nil {
				t.Fatalf("AddOrphan: %+v", err)
			}
			if added != tc.wantAdded || f.IsOrphan(childHash) != tc.wantAdded {
				t.Fatalf("AddOrphan returned %t and IsOrphan %t, want both %t",
					added, f.IsOrphan(childHash), tc.wantAdded)
			}
		})
	}
}
