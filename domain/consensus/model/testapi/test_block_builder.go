package testapi

import (
	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
)

// TestBlockBuilder adds to the main BlockBuilder methods required by tests
type TestBlockBuilder interface {
	model.BlockBuilder

	// BuildBlockWithParents builds a block with provided parents, coinbaseData and transactions,
	// and returns the block together with its past UTXO-diff from the virtual.
	BuildBlockWithParents(parentHashes []*externalapi.DomainHash, coinbaseData *externalapi.DomainCoinbaseData,
		transactions []*externalapi.DomainTransaction) (*externalapi.DomainBlock, externalapi.UTXODiff, error)

	BuildUTXOInvalidHeader(parentHashes []*externalapi.DomainHash) (externalapi.BlockHeader, error)

	BuildUTXOInvalidBlock(parentHashes []*externalapi.DomainHash) (*externalapi.DomainBlock,
		error)

	SetNonceCounter(nonceCounter uint64)

	// EnableUniqueDefaultCoinbaseExtraData makes every subsequently built block that's
	// given a nil coinbaseData get distinct coinbase ExtraData instead of the shared
	// empty default. Some tests mine multiple chains/siblings from the same ancestor,
	// which - without this - produce blocks that share both blue score and (default)
	// coinbase data, and therefore collide on their coinbase transaction ID, since
	// per-block entropy is only folded into the coinbase payload from block version 8
	// onward (see the coinbasemanager package). Off by default so tests that rely on
	// deterministic block hashes (e.g. for tie-break ordering) are unaffected.
	EnableUniqueDefaultCoinbaseExtraData()
}
