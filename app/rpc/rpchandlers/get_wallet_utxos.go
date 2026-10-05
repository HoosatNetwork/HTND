package rpchandlers

import (
	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
	"github.com/HoosatNetwork/HTND/v2/app/rpc/rpccontext"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/libhtnwallet"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/txscript"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
	"github.com/HoosatNetwork/HTND/v2/domain/utxoindex"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter/router"
	"github.com/HoosatNetwork/HTND/v2/util"
	"github.com/HoosatNetwork/HTND/v2/util/memory"
	"github.com/pkg/errors"
)

const (
	// defaultWalletGapLimit is how many addresses in a row must hold no coin before a key chain's scan
	// stops, when the request does not say. The index only knows current coins, so an address that was
	// used and emptied looks unused; wallet software that hands out a fresh change address per payment
	// leaves long runs of those, so this is well above BIP44's 20.
	defaultWalletGapLimit = 100
	// maxWalletGapLimit bounds what a request may ask for.
	maxWalletGapLimit = 1000
	// maxWalletScanIndexes caps the indexes scanned per key chain and cosigner. Each index costs a few key
	// derivations and an index lookup per address form, and the request is served on the node's RPC.
	maxWalletScanIndexes = 20_000
	// maxWalletCosigners bounds the key chains a multisig request makes the node scan.
	maxWalletCosigners = 20
)

// walletAddressLookup returns the coins of one address, and whether the UTXO index lists any - which is
// what decides that an address is in use, even when consensus no longer holds them.
type walletAddressLookup func(address util.Address) (entries []*appmessage.UTXOsByAddressesEntry, inUse bool, err error)

// walletScanResult is what scanWalletUTXOs found.
type walletScanResult struct {
	entries                []*appmessage.WalletUTXOEntry
	scannedExternalIndexes uint32
	scannedInternalIndexes uint32
	truncated              bool
}

// HandleGetWalletUTXOs handles the respectively named RPC command
func HandleGetWalletUTXOs(context *rpccontext.Context, _ *router.Router, request appmessage.Message) (appmessage.Message, error) {
	errorResponse := func(format string, args ...any) (appmessage.Message, error) {
		errorMessage := &appmessage.GetWalletUTXOsResponseMessage{}
		errorMessage.Error = appmessage.RPCErrorf(format, args...)
		return errorMessage, nil
	}
	if !context.Config.UTXOIndex {
		return errorResponse("Method unavailable when htnd is run without --utxoindex")
	}

	getWalletUTXOsRequest := request.(*appmessage.GetWalletUTXOsRequestMessage)
	limit := getWalletUTXOsRequest.Limit
	if limit == 0 || (limit > context.Config.UTXODefaultMaxLimit && context.Config.UTXODefaultMaxLimit != 0) {
		limit = context.Config.UTXODefaultMaxLimit
	}

	var reusableHexBuffer []byte
	lookup := func(address util.Address) ([]*appmessage.UTXOsByAddressesEntry, bool, error) {
		return walletAddressUTXOs(context, address, &reusableHexBuffer)
	}
	result, err := scanWalletUTXOs(context.Config.ActiveNetParams, getWalletUTXOsRequest, limit, lookup)
	if err != nil {
		var rpcError *appmessage.RPCError
		if errors.As(err, &rpcError) {
			errorMessage := &appmessage.GetWalletUTXOsResponseMessage{}
			errorMessage.Error = rpcError
			return errorMessage, nil
		}
		return nil, err
	}
	return appmessage.NewGetWalletUTXOsResponseMessage(result.entries, result.scannedExternalIndexes,
		result.scannedInternalIndexes, result.truncated), nil
}

// scanWalletUTXOs walks every key chain of the wallet the request describes - external and internal, for
// every cosigner of a multisig wallet - deriving addresses from index 0 until gapLimit of them in a row are
// unused, and collects the coins of each with the path it was derived at. It stops early, and says so,
// once limit coins are collected (0 is no limit) or a chain reaches maxWalletScanIndexes.
func scanWalletUTXOs(params *dagconfig.Params, request *appmessage.GetWalletUTXOsRequestMessage, limit uint32,
	lookup walletAddressLookup,
) (*walletScanResult, error) {
	extendedPublicKeys := request.ExtendedPublicKeys
	if len(extendedPublicKeys) == 0 {
		return nil, appmessage.RPCErrorf("At least one extended public key is required")
	}
	if len(extendedPublicKeys) > maxWalletCosigners {
		return nil, appmessage.RPCErrorf("At most %d extended public keys are supported, got %d",
			maxWalletCosigners, len(extendedPublicKeys))
	}
	minimumSignatures := request.MinimumSignatures
	if minimumSignatures == 0 {
		minimumSignatures = 1
	}
	if int(minimumSignatures) > len(extendedPublicKeys) {
		return nil, appmessage.RPCErrorf("minimumSignatures %d is more than the %d extended public keys given",
			minimumSignatures, len(extendedPublicKeys))
	}
	gapLimit := request.GapLimit
	if gapLimit == 0 {
		gapLimit = defaultWalletGapLimit
	}
	if gapLimit > maxWalletGapLimit {
		return nil, appmessage.RPCErrorf("gapLimit %d is more than the maximum of %d", gapLimit, maxWalletGapLimit)
	}

	isMultisig := len(extendedPublicKeys) > 1
	cosignerCount := uint32(1)
	if isMultisig {
		cosignerCount = uint32(len(extendedPublicKeys))
	}

	result := &walletScanResult{}
	for _, keyChain := range []uint8{libhtnwallet.ExternalKeychain, libhtnwallet.InternalKeychain} {
		for cosignerIndex := range cosignerCount {
			scanned, err := scanWalletKeyChain(params, request, isMultisig, cosignerIndex, keyChain,
				minimumSignatures, gapLimit, limit, lookup, result)
			if err != nil {
				return nil, err
			}
			if keyChain == libhtnwallet.ExternalKeychain {
				result.scannedExternalIndexes = max(result.scannedExternalIndexes, scanned)
			} else {
				result.scannedInternalIndexes = max(result.scannedInternalIndexes, scanned)
			}
			if result.truncated && limit != 0 && uint32(len(result.entries)) >= limit {
				return result, nil
			}
		}
	}
	return result, nil
}

