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
