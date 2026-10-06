package appmessage

// GetBlockByTransactionIDRequestMessage is an appmessage corresponding to
// its respective RPC message
type GetBlockByTransactionIDRequestMessage struct {
	baseMessage
	TransactionID       string
	IncludeTransactions bool
}

// Command returns the protocol command string for the message
func (msg *GetBlockByTransactionIDRequestMessage) Command() MessageCommand {
	return CmdGetBlockByTransactionIDRequestMessage
}

// NewGetBlockByTransactionIDRequestMessage returns a instance of the message
func NewGetBlockByTransactionIDRequestMessage(transactionID string, includeTransactions bool) *GetBlockByTransactionIDRequestMessage {
	return &GetBlockByTransactionIDRequestMessage{
		TransactionID:       transactionID,
		IncludeTransactions: includeTransactions,
	}
}

// GetBlockByTransactionIDResponseMessage is an appmessage corresponding to
// its respective RPC message
type GetBlockByTransactionIDResponseMessage struct {
	baseMessage
	Block *RPCBlock

	Error *RPCError
}

// Command returns the protocol command string for the message
func (msg *GetBlockByTransactionIDResponseMessage) Command() MessageCommand {
	return CmdGetBlockByTransactionIDResponseMessage
}

// NewGetBlockByTransactionIDResponseMessage returns a instance of the message
func NewGetBlockByTransactionIDResponseMessage() *GetBlockByTransactionIDResponseMessage {
	return &GetBlockByTransactionIDResponseMessage{}
}
