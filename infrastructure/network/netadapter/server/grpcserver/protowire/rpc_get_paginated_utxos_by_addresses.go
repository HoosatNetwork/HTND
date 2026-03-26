package protowire

import (
	"github.com/Hoosat-Oy/HTND/app/appmessage"
	"github.com/pkg/errors"
)

func (x *HoosatdMessage_GetPaginatedUtxosByAddressesRequest) toAppMessage() (appmessage.Message, error) {
	if x == nil {
		return nil, errors.Wrapf(errorNil, "HoosatdMessage_GetPaginatedUtxosByAddressesRequest is nil")
	}
	return x.GetPaginatedUtxosByAddressesRequest.toAppMessage()
}

func (x *HoosatdMessage_GetPaginatedUtxosByAddressesRequest) fromAppMessage(message *appmessage.GetPaginatedUTXOsByAddressesRequestMessage) error {
	x.GetPaginatedUtxosByAddressesRequest = &GetPaginatedUtxosByAddressesRequestMessage{
		Addresses: message.Addresses,
		Offset:    message.Offset,
		Limit:     message.Limit,
	}
	return nil
}

func (x *GetPaginatedUtxosByAddressesRequestMessage) toAppMessage() (appmessage.Message, error) {
	if x == nil {
		return nil, errors.Wrapf(errorNil, "GetPaginatedUtxosByAddressesRequestMessage is nil")
	}
	return &appmessage.GetPaginatedUTXOsByAddressesRequestMessage{
		Addresses: x.Addresses,
		Offset:    x.GetOffset(),
		Limit:     x.GetLimit(),
	}, nil
}

func (x *HoosatdMessage_GetPaginatedUtxosByAddressesResponse) toAppMessage() (appmessage.Message, error) {
	if x == nil {
		return nil, errors.Wrapf(errorNil, "GetPaginatedUtxosByAddressesResponseMessage is nil")
	}
	return x.GetPaginatedUtxosByAddressesResponse.toAppMessage()
}

func (x *HoosatdMessage_GetPaginatedUtxosByAddressesResponse) fromAppMessage(message *appmessage.GetPaginatedUTXOsByAddressesResponseMessage) error {
	var err *RPCError
	if message.Error != nil {
		err = &RPCError{Message: message.Error.Message}
	}
	entries := make([]*UtxosByAddressesEntry, len(message.Entries))
	for i, entry := range message.Entries {
		entries[i] = &UtxosByAddressesEntry{}
		entries[i].fromAppMessage(entry)
	}
	x.GetPaginatedUtxosByAddressesResponse = &GetPaginatedUtxosByAddressesResponseMessage{
		Entries: entries,
		Error:   err,
	}
	return nil
}

func (x *GetPaginatedUtxosByAddressesResponseMessage) toAppMessage() (appmessage.Message, error) {
	if x == nil {
		return nil, errors.Wrapf(errorNil, "GetPaginatedUtxosByAddressesResponseMessage is nil")
	}
	rpcErr, err := x.Error.toAppMessage()
	// Error is an optional field
	if err != nil && !errors.Is(err, errorNil) {
		return nil, err
	}

	if rpcErr != nil && len(x.Entries) != 0 {
		return nil, errors.New("GetPaginatedUtxosByAddressesResponseMessage contains both an error and a response")
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
