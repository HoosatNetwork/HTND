package blockrelay

import (
	"math/big"
	"testing"

	"github.com/HoosatNetwork/HTND/app/appmessage"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/blockheader"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/consensushashing"
)

func trustedBlockWithParents(nonce uint64, blueWork int64, parents ...*externalapi.DomainHash) (
	*appmessage.MsgBlockWithTrustedDataV4, *externalapi.DomainHash,
) {
	header := blockheader.NewImmutableBlockHeader(10, []externalapi.BlockLevelParents{parents},
		&externalapi.DomainHash{}, &externalapi.DomainHash{}, &externalapi.DomainHash{}, 0, 0, nonce, 0, 0,
		big.NewInt(blueWork), &externalapi.DomainHash{})
	return &appmessage.MsgBlockWithTrustedDataV4{
		Block: &appmessage.MsgBlock{Header: *appmessage.DomainBlockHeaderToBlockHeader(header)},
	}, consensushashing.HeaderHash(header)
}

func headerHashOf(block *appmessage.MsgBlockWithTrustedDataV4) *externalapi.DomainHash {
	return consensushashing.HeaderHash(appmessage.BlockHeaderToDomainBlockHeader(&block.Block.Header))
}

// TestOrderBlocksWithTrustedDataTopologically pins the syncee half of the HTN-196 anticone order fix:
// whatever order a peer sends the pruning point anticone in, the syncee inserts every block after
// its parents, and an order that is already valid is kept as sent.
func TestOrderBlocksWithTrustedDataTopologically(t *testing.T) {
	outside := externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{0xff})

	// a <- b <- c, with c also merging a, and d beside them. The blue work written in the headers
	// runs backwards, the way a peer ordering by unvalidated header claims would get it wrong.
	a, aHash := trustedBlockWithParents(1, 40, outside)
	b, bHash := trustedBlockWithParents(2, 30, aHash)
	c, _ := trustedBlockWithParents(3, 20, bHash, aHash)
	d, _ := trustedBlockWithParents(4, 10, outside)

	assertTopological := func(ordered []*appmessage.MsgBlockWithTrustedDataV4) {
		position := map[externalapi.DomainHash]int{}
		for i, block := range ordered {
			position[*headerHashOf(block)] = i
		}
		if len(position) != 4 {
			t.Fatalf("expected the 4 blocks back, got %d distinct", len(position))
		}
		for i, block := range ordered {
			for _, parent := range appmessage.BlockHeaderToDomainBlockHeader(&block.Block.Header).DirectParents() {
				if parentPosition, isInSet := position[*parent]; isInSet && parentPosition > i {
					t.Fatalf("block %s is placed before its parent %s", headerHashOf(block), parent)
				}
			}
		}
	}

	byClaimedBlueWork := []*appmessage.MsgBlockWithTrustedDataV4{d, c, b, a}
	assertTopological(orderBlocksWithTrustedDataTopologically(byClaimedBlueWork))

	alreadyOrdered := []*appmessage.MsgBlockWithTrustedDataV4{d, a, b, c}
	for i, block := range orderBlocksWithTrustedDataTopologically(alreadyOrdered) {
		if block != alreadyOrdered[i] {
			t.Fatalf("an order that was already topological was changed at position %d", i)
		}
	}
}
