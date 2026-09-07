package protowire

import (
	"github.com/HoosatNetwork/HTND/app/appmessage"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
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
	var acceptingBlockHash string
	if message.AcceptingBlockHash != nil {
		acceptingBlockHash = message.AcceptingBlockHash.String()
	} else {
		acceptingBlockHash = ""
	}
	x.GetTransactionStatusResponse = &GetTransactionStatusResponseMessage{
		Status:             toWireTransactionStatus(message.Status),
		Confirmations:      message.Confirmations,
		AcceptingBlockHash: acceptingBlockHash,
		Error:              err,
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
	acceptingBlockHash, err := externalapi.NewDomainHashFromString(x.AcceptingBlockHash)
	if err != nil {
		return nil, err
	}
	return &appmessage.GetTransactionStatusResponseMessage{
		Status:             fromWireTransactionStatus(x.Status),
		Confirmations:      x.Confirmations,
		AcceptingBlockHash: acceptingBlockHash,
		Error:              rpcErr,
	}, nil
}

// toWireTransactionStatus and fromWireTransactionStatus convert between the two TransactionStatus
// enums explicitly.
//
// They were converted by a numeric cast, which is only correct while two independently declared
// enums happen to agree - and they did not. appmessage had Invalid at 3, where the wire enum has
// ORPHAN, so everything from 3 upwards was shifted by one and clients were told an orphan had been
// accepted. Naming every pair means a value added to one enum and not the other stops compiling
// instead of quietly mis-reporting a transaction's fate.
var wireTransactionStatus = map[appmessage.TransactionStatus]TransactionStatus{
	appmessage.TransactionStatusUnknown:   TransactionStatus_TRANSACTION_STATUS_UNKNOWN,
	appmessage.TransactionStatusNotFound:  TransactionStatus_TRANSACTION_STATUS_NOT_FOUND,
	appmessage.TransactionStatusPending:   TransactionStatus_TRANSACTION_STATUS_PENDING,
	appmessage.TransactionStatusOrphan:    TransactionStatus_TRANSACTION_STATUS_ORPHAN,
	appmessage.TransactionStatusAccepted:  TransactionStatus_TRANSACTION_STATUS_ACCEPTED,
	appmessage.TransactionStatusConfirmed: TransactionStatus_TRANSACTION_STATUS_CONFIRMED,
	appmessage.TransactionStatusInvalid:   TransactionStatus_TRANSACTION_STATUS_INVALID,
}

func toWireTransactionStatus(status appmessage.TransactionStatus) TransactionStatus {
	wire, ok := wireTransactionStatus[status]
	if !ok {
		// An unmapped status is a bug, and UNKNOWN is the only honest thing to send: better a client
		// that knows the node cannot classify the transaction than one told the wrong thing about it.
		return TransactionStatus_TRANSACTION_STATUS_UNKNOWN
	}
	return wire
}

func fromWireTransactionStatus(status TransactionStatus) appmessage.TransactionStatus {
	for appStatus, wire := range wireTransactionStatus {
		if wire == status {
			return appStatus
		}
	}
	return appmessage.TransactionStatusUnknown
}
