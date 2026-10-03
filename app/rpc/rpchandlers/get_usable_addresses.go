package rpchandlers

import (
	"sync"
	"time"

	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
	"github.com/HoosatNetwork/HTND/v2/app/rpc/rpccontext"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/txscript"
	"github.com/HoosatNetwork/HTND/v2/domain/utxoindex"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter/router"
	"github.com/HoosatNetwork/HTND/v2/util"
	"github.com/HoosatNetwork/HTND/v2/util/memory"
	"github.com/pkg/errors"
)

var (
	usableAddressesCache      = make(map[string]usableAddressCacheEntry)
	usableAddressesCacheMutex sync.Mutex
)

const (
	// usableAddressesCacheTTL trades slight staleness for vastly reduced disk IO when
	// clients repeatedly query the same derived addresses (e.g. wallet sync loop).
	usableAddressesCacheTTL = 30 * time.Second
	// usableAddressesCacheMaxEntries is a safety bound to avoid unbounded memory growth
	// in case clients continuously query unique addresses.
	usableAddressesCacheMaxEntries = 200_000
)

type usableAddressCacheEntry struct {
	usable    bool
	checkedAt time.Time
}

func getUsabilityOfAddress(context *rpccontext.Context, addressString string) (bool, error) {
	address, err := util.DecodeAddress(addressString, context.Config.ActiveNetParams.Prefix)
	if err != nil {
		return false, appmessage.RPCErrorf("Couldn't decode address '%s': %s", addressString, err)
	}

	scriptPublicKey, err := txscript.PayToAddrScript(address)
	if err != nil {
		return false, appmessage.RPCErrorf("Could not create a scriptPublicKey for address '%s': %s", addressString, err)
	}
	// Cache the result for a short time to avoid repeated DB lookups.
	// NOTE: We intentionally do not cache syncing errors (ErrUTXOIndexSyncing).
	now := time.Now()
	usableAddressesCacheMutex.Lock()
	if entry, ok := usableAddressesCache[addressString]; ok {
		if now.Sub(entry.checkedAt) <= usableAddressesCacheTTL {
			usableAddressesCacheMutex.Unlock()
			return entry.usable, nil
		}
	}
	usableAddressesCacheMutex.Unlock()

	hasUTXOs, err := holdsSpendableCoin(context.UTXOIndex, context.Domain.Consensus(), scriptPublicKey, addressString)
	if err != nil {
		if errors.Is(err, utxoindex.ErrUTXOIndexSyncing) {
			return false, appmessage.RPCErrorf("UTXO index is resyncing after a pruning-point update; retry shortly")
		}
		return false, err
	}

	usableAddressesCacheMutex.Lock()
	// Simple safety bound: if the cache grows too big (e.g. scanning huge ranges), clear it.
	if len(usableAddressesCache) >= usableAddressesCacheMaxEntries {
		usableAddressesCache = make(map[string]usableAddressCacheEntry)
	}
	usableAddressesCache[addressString] = usableAddressCacheEntry{usable: hasUTXOs, checkedAt: now}
	usableAddressesCacheMutex.Unlock()

	return hasUTXOs, nil
}

// usabilityProbeLimits are the reads the usable-address check makes in turn, each one only when every
// coin the read before it returned was withheld and the address may hold more. 0 reads them all.
//
// The first reads a single coin because that is nearly always the whole answer: a withheld coin is the
// index trailing a block by a moment, so an address's first coin is almost always one consensus holds.
// The check runs for every address a wallet scans, and with 32 coins per probe it was still most of a
// busy node's CPU - 32 index decodes and 32 seeks into virtual's UTXO set per address.
var usabilityProbeLimits = []uint32{1, 32, 0}

// addressUTXOLister is the part of the UTXO index holdsSpendableCoin reads.
type addressUTXOLister interface {
	UTXOs(scriptPublicKey *externalapi.ScriptPublicKey, limit uint32, buffer *memory.Block[utxoindex.UTXOPair]) (
		[]utxoindex.UTXOPair, *memory.Block[utxoindex.UTXOPair], []*externalapi.DomainHash, error)
}

// virtualUTXOChecker is the part of consensus holdsSpendableCoin checks the index's coins against.
type virtualUTXOChecker interface {
	GetVirtualUTXOEntries(outpoints []*externalapi.DomainOutpoint, maxWait time.Duration) (
		[]externalapi.UTXOEntry, []*externalapi.DomainHash, bool, error)
}

