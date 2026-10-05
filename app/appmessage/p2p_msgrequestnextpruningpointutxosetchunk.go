package appmessage

// MsgRequestNextPruningPointUTXOSetChunk represents a hoosat RequestNextPruningPointUTXOSetChunk message
type MsgRequestNextPruningPointUTXOSetChunk struct {
	baseMessage
}

// Command returns the protocol command string for the message
func (msg *MsgRequestNextPruningPointUTXOSetChunk) Command() MessageCommand {
	return CmdRequestNextPruningPointUTXOSetChunk
}

// NewMsgRequestNextPruningPointUTXOSetChunk returns a new MsgRequestNextPruningPointUTXOSetChunk.
func NewMsgRequestNextPruningPointUTXOSetChunk() *MsgRequestNextPruningPointUTXOSetChunk {
	return &MsgRequestNextPruningPointUTXOSetChunk{}
}
