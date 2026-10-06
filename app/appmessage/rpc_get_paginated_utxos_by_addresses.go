package appmessage

// GetUTXOsByAddressesRequestMessage is an appmessage corresponding to
// its respective RPC message
type GetPaginatedUTXOsByAddressesRequestMessage struct {
	baseMessage
	Addresses []string
	Offset    uint32
	Limit     uint32
}

// Command returns the protocol command string for the message
func (msg *GetPaginatedUTXOsByAddressesRequestMessage) Command() MessageCommand {
	return CmdGetPaginatedUTXOsByAddressesRequestMessage
}

// NewGetPaginatedUTXOsByAddressesRequestMessage returns a instance of the message
func NewGetPaginatedUTXOsByAddressesRequestMessage(addresses []string, offset, limit uint32) *GetPaginatedUTXOsByAddressesRequestMessage {
	return &GetPaginatedUTXOsByAddressesRequestMessage{
		Addresses: addresses,
		Offset:    offset,
		Limit:     limit,
	}
}

// GetPaginatedUTXOsByAddressesResponseMessage is an appmessage corresponding to
// its respective RPC message
type GetPaginatedUTXOsByAddressesResponseMessage struct {
	baseMessage
	Entries []*UTXOsByAddressesEntry

	Error *RPCError
}

// Command returns the protocol command string for the message
func (msg *GetPaginatedUTXOsByAddressesResponseMessage) Command() MessageCommand {
	return CmdGetPaginatedUTXOsByAddressesResponseMessage
}

// NewGetPaginatedUTXOsByAddressesResponseMessage returns a instance of the message
func NewGetPaginatedUTXOsByAddressesResponseMessage(entries []*UTXOsByAddressesEntry) *GetPaginatedUTXOsByAddressesResponseMessage {
	return &GetPaginatedUTXOsByAddressesResponseMessage{
		Entries: entries,
	}
}
