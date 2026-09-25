package blockrelay

import (
	"github.com/HoosatNetwork/HTND/v2/app/protocol/protocolerrors"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
)

// selectedChainReader is the part of a consensus the pruning point chain checks read.
type selectedChainReader interface {
	IsInSelectedParentChainOf(blockHashA *externalapi.DomainHash, blockHashB *externalapi.DomainHash) (bool, error)
}

// ownPruningPointReader is what the serving-side check reads from this node's consensus.
type ownPruningPointReader interface {
	selectedChainReader
	PruningPoint() (*externalapi.DomainHash, error)
	GetHeadersSelectedTip() (*externalapi.DomainHash, error)
}

// namedBlock is a block a pruning point has to be on the selected parent chain of, with the name the
// error message uses for it.
type namedBlock struct {
	name string
	hash *externalapi.DomainHash
}

// checkPruningPointMeetsChains returns a non-banning protocol error unless pruningPoint is on the
// selected parent chain of every block in blocks (nil hashes are skipped).
//
// HTN-196: a headers-proof IBD used to commit the imported pruning point before anything checked that
// it was the root of the chain it had just downloaded. When it was not, the node was left with a
// pruning point that met the tip's chain only at virtual genesis, could sync no block bodies below
// the tip, and could not recover in place. The IBD flow runs this check after the headers are in the
// staging consensus and before the UTXO set is downloaded, so a pruning point that does not meet the
// chain is refused while it can still be thrown away: the staging consensus is deleted, the node
// keeps the state it had, and another peer is tried. The peer is not banned, because the
// disagreement can come from this node's side as well as the peer's.
func checkPruningPointMeetsChains(consensus selectedChainReader, pruningPoint *externalapi.DomainHash,
	blocks []namedBlock,
) error {
	for _, block := range blocks {
		if block.hash == nil {
			continue
		}
		isOnChain, err := consensus.IsInSelectedParentChainOf(pruningPoint, block.hash)
		if err != nil {
			return protocolerrors.Wrapf(false, err, "refusing pruning point %s: could not check whether it is "+
				"on the selected parent chain of %s %s", pruningPoint, block.name, block.hash)
		}
		if !isOnChain {
			return protocolerrors.Errorf(false, "refusing pruning point %s: it is not on the selected parent "+
				"chain of %s %s, so block bodies could not be synced below it (HTN-196)",
				pruningPoint, block.name, block.hash)
		}
	}
	return nil
}

// checkOwnPruningPointMeetsHeadersChain returns a non-banning protocol error unless this node's own
// pruning point is on the selected parent chain of its headers selected tip.
//
// This is the serving side of the same guard. A node hands a syncee its pruning point, and the
// headers it serves run from that pruning point up to its headers selected tip. If its pruning point
// is not on that tip's chain, the pruning point and the headers do not belong together, and a
// syncee that took them would be refused - or, on an older build, would commit a pruning point it
// can never sync block bodies below. Refusing to serve the proof in that state makes the syncee try
// another peer straight away.
func checkOwnPruningPointMeetsHeadersChain(consensus ownPruningPointReader) error {
	pruningPoint, err := consensus.PruningPoint()
	if err != nil {
		return err
	}
	headersSelectedTip, err := consensus.GetHeadersSelectedTip()
	if err != nil {
		return err
	}
	isOnChain, err := consensus.IsInSelectedParentChainOf(pruningPoint, headersSelectedTip)
	if err != nil {
		return err
	}
	if !isOnChain {
		log.Warnf("This node's pruning point %s is not on the selected parent chain of its headers selected "+
			"tip %s. Refusing to serve the pruning point proof until they agree.", pruningPoint, headersSelectedTip)
		return protocolerrors.Errorf(false, "this node's pruning point %s is not on the selected parent "+
			"chain of its headers selected tip %s; not serving a pruning point proof", pruningPoint, headersSelectedTip)
	}
	return nil
}
