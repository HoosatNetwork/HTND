package appmessage

// MsgIBDBlockLocatorHighestHashNotFound represents a hoosat BlockLocatorHighestHashNotFound message
type MsgIBDBlockLocatorHighestHashNotFound struct {
	baseMessage
}

// Command returns the protocol command string for the message
func (msg *MsgIBDBlockLocatorHighestHashNotFound) Command() MessageCommand {
	return CmdIBDBlockLocatorHighestHashNotFound
}

// NewMsgIBDBlockLocatorHighestHashNotFound returns a new IBDBlockLocatorHighestHashNotFound message
func NewMsgIBDBlockLocatorHighestHashNotFound() *MsgIBDBlockLocatorHighestHashNotFound {
	return &MsgIBDBlockLocatorHighestHashNotFound{}
}
