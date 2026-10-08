package protowire

import (
	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
	"github.com/pkg/errors"
)

func (x *HoosatdMessage_GetWalletUtxosRequest) toAppMessage() (appmessage.Message, error) {
	if x == nil {
		return nil, errors.Wrapf(errorNil, "HoosatdMessage_GetWalletUtxosRequest is nil")
	}
	return x.GetWalletUtxosRequest.toAppMessage()
}

func (x *HoosatdMessage_GetWalletUtxosRequest) fromAppMessage(message *appmessage.GetWalletUTXOsRequestMessage) error {
	x.GetWalletUtxosRequest = &GetWalletUtxosRequestMessage{
		ExtendedPublicKeys: message.ExtendedPublicKeys,
		MinimumSignatures:  message.MinimumSignatures,
		Ecdsa:              message.ECDSA,
		GapLimit:           message.GapLimit,
		Limit:              message.Limit,
	}
	return nil
}

func (x *GetWalletUtxosRequestMessage) toAppMessage() (appmessage.Message, error) {
	if x == nil {
		return nil, errors.Wrapf(errorNil, "GetWalletUtxosRequestMessage is nil")
	}
	return &appmessage.GetWalletUTXOsRequestMessage{
		ExtendedPublicKeys: x.GetExtendedPublicKeys(),
		MinimumSignatures:  x.GetMinimumSignatures(),
		ECDSA:              x.GetEcdsa(),
		GapLimit:           x.GetGapLimit(),
		Limit:              x.GetLimit(),
	}, nil
}

func (x *HoosatdMessage_GetWalletUtxosResponse) toAppMessage() (appmessage.Message, error) {
	if x == nil {
		return nil, errors.Wrapf(errorNil, "HoosatdMessage_GetWalletUtxosResponse is nil")
	}
	return x.GetWalletUtxosResponse.toAppMessage()
}

func (x *HoosatdMessage_GetWalletUtxosResponse) fromAppMessage(message *appmessage.GetWalletUTXOsResponseMessage) error {
	var err *RPCError
	if message.Error != nil {
		err = &RPCError{Message: message.Error.Message}
	}
	entries := make([]*WalletUtxoEntry, len(message.Entries))
	for i, entry := range message.Entries {
		entries[i] = &WalletUtxoEntry{}
		entries[i].fromAppMessage(entry)
	}
	x.GetWalletUtxosResponse = &GetWalletUtxosResponseMessage{
		Entries:                entries,
		ScannedExternalIndexes: message.ScannedExternalIndexes,
		ScannedInternalIndexes: message.ScannedInternalIndexes,
		Truncated:              message.Truncated,
		Error:                  err,
	}
	return nil
}

func (x *GetWalletUtxosResponseMessage) toAppMessage() (appmessage.Message, error) {
	if x == nil {
		return nil, errors.Wrapf(errorNil, "GetWalletUtxosResponseMessage is nil")
	}
	rpcErr, err := x.Error.toAppMessage()
	// Error is an optional field
	if err != nil && !errors.Is(err, errorNil) {
		return nil, err
	}

	if rpcErr != nil && len(x.Entries) != 0 {
		return nil, errors.New("GetWalletUtxosResponseMessage contains both an error and a response")
	}

	entries := make([]*appmessage.WalletUTXOEntry, len(x.Entries))
	for i, entry := range x.Entries {
		entryAsAppMessage, err := entry.toAppMessage()
		if err != nil {
			return nil, err
		}
		entries[i] = entryAsAppMessage
	}

	return &appmessage.GetWalletUTXOsResponseMessage{
		Entries:                entries,
		ScannedExternalIndexes: x.GetScannedExternalIndexes(),
		ScannedInternalIndexes: x.GetScannedInternalIndexes(),
		Truncated:              x.GetTruncated(),
		Error:                  rpcErr,
	}, nil
}

func (x *WalletUtxoEntry) toAppMessage() (*appmessage.WalletUTXOEntry, error) {
	if x == nil {
		return nil, errors.Wrapf(errorNil, "WalletUtxoEntry is nil")
	}
	outpoint, err := x.Outpoint.toAppMessage()
	if err != nil {
		return nil, err
	}
	utxoEntry, err := x.UtxoEntry.toAppMessage()
	if err != nil {
		return nil, err
	}
	return &appmessage.WalletUTXOEntry{
		Address:        x.Address,
		Outpoint:       outpoint,
		UTXOEntry:      utxoEntry,
		DerivationPath: x.DerivationPath,
	}, nil
}

func (x *WalletUtxoEntry) fromAppMessage(message *appmessage.WalletUTXOEntry) {
	outpoint := &RpcOutpoint{}
	outpoint.fromAppMessage(message.Outpoint)
	utxoEntry := &RpcUtxoEntry{}
	utxoEntry.fromAppMessage(message.UTXOEntry)
	*x = WalletUtxoEntry{
		Address:        message.Address,
		Outpoint:       outpoint,
		UtxoEntry:      utxoEntry,
		DerivationPath: message.DerivationPath,
	}
}
