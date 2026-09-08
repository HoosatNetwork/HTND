package appmessage

// GetInfoRequestMessage is an appmessage corresponding to
// its respective RPC message
type GetInfoRequestMessage struct {
	baseMessage
}

// Command returns the protocol command string for the message
func (msg *GetInfoRequestMessage) Command() MessageCommand {
	return CmdGetInfoRequestMessage
}

// NewGetInfoRequestMessage returns a instance of the message
func NewGetInfoRequestMessage() *GetInfoRequestMessage {
	return &GetInfoRequestMessage{}
}

// GetInfoResponseMessage is an appmessage corresponding to
// its respective RPC message
type GetInfoResponseMessage struct {
	baseMessage
	P2PID         string
	MempoolSize   uint64
	ServerVersion string
	IsUtxoIndexed bool
	IsSynced      bool
	// IsUtxoSetVerified is true only when this node's pruning point UTXO set hashes to the UTXO
	// commitment in the pruning point's own header. IsSynced says the node believes it has caught
	// up; this says whether what it caught up to can be trusted. They are different questions, and
	// a node can answer true to the first and false to the second.
	IsUtxoSetVerified bool

	// CirculatingSompiSupply is what this node's UTXO set currently holds, and ReferenceSompiSupply
	// is the fixed published snapshot to compare it against. Both zero without --utxoindex, or while
	// the index is resyncing. The growth between them is for comparing nodes, not for judging one in
	// isolation: emission makes every healthy node exceed a past snapshot.
	CirculatingSompiSupply     uint64
	ReferenceSompiSupply       uint64
	ReferenceSupplyDescription string

	Error *RPCError
}

// Command returns the protocol command string for the message
func (msg *GetInfoResponseMessage) Command() MessageCommand {
	return CmdGetInfoResponseMessage
}

// NewGetInfoResponseMessage returns a instance of the message
func NewGetInfoResponseMessage(p2pID string, mempoolSize uint64, serverVersion string, isUtxoIndexed bool,
	isSynced bool, isUTXOSetVerified bool, circulatingSompiSupply uint64, referenceSompiSupply uint64,
	referenceSupplyDescription string,
) *GetInfoResponseMessage {
	return &GetInfoResponseMessage{
		P2PID:             p2pID,
		MempoolSize:       mempoolSize,
		ServerVersion:     serverVersion,
		IsUtxoIndexed:     isUtxoIndexed,
		IsSynced:          isSynced,
		IsUtxoSetVerified: isUTXOSetVerified,

		CirculatingSompiSupply:     circulatingSompiSupply,
		ReferenceSompiSupply:       referenceSompiSupply,
		ReferenceSupplyDescription: referenceSupplyDescription,
	}
}