// scanWalletKeyChain scans one key chain of one cosigner into result and returns how many indexes it
// scanned.
func scanWalletKeyChain(params *dagconfig.Params, request *appmessage.GetWalletUTXOsRequestMessage, isMultisig bool,
	cosignerIndex uint32, keyChain uint8, minimumSignatures uint32, gapLimit uint32, limit uint32,
	lookup walletAddressLookup, result *walletScanResult,
) (uint32, error) {
	unusedInARow := uint32(0)
	index := uint32(0)
	for ; unusedInARow < gapLimit; index++ {
		if index >= maxWalletScanIndexes {
			result.truncated = true
			return index, nil
		}
		path := libhtnwallet.WalletAddressPath(isMultisig, cosignerIndex, keyChain, index)
		addresses, err := libhtnwallet.WalletAddressesAtPath(params, request.ExtendedPublicKeys, minimumSignatures,
			path, request.ECDSA)
		if err != nil {
			return 0, appmessage.RPCErrorf("Could not derive the address at %s: %s", path, err)
		}

		inUseAtIndex := false
		for _, address := range addresses {
			entries, inUse, err := lookup(address)
			if err != nil {
				return 0, err
			}
			inUseAtIndex = inUseAtIndex || inUse
			for _, entry := range entries {
				if limit != 0 && uint32(len(result.entries)) >= limit {
					result.truncated = true
					return index + 1, nil
				}
				result.entries = append(result.entries, &appmessage.WalletUTXOEntry{
					Address:        entry.Address,
					Outpoint:       entry.Outpoint,
					UTXOEntry:      entry.UTXOEntry,
					DerivationPath: path,
				})
			}
		}
		if inUseAtIndex {
			unusedInARow = 0
		} else {
			unusedInARow++
		}
	}
	return index, nil
}

// walletAddressUTXOs looks one address up in the UTXO index and returns the coins of it that consensus
// holds, the same way HandleGetUTXOsByAddresses does.
func walletAddressUTXOs(context *rpccontext.Context, address util.Address, hexBuffer *[]byte) (
	[]*appmessage.UTXOsByAddressesEntry, bool, error,
) {
	addressString := address.String()
	scriptPublicKey, err := txscript.PayToAddrScript(address)
	if err != nil {
		return nil, false, appmessage.RPCErrorf("Could not create a scriptPublicKey for address '%s': %s", addressString, err)
	}

	buffer := memory.Malloc[utxoindex.UTXOPair](1000)
	if buffer == nil {
		return nil, false, appmessage.RPCErrorf("Could not allocate memory for address '%s'", addressString)
	}
	pairs, buffer, indexVirtualParents, err := context.UTXOIndex.UTXOs(scriptPublicKey, 0, buffer)
	defer func() { memory.Free(buffer) }()
	if err != nil {
		if errors.Is(err, utxoindex.ErrUTXOIndexSyncing) {
			return nil, false, appmessage.RPCErrorf("UTXO index is resyncing after a pruning-point update; retry shortly")
		}
		return nil, false, err
	}
	inUse := len(pairs) > 0
	if !inUse {
		return nil, false, nil
	}

	pairs, withheld, drifted, err := rpccontext.FilterUTXOPairsAgainstVirtual(context.Domain.Consensus(), pairs, indexVirtualParents)
	if err != nil {
		return nil, false, err
	}
	rpccontext.LogWithheldUTXOs(withheld, drifted, addressString, "the wallet UTXOs response")

	var scriptHex string
	*hexBuffer, scriptHex = encodeHexString(*hexBuffer, scriptPublicKey.Script)
	sharedScript := &appmessage.RPCScriptPublicKey{Script: scriptHex, Version: scriptPublicKey.Version}
	entries := make([]*appmessage.UTXOsByAddressesEntry, 0, len(pairs))
	for _, pair := range pairs {
		if entry := rpccontext.ConvertUTXOOutpointEntryPairToUTXOsByAddressesEntry(addressString, sharedScript, pair); entry != nil {
			entries = append(entries, entry)
		}
	}
	return entries, true, nil
}
