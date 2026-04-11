package protowire

import (
	"github.com/Hoosat-Oy/HTND/app/appmessage"
	"github.com/pkg/errors"
)

func (x *HoosatdMessage_SubmitTransactionReplacementRequest) toAppMessage() (appmessage.Message, error) {
	if x == nil {
		return nil, errors.Wrapf(errorNil, "HoosatdMessage_SubmitTransactionReplacementRequest is nil")
	}
	return x.SubmitTransactionReplacementRequest.toAppMessage()
}

func (x *HoosatdMessage_SubmitTransactionReplacementRequest) fromAppMessage(message *appmessage.SubmitTransactionReplacementRequestMessage) error {
	x.SubmitTransactionReplacementRequest = &SubmitTransactionReplacementRequestMessage{
		Transaction:    &RpcTransaction{},
		IsHighPriority: message.IsHighPriority,
	}
	if message.Transaction != nil {
		x.SubmitTransactionReplacementRequest.Transaction.fromAppMessage(message.Transaction)
	}
	return nil
}

func (x *SubmitTransactionReplacementRequestMessage) toAppMessage() (appmessage.Message, error) {
	if x == nil {
		return nil, errors.Wrapf(errorNil, "SubmitTransactionReplacementRequestMessage is nil")
	}
	rpcTransaction, err := x.Transaction.toAppMessage()
	if err != nil {
		return nil, err
	}
	return &appmessage.SubmitTransactionReplacementRequestMessage{
		Transaction:    rpcTransaction,
		IsHighPriority: x.IsHighPriority,
	}, nil
}

func (x *HoosatdMessage_SubmitTransactionReplacementResponse) toAppMessage() (appmessage.Message, error) {
	if x == nil {
		return nil, errors.Wrapf(errorNil, "HoosatdMessage_SubmitTransactionReplacementResponse is nil")
	}
	return x.SubmitTransactionReplacementResponse.toAppMessage()
}

func (x *HoosatdMessage_SubmitTransactionReplacementResponse) fromAppMessage(message *appmessage.SubmitTransactionReplacementResponseMessage) error {
	var err *RPCError
	if message.Error != nil {
		err = &RPCError{Message: message.Error.Message}
	}
	x.SubmitTransactionReplacementResponse = &SubmitTransactionReplacementResponseMessage{
		TransactionId: message.TransactionID,
		Error:         err,
	}
	if message.ReplacedTransaction != nil {
		x.SubmitTransactionReplacementResponse.ReplacedTransaction = &RpcTransaction{}
		x.SubmitTransactionReplacementResponse.ReplacedTransaction.fromAppMessage(message.ReplacedTransaction)
	}
	return nil
}

func (x *SubmitTransactionReplacementResponseMessage) toAppMessage() (appmessage.Message, error) {
	if x == nil {
		return nil, errors.Wrapf(errorNil, "SubmitTransactionReplacementResponseMessage is nil")
	}

	if x.Error != nil {
		rpcErr, err := x.Error.toAppMessage()
		// Error is an optional field
		if err != nil && !errors.Is(err, errorNil) {
			return nil, err
		}
		return &appmessage.SubmitTransactionReplacementResponseMessage{
			TransactionID: x.TransactionId,
			Error:         rpcErr,
		}, nil
	}

	replacedTx, err := x.ReplacedTransaction.toAppMessage()
	if err != nil && !errors.Is(err, errorNil) {
		return nil, err
	}
	return &appmessage.SubmitTransactionReplacementResponseMessage{
		TransactionID:       x.TransactionId,
		ReplacedTransaction: replacedTx,
	}, nil
}
