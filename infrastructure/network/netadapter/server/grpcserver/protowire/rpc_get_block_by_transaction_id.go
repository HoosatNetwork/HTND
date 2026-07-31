package protowire

import (
	"github.com/HoosatNetwork/HTND/app/appmessage"
	"github.com/pkg/errors"
)

func (x *HoosatdMessage_GetBlockByTransactionIdRequest) toAppMessage() (appmessage.Message, error) {
	if x == nil {
		return nil, errors.Wrapf(errorNil, "HoosatdMessage_GetBlockByTransactionIdRequest is nil")
	}
	return x.GetBlockByTransactionIdRequest.toAppMessage()
}

func (x *HoosatdMessage_GetBlockByTransactionIdRequest) fromAppMessage(message *appmessage.GetBlockByTransactionIDRequestMessage) error {
	x.GetBlockByTransactionIdRequest = &GetBlockByTransactionIDRequestMessage{
		TransactionId:       message.TransactionID,
		IncludeTransactions: message.IncludeTransactions,
	}
	return nil
}

func (x *GetBlockByTransactionIDRequestMessage) toAppMessage() (appmessage.Message, error) {
	if x == nil {
		return nil, errors.Wrapf(errorNil, "GetBlockByTransactionIDRequestMessage is nil")
	}
	return &appmessage.GetBlockByTransactionIDRequestMessage{
		TransactionID:       x.TransactionId,
		IncludeTransactions: x.IncludeTransactions,
	}, nil
}

func (x *HoosatdMessage_GetBlockByTransactionIdResponse) toAppMessage() (appmessage.Message, error) {
	if x == nil {
		return nil, errors.Wrapf(errorNil, "HoosatdMessage_GetBlockByTransactionIdResponse is nil")
	}
	return x.GetBlockByTransactionIdResponse.toAppMessage()
}

func (x *HoosatdMessage_GetBlockByTransactionIdResponse) fromAppMessage(message *appmessage.GetBlockByTransactionIDResponseMessage) error {
	var err *RPCError
	if message.Error != nil {
		err = &RPCError{Message: message.Error.Message}
	}
	var block *RpcBlock
	if message.Block != nil {
		protoBlock := &RpcBlock{}
		err := protoBlock.fromAppMessage(message.Block)
		if err != nil {
			return err
		}
		block = protoBlock
	}
	x.GetBlockByTransactionIdResponse = &GetBlockByTransactionIDResponseMessage{
		Block: block,
		Error: err,
	}
	return nil
}

func (x *GetBlockByTransactionIDResponseMessage) toAppMessage() (appmessage.Message, error) {
	if x == nil {
		return nil, errors.Wrapf(errorNil, "GetBlockByTransactionIDResponseMessage is nil")
	}
	rpcErr := &appmessage.RPCError{Message: ""}
	if x.Error != nil {
		rpcErr.Message = x.Error.Message
	}
	var block *appmessage.RPCBlock
	if x.Block != nil {
		var err error
		block, err = x.Block.toAppMessage()
		if err != nil {
			return nil, err
		}
	}
	return &appmessage.GetBlockByTransactionIDResponseMessage{
		Block: block,
		Error: rpcErr,
	}, nil
}
