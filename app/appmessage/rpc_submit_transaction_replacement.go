package appmessage

// SubmitTransactionReplacementRequestMessage is an appmessage corresponding to
// its respective RPC message.
type SubmitTransactionReplacementRequestMessage struct {
	baseMessage
	Transaction    *RPCTransaction
	IsHighPriority *bool
}

// Command returns the protocol command string for the message
func (msg *SubmitTransactionReplacementRequestMessage) Command() MessageCommand {
	return CmdSubmitTransactionReplacementRequestMessage
}

// NewSubmitTransactionReplacementRequestMessage returns an instance of the message.
func NewSubmitTransactionReplacementRequestMessage(transaction *RPCTransaction) *SubmitTransactionReplacementRequestMessage {
	return &SubmitTransactionReplacementRequestMessage{
		Transaction: transaction,
	}
}

// NewSubmitTransactionReplacementRequestMessageWithPriority returns an instance of the message
// with an explicit high-priority preference.
func NewSubmitTransactionReplacementRequestMessageWithPriority(transaction *RPCTransaction, isHighPriority *bool) *SubmitTransactionReplacementRequestMessage {
	return &SubmitTransactionReplacementRequestMessage{
		Transaction:    transaction,
		IsHighPriority: isHighPriority,
	}
}

// SubmitTransactionReplacementResponseMessage is an appmessage corresponding to
// its respective RPC message.
type SubmitTransactionReplacementResponseMessage struct {
	baseMessage
	TransactionID       string
	ReplacedTransaction *RPCTransaction

	Error *RPCError
}

// Command returns the protocol command string for the message
func (msg *SubmitTransactionReplacementResponseMessage) Command() MessageCommand {
	return CmdSubmitTransactionReplacementResponseMessage
}

// NewSubmitTransactionReplacementResponseMessage returns an instance of the message.
func NewSubmitTransactionReplacementResponseMessage(transactionID string) *SubmitTransactionReplacementResponseMessage {
	return &SubmitTransactionReplacementResponseMessage{
		TransactionID: transactionID,
	}
}
