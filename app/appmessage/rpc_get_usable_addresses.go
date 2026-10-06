package appmessage

// GetUsableAddressesRequestMessage is an appmessage corresponding to
// its respective RPC message
type GetUsableAddressesRequestMessage struct {
	baseMessage
	Addresses []string
}

// Command returns the protocol command string for the message
func (msg *GetUsableAddressesRequestMessage) Command() MessageCommand {
	return CmdGetUsableAddressesRequestMessage
}

// NewGetUsableAddressesRequest returns a instance of the message
func NewGetUsableAddressesRequest(addresses []string) *GetUsableAddressesRequestMessage {
	return &GetUsableAddressesRequestMessage{
		Addresses: addresses,
	}
}

// GetUsableAddressesResponseMessage is an appmessage corresponding to
// its respective RPC message
type GetUsableAddressesResponseMessage struct {
	baseMessage
	Addresses []string
	Error     *RPCError
}

// Command returns the protocol command string for the message
func (msg *GetUsableAddressesResponseMessage) Command() MessageCommand {
	return CmdGetUsableAddressesResponseMessage
}

// NewGetUsableAddressesResponse returns an instance of the message
func NewGetUsableAddressesResponse(addresses []string) *GetUsableAddressesResponseMessage {
	return &GetUsableAddressesResponseMessage{
		Addresses: addresses,
	}
}
