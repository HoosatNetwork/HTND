package externalapi

import "github.com/pkg/errors"

// ErrVirtualHasNoUsableTip is returned by ResolveVirtual when it ran to completion but every tip it
// could have moved virtual onto is disqualified or invalid, so virtual is left at the virtual genesis
// marker. It is a recoverable state rather than a broken DAG: the block statuses say no chain is
// usable, while the blocks themselves may be fine.
//
// It is a sentinel so callers can recognise the case with errors.Is instead of matching the message.
var ErrVirtualHasNoUsableTip = errors.New(
	"ResolveVirtual finished with no UTXO-valid/pending tip (all tips disqualified or invalid); " +
		"virtual cannot leave VirtualGenesis")

// ErrPruningPointDataDoesNotReconcile is returned by GetMissingBlockBodyHashes when this node's
// pruning point cannot be reconciled with the syncer chain it was asked to fill in bodies for - the
// pruning point is not on that chain's selected parent chain and no usable shared ancestor could be
// found (see syncmanager.missingBlockBodyHashes). It used to be swallowed into an empty result with a
// nil error so IBD would report success, on the theory that block relay would pick up the tip from
// there - but with no bodies below the headers tip, relay can never add a block, and a peer whose
// chain has this shape gives the same empty answer forever, so IBD "succeeds" in a loop and the node
// never actually syncs (HTN-196).
//
// It is a sentinel so callers can recognise the case with errors.Is and fail that IBD attempt instead
// of reporting success, so the node disconnects and tries a different peer.
var ErrPruningPointDataDoesNotReconcile = errors.New(
	"the pruning point cannot be reconciled with the syncer's chain: not on its selected parent " +
		"chain, and no usable shared ancestor was found - block bodies cannot be synced from here")
