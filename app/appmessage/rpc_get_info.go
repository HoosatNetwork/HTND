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

	Error *RPCError
}

// Command returns the protocol command string for the message
func (msg *GetInfoResponseMessage) Command() MessageCommand {
	return CmdGetInfoResponseMessage
}

// NewGetInfoResponseMessage returns a instance of the message
func NewGetInfoResponseMessage(p2pID string, mempoolSize uint64, serverVersion string, isUtxoIndexed bool,
	isSynced bool, isUTXOSetVerified bool,
) *GetInfoResponseMessage {
	return &GetInfoResponseMessage{
		P2PID:             p2pID,
		MempoolSize:       mempoolSize,
		ServerVersion:     serverVersion,
		IsUtxoIndexed:     isUtxoIndexed,
		IsSynced:          isSynced,
		IsUtxoSetVerified: isUTXOSetVerified,
	}
}
