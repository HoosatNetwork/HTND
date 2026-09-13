package externalapi

// ConsensusEvent is an interface type that is implemented by all events raised by consensus
type ConsensusEvent interface {
	isConsensusEvent()
}

// BlockAdded is an event raised by consensus when a block was added to the dag
type BlockAdded struct {
	Block *DomainBlock
}

func (*BlockAdded) isConsensusEvent() {}

// VirtualChangeSet is an event raised by consensus when virtual changes
type VirtualChangeSet struct {
	VirtualSelectedParentChainChanges *SelectedChainPath
	VirtualUTXODiff                   UTXODiff
	VirtualParents                    []*DomainHash
	VirtualSelectedParentBlueScore    uint64
	VirtualDAAScore                   uint64

	// EarlierChangeSetsDropped reports that at least one change set this consensus raised before
	// this one was never delivered. Virtual had already been committed when that happened, so a
	// consumer that maintains state by replaying VirtualUTXODiff is missing a diff, and nothing
	// later in the stream makes up for it.
	EarlierChangeSetsDropped bool
}

func (*VirtualChangeSet) isConsensusEvent() {}

// PruningPointUTXOSetOverride is not raised by consensus itself. The node puts it on the consensus
// events channel after swapping in a consensus whose UTXO set was imported, so that its handler runs
// only after every event the replaced consensus raised. The handler's outcome is sent to Done.
type PruningPointUTXOSetOverride struct {
	Done chan error
}

func (*PruningPointUTXOSetOverride) isConsensusEvent() {}

// SelectedChainPath is a path the of the selected chains between two blocks.
type SelectedChainPath struct {
	Added   []*DomainHash
	Removed []*DomainHash
}
