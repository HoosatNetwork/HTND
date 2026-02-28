package protowire

import (
	"github.com/Hoosat-Oy/HTND/app/appmessage"
	"github.com/pkg/errors"
)

func (x *HoosatdMessage_GetUtxosByAddressesRequest) toAppMessage() (appmessage.Message, error) {
	if x == nil {
		return nil, errors.Wrapf(errorNil, "HoosatdMessage_GetUtxosByAddressesRequest is nil")
	}
	return x.GetUtxosByAddressesRequest.toAppMessage()
}

func (x *HoosatdMessage_GetUtxosByAddressesRequest) fromAppMessage(message *appmessage.GetUTXOsByAddressesRequestMessage) error {
	x.GetUtxosByAddressesRequest = &GetUtxosByAddressesRequestMessage{
		Addresses: message.Addresses,
		Limit:     &message.Limit,
	}
	return nil
}

func (x *GetUtxosByAddressesRequestMessage) toAppMessage() (appmessage.Message, error) {
	if x == nil {
		return nil, errors.Wrapf(errorNil, "GetUtxosByAddressesRequestMessage is nil")
	}
	return &appmessage.GetUTXOsByAddressesRequestMessage{
		Addresses: x.Addresses,
		Limit:     x.GetLimit(),
	}, nil
}

func (x *HoosatdMessage_GetUtxosByAddressesResponse) toAppMessage() (appmessage.Message, error) {
	if x == nil {
		return nil, errors.Wrapf(errorNil, "GetUtxosByAddressesResponseMessage is nil")
	}
	return x.GetUtxosByAddressesResponse.toAppMessage()
}

func (x *HoosatdMessage_GetUtxosByAddressesResponse) fromAppMessage(message *appmessage.GetUTXOsByAddressesResponseMessage) error {
	var err *RPCError
	if message.Error != nil {
		err = &RPCError{Message: message.Error.Message}
	}
	entries := make([]*UtxosByAddressesEntry, len(message.Entries))
	for i, entry := range message.Entries {
		entries[i] = &UtxosByAddressesEntry{}
		entries[i].fromAppMessage(entry)
	}
	x.GetUtxosByAddressesResponse = &GetUtxosByAddressesResponseMessage{
		Entries: entries,
		Error:   err,
	}
	return nil
}

func (x *GetUtxosByAddressesResponseMessage) toAppMessage() (appmessage.Message, error) {
	if x == nil {
		return nil, errors.Wrapf(errorNil, "GetUtxosByAddressesResponseMessage is nil")
	}
	rpcErr, err := x.Error.toAppMessage()
	// Error is an optional field
	if err != nil && !errors.Is(err, errorNil) {
		return nil, err
	}

	if rpcErr != nil && len(x.Entries) != 0 {
		return nil, errors.New("GetUtxosByAddressesResponseMessage contains both an error and a response")
	}

	entries := make([]*appmessage.UTXOsByAddressesEntry, len(x.Entries))
	for i, entry := range x.Entries {
		entryAsAppMessage, err := entry.toAppMessage()
		if err != nil {
			return nil, err
		}
		entries[i] = entryAsAppMessage
	}

	return &appmessage.GetUTXOsByAddressesResponseMessage{
		Entries: entries,
		Error:   rpcErr,
	}, nil
}
