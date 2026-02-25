package protowire

import (
	"github.com/Hoosat-Oy/HTND/app/appmessage"
	"github.com/pkg/errors"
)

func (x *HoosatdMessage_GetUsableAddressesRequest) toAppMessage() (appmessage.Message, error) {
	if x == nil {
		return nil, errors.Wrapf(errorNil, "HoosatdMessage_GetBalanceByAddressRequest is nil")
	}
	return x.GetUsableAddressesRequest.toAppMessage()
}

func (x *HoosatdMessage_GetUsableAddressesRequest) fromAppMessage(message *appmessage.GetUsableAddressesRequestMessage) error {
	x.GetUsableAddressesRequest = &GetUsableAddressesRequestMessage{
		Addresses: message.Addresses,
	}
	return nil
}

func (x *GetUsableAddressesRequestMessage) toAppMessage() (appmessage.Message, error) {
	if x == nil {
		return nil, errors.Wrapf(errorNil, "GetBalanceByAddressRequest is nil")
	}
	return &appmessage.GetUsableAddressesRequestMessage{
		Addresses: x.Addresses,
	}, nil
}

func (x *HoosatdMessage_GetUsableAddressesResponse) toAppMessage() (appmessage.Message, error) {
	if x == nil {
		return nil, errors.Wrapf(errorNil, "GetBalanceByAddressResponse is nil")
	}
	return x.GetUsableAddressesResponse.toAppMessage()
}

func (x *HoosatdMessage_GetUsableAddressesResponse) fromAppMessage(message *appmessage.GetUsableAddressesResponseMessage) error {
	var err *RPCError
	if message.Error != nil {
		err = &RPCError{Message: message.Error.Message}
	}
	x.GetUsableAddressesResponse = &GetUsableAddressesResponseMessage{
		Addresses: message.Addresses,
		Error:     err,
	}
	return nil
}

func (x *GetUsableAddressesResponseMessage) toAppMessage() (appmessage.Message, error) {
	if x == nil {
		return nil, errors.Wrapf(errorNil, "GetUsableAddressesResponseMessage is nil")
	}
	rpcErr, err := x.Error.toAppMessage()
	// Error is an optional field
	if err != nil && !errors.Is(err, errorNil) {
		return nil, err
	}

	return &appmessage.GetUsableAddressesResponseMessage{
		Addresses: x.Addresses,
		Error:     rpcErr,
	}, nil
}