// holdsSpendableCoin reports whether the address holds a coin that can actually be spent. The index's
// own answer - only that it has entries for this address - is checked against consensus: an address
// whose every listed coin is one consensus does not hold is not usable, since every transaction built
// on it is refused.
//
// One spendable coin settles the question, so it reads in the steps of usabilityProbeLimits.
// Reading and checking every coin an address holds dominated the CPU of a busy node: a pool or exchange
// address holds thousands, and each one costs a decode and a seek into virtual's UTXO set, all to
// answer yes. Only when every probed coin is one consensus no longer holds - the index trailing a block
// that spent them - does it read them all, so the answer is the same as checking every coin.
func holdsSpendableCoin(index addressUTXOLister, consensus virtualUTXOChecker,
	scriptPublicKey *externalapi.ScriptPublicKey, addressString string,
) (bool, error) {
	for _, limit := range usabilityProbeLimits {
		bufferSize := int(limit)
		if limit == 0 {
			bufferSize = 1000
		}
		buffer := memory.Malloc[utxoindex.UTXOPair](bufferSize)
		if buffer == nil {
			return false, appmessage.RPCErrorf("Could not allocate memory for address '%s'", addressString)
		}
		pairs, buffer, indexVirtualParents, err := index.UTXOs(scriptPublicKey, limit, buffer)
		if err != nil {
			memory.Free(buffer)
			return false, err
		}
		read := len(pairs)
		kept, withheld, drifted, err := rpccontext.FilterUTXOPairsAgainstVirtual(consensus, pairs, indexVirtualParents)
		memory.Free(buffer)
		if err != nil {
			return false, err
		}
		readAll := limit == 0 || read < int(limit)
		if len(kept) > 0 || readAll {
			rpccontext.LogWithheldUTXOs(withheld, drifted, addressString, "the usable-address check")
			return len(kept) > 0, nil
		}
		// Every probed coin was withheld and there may be more: read further. Not logged here, since
		// the next read counts these same coins again.
	}
	panic("unreachable: the full read always returns")
}

var usableAddressesPool = sync.Pool{
	New: func() any {
		slice := make([]string, 0, 2)
		return &slice
	},
}

func releaseUsableAddresses(addresses []string) {
	clear(addresses[:cap(addresses)])
	addresses = addresses[:0]
	usableAddressesPool.Put(&addresses)
}

// HandleGetUsableAddresses handles the respectively named RPC command
func HandleGetUsableAddresses(context *rpccontext.Context, _ *router.Router, request appmessage.Message) (appmessage.Message, error) {
	if !context.Config.UTXOIndex {
		errorMessage := &appmessage.GetUsableAddressesResponseMessage{}
		errorMessage.Error = appmessage.RPCErrorf("Method unavailable when htnd is run without --utxoindex")
		return errorMessage, nil
	}

	// log.Infof("-----------------------------------------------------------")
	// log.Infof("Handling GetUsableAddressesRequest")
	// log.Infof("-----------------------------------------------------------")

	getUsableAddressesRequest := request.(*appmessage.GetUsableAddressesRequestMessage)

	UsableAddresses := (*usableAddressesPool.Get().(*[]string))[:0]
	if cap(UsableAddresses) < len(getUsableAddressesRequest.Addresses) {
		UsableAddresses = make([]string, 0, len(getUsableAddressesRequest.Addresses))
	}
	defer releaseUsableAddresses(UsableAddresses)
	for _, address := range getUsableAddressesRequest.Addresses {
		usable, err := getUsabilityOfAddress(context, address)
		if err != nil {
			rpcError := &appmessage.RPCError{}
			if !errors.As(err, &rpcError) {
				return nil, err
			}
			errorMessage := &appmessage.GetUsableAddressesResponseMessage{}
			errorMessage.Error = rpcError
			return errorMessage, nil
		}
		if usable {
			UsableAddresses = append(UsableAddresses, address)
		}
	}
	// log.Infof("-----------------------------------------------------------")
	// log.Infof("Found %s usable addresses", len(UsableAddresses))
	// log.Infof("-----------------------------------------------------------")
	responseAddresses := append(make([]string, 0, len(UsableAddresses)), UsableAddresses...)
	response := appmessage.NewGetUsableAddressesResponse(responseAddresses)
	return response, nil
}
