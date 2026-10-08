package appmessage

// GetWalletUTXOsRequestMessage is an appmessage corresponding to
// its respective RPC message
type GetWalletUTXOsRequestMessage struct {
	baseMessage
	ExtendedPublicKeys []string
	MinimumSignatures  uint32
	ECDSA              bool
	GapLimit           uint32
	Limit              uint32
}

// Command returns the protocol command string for the message
func (msg *GetWalletUTXOsRequestMessage) Command() MessageCommand {
	return CmdGetWalletUTXOsRequestMessage
}

// NewGetWalletUTXOsRequestMessage returns a instance of the message
func NewGetWalletUTXOsRequestMessage(extendedPublicKeys []string, minimumSignatures uint32, ecdsa bool,
	gapLimit uint32, limit uint32,
) *GetWalletUTXOsRequestMessage {
	return &GetWalletUTXOsRequestMessage{
		ExtendedPublicKeys: extendedPublicKeys,
		MinimumSignatures:  minimumSignatures,
		ECDSA:              ecdsa,
		GapLimit:           gapLimit,
		Limit:              limit,
	}
}

// WalletUTXOEntry is a UTXO of a wallet address, with the path the address is derived at
type WalletUTXOEntry struct {
	Address        string
	Outpoint       *RPCOutpoint
	UTXOEntry      *RPCUTXOEntry
	DerivationPath string
}

// GetWalletUTXOsResponseMessage is an appmessage corresponding to
// its respective RPC message
type GetWalletUTXOsResponseMessage struct {
	baseMessage
	Entries                []*WalletUTXOEntry
	ScannedExternalIndexes uint32
	ScannedInternalIndexes uint32
	Truncated              bool

	Error *RPCError
}

// Command returns the protocol command string for the message
func (msg *GetWalletUTXOsResponseMessage) Command() MessageCommand {
	return CmdGetWalletUTXOsResponseMessage
}

// NewGetWalletUTXOsResponseMessage returns a instance of the message
func NewGetWalletUTXOsResponseMessage(entries []*WalletUTXOEntry, scannedExternalIndexes, scannedInternalIndexes uint32,
	truncated bool,
) *GetWalletUTXOsResponseMessage {
	return &GetWalletUTXOsResponseMessage{
		Entries:                entries,
		ScannedExternalIndexes: scannedExternalIndexes,
		ScannedInternalIndexes: scannedInternalIndexes,
		Truncated:              truncated,
	}
}
