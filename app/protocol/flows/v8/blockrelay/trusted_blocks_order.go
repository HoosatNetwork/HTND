package blockrelay

import (
	"github.com/HoosatNetwork/HTND/app/appmessage"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/consensushashing"
)

// orderBlocksWithTrustedDataTopologically returns blocks reordered so that each block comes after
// every one of its direct parents that is also in blocks. Blocks that are already in a valid order
// keep it, so a well-behaved peer's order is left unchanged.
//
// The syncee inserts the pruning point anticone in the order it is handed, and a block inserted
// before one of its parents treats that parent as pruned: its selected parent becomes virtual
// genesis and its merge set and reachability lose the parent. Every block colored on top of it can
// then differ from the syncer's DAG, which is one way the imported pruning point ends up off the
// tip's selected chain (HTN-196). Peers order this set by the blue work written in each header,
// which nothing validates, so the syncee cannot rely on their order and fixes it itself.
//
// A set with a cycle cannot come from a real DAG; whatever cannot be placed is appended in the
// original order so that consensus, not this function, rejects it.
func orderBlocksWithTrustedDataTopologically(
	blocks []*appmessage.MsgBlockWithTrustedDataV4,
) []*appmessage.MsgBlockWithTrustedDataV4 {
	if len(blocks) < 2 {
		return blocks
	}

	hashes := make([]externalapi.DomainHash, len(blocks))
	inSet := make(map[externalapi.DomainHash]struct{}, len(blocks))
	for i, block := range blocks {
		hashes[i] = *consensushashing.HeaderHash(appmessage.BlockHeaderToDomainBlockHeader(&block.Block.Header))
		inSet[hashes[i]] = struct{}{}
	}

	placed := make(map[externalapi.DomainHash]struct{}, len(blocks))
	isPlaced := make([]bool, len(blocks))
	ordered := make([]*appmessage.MsgBlockWithTrustedDataV4, 0, len(blocks))
	for len(ordered) < len(blocks) {
		progress := false
		for i, block := range blocks {
			if isPlaced[i] {
				continue
			}
			ready := true
			for _, parent := range appmessage.BlockHeaderToDomainBlockHeader(&block.Block.Header).DirectParents() {
				if _, isInSet := inSet[*parent]; !isInSet {
					continue
				}
				if _, isParentPlaced := placed[*parent]; !isParentPlaced {
					ready = false
					break
				}
			}
			if !ready {
				continue
			}
			ordered = append(ordered, block)
			placed[hashes[i]] = struct{}{}
			isPlaced[i] = true
			progress = true
		}
		if !progress {
			for i, block := range blocks {
				if !isPlaced[i] {
					ordered = append(ordered, block)
				}
			}
			break
		}
	}
	return ordered
}
