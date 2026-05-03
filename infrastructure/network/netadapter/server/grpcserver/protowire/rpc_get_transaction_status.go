package protowire

import (
	"github.com/Hoosat-Oy/HTND/app/appmessage"
	"github.com/pkg/errors"
)

func (x *HoosatdMessage_GetTransactionStatusRequest) toAppMessage() (appmessage.Message, error) {
	if x == nil {
		return nil, errors.Wrapf(errorNil, "HoosatdMessage_GetTransactionStatusRequest is nil")
	}
	return x.GetTransactionStatusRequest.toAppMessage()
}

func (x *HoosatdMessage_GetTransactionStatusRequest) fromAppMessage(message *appmessage.GetTransactionStatusRequestMessage) error {
	x.GetTransactionStatusRequest = &GetTransactionStatusRequestMessage{TransactionId: message.TransactionID}
	return nil
}

func (x *GetTransactionStatusRequestMessage) toAppMessage() (appmessage.Message, error) {
	if x == nil {
		return nil, errors.Wrapf(errorNil, "GetTransactionStatusRequestMessage is nil")
	}
	return &appmessage.GetTransactionStatusRequestMessage{TransactionID: x.TransactionId}, nil
}

func (x *HoosatdMessage_GetTransactionStatusResponse) toAppMessage() (appmessage.Message, error) {
	if x == nil {
		return nil, errors.Wrapf(errorNil, "HoosatdMessage_GetTransactionStatusResponse is nil")
	}
	return x.GetTransactionStatusResponse.toAppMessage()
}

func (x *HoosatdMessage_GetTransactionStatusResponse) fromAppMessage(message *appmessage.GetTransactionStatusResponseMessage) error {
	var err *RPCError
	if message.Error != nil {
		err = &RPCError{Message: message.Error.Message}
	}
	x.GetTransactionStatusResponse = &GetTransactionStatusResponseMessage{
		Status:        TransactionStatus(message.Status),
		Confirmations: message.Confirmations,
		Error:         err,
	}
	return nil
}

func (x *GetTransactionStatusResponseMessage) toAppMessage() (appmessage.Message, error) {
	if x == nil {
		return nil, errors.Wrapf(errorNil, "GetTransactionStatusResponseMessage is nil")
	}
	rpcErr, err := x.Error.toAppMessage()
	if err != nil && !errors.Is(err, errorNil) {
		return nil, err
	}
	return &appmessage.GetTransactionStatusResponseMessage{
		Status:        appmessage.TransactionStatus(x.Status),
		Confirmations: x.Confirmations,
		Error:         rpcErr,
	}, nil
}
