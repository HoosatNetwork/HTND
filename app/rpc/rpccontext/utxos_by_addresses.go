package rpccontext

import (
	"encoding/hex"
	"math"
	"sync/atomic"
	"time"

	"github.com/HoosatNetwork/HTND/domain/consensus/utils/txscript"
	"github.com/HoosatNetwork/HTND/util"
	"github.com/pkg/errors"

	"github.com/HoosatNetwork/HTND/app/appmessage"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/utxoindex"
)

// virtualUTXOSource is the part of consensus this file needs: what virtual's UTXO set holds.
type virtualUTXOSource interface {
	GetVirtualUTXOEntries(outpoints []*externalapi.DomainOutpoint, maxWait time.Duration) (
		[]externalapi.UTXOEntry, []*externalapi.DomainHash, bool, error)
}

// virtualCheckMaxLockWait is how long the check waits for the consensus lock before serving the
// index's answer unchecked. Ordinary block processing releases the lock within milliseconds; what holds
// it longer is work such as a pruning point UTXO set update, which runs for minutes - longer than any
// client waits for a UTXO list.
const virtualCheckMaxLockWait = 2 * time.Second

// FilterUTXOPairsAgainstVirtual withholds index entries whose coin virtual's UTXO set does not hold,
// and returns the rest carrying virtual's own entry. It reports how many were withheld.
//
// The UTXO index is a secondary structure maintained by applying diffs, and it can disagree with the
// set consensus spends from. Both kinds of disagreement reach the spender as a broken coin. An
// outpoint the index still lists but consensus does not hold is one a wallet will build a transaction
// around, and that transaction is refused by every node, this one included, for a missing input. And
// an entry whose contents drifted - a coin's BlockDAAScore changes when a different block takes over
// merging it - misstates the coin a spender is being handed, while the stamp is part of what the
// signed transaction commits to.
//
// So consensus answers both questions here, and the index is left to do the one thing only it can:
// say which outpoints belong to an address.
//
// The check gives way when consensus is busy. If the consensus lock is not free within
// virtualCheckMaxLockWait, the pairs are returned exactly as the index gave them - what these RPCs
// served before the check existed - so a client asking during a pruning point update gets an answer
// promptly instead of a DeadlineExceeded minutes later.
//
// A withheld coin is not by itself drift. The index applies consensus's changes after consensus has
// committed them, so for a moment after a block spends a coin, or after the tip moves to a sibling and
// takes its coinbase outputs with it, the index still lists coins virtual no longer holds. drifted is
// true only when coins were withheld and indexVirtualParents - those of the last change the index
// applied, read together with pairs - equal virtual's parents during the lookup, so both described the
// same virtual state.
func FilterUTXOPairsAgainstVirtual(source virtualUTXOSource, pairs []utxoindex.UTXOPair,
	indexVirtualParents []*externalapi.DomainHash,
) (kept []utxoindex.UTXOPair, withheld int, drifted bool, err error) {
	if len(pairs) == 0 {
		return pairs, 0, false, nil
	}
	outpoints := make([]*externalapi.DomainOutpoint, len(pairs))
	for i := range pairs {
		outpoints[i] = &pairs[i].Outpoint
	}
	entries, virtualParents, checked, err := source.GetVirtualUTXOEntries(outpoints, virtualCheckMaxLockWait)
	if err != nil {
		return nil, 0, false, err
	}
	if !checked {
		logServedUnchecked(len(pairs))
		return pairs, 0, false, nil
	}
	if len(entries) != len(pairs) {
		return nil, 0, false, errors.Errorf("consensus answered for %d outpoints, %d were asked about", len(entries), len(pairs))
	}

	kept = pairs[:0]
	for i, entry := range entries {
		if entry == nil {
			withheld++
			continue
		}
		pair := pairs[i]
		pair.Entry = entry
		kept = append(kept, pair)
	}
	drifted = withheld > 0 && indexVirtualParents != nil && virtualParents != nil &&
		externalapi.HashesEqual(indexVirtualParents, virtualParents)
	return kept, withheld, drifted, nil
}

// LogWithheldUTXOs reports the coins FilterUTXOPairsAgainstVirtual withheld from what, e.g. "the
// response". Only drift is a warning; an index that had not yet caught up with virtual is routine.
func LogWithheldUTXOs(withheld int, drifted bool, address string, what string) {
	if withheld == 0 {
		return
	}
	if drifted {
		log.Warnf("Left %d UTXO(s) of address %s out of %s: the UTXO index lists them but virtual's UTXO set "+
			"does not hold them, and both describe the same virtual state. The index has drifted from consensus.",
			withheld, address, what)
		return
	}
	log.Debugf("Left %d UTXO(s) of address %s out of %s: virtual no longer holds them and the UTXO index had "+
		"not yet applied that change", withheld, address, what)
}

// lastUncheckedLog rate-limits logServedUnchecked, so a busy stretch produces one Info line per
// interval rather than one per request.
var lastUncheckedLog atomic.Int64

func logServedUnchecked(coins int) {
	const interval = 30 * time.Second
	now := time.Now().UnixNano()
	last := lastUncheckedLog.Load()
	if now-last >= int64(interval) && lastUncheckedLog.CompareAndSwap(last, now) {
		log.Infof("Consensus has held its lock for longer than %s, so UTXO RPCs are serving the UTXO "+
			"index's coins without checking them against virtual's UTXO set until it is free", virtualCheckMaxLockWait)
		return
	}
	log.Debugf("Served %d coin(s) from the UTXO index unchecked: consensus is busy", coins)
}

// ConvertUTXOOutpointEntryPairToUTXOsByAddressesEntry converts
// a UTXOOutpointEntryPair to a slice of UTXOsByAddressesEntry
func ConvertUTXOOutpointEntryPairToUTXOsByAddressesEntry(address string, script *appmessage.RPCScriptPublicKey, pair utxoindex.UTXOPair) *appmessage.UTXOsByAddressesEntry {
	// Compute scriptHex once per address (all UTXOs for this address share the same ScriptPublicKey)

	return &appmessage.UTXOsByAddressesEntry{
		Address: address,
		Outpoint: &appmessage.RPCOutpoint{
			TransactionID: pair.Outpoint.TransactionID.String(),
			Index:         pair.Outpoint.Index,
		},
		UTXOEntry: &appmessage.RPCUTXOEntry{
			Amount:          pair.Entry.Amount(),
			ScriptPublicKey: script,
			BlockDAAScore:   pair.Entry.BlockDAAScore(),
			IsCoinbase:      pair.Entry.IsCoinbase(),
		},
	}
}

func encodeHexString(buffer []byte, value []byte) ([]byte, string) {
	return encodeHexStringWithMaxValueLen(buffer, value, math.MaxInt/2)
}

func encodeHexStringWithMaxValueLen(buffer []byte, value []byte, maxValueLen int) ([]byte, string) {
	if value == nil {
		return buffer[:0], ""
	}
	if maxValueLen < 0 {
		return buffer[:0], ""
	}
	if len(value) > maxValueLen {
		return buffer[:0], ""
	}
	needed := hex.EncodedLen(len(value))
	if needed == 0 {
		return buffer[:0], ""
	}
	if cap(buffer) < needed {
		buffer = make([]byte, needed)
	} else {
		buffer = buffer[:needed]
	}
	hex.Encode(buffer, value)
	return buffer, string(buffer)
}

// ConvertAddressStringsToUTXOsChangedNotificationAddresses converts address strings
// to UTXOsChangedNotificationAddresses
func (ctx *Context) ConvertAddressStringsToUTXOsChangedNotificationAddresses(
	addressStrings []string,
) ([]*UTXOsChangedNotificationAddress, error) {
	addresses := make([]*UTXOsChangedNotificationAddress, len(addressStrings))
	var reusableHexBuffer []byte
	for i, addressString := range addressStrings {
		address, err := util.DecodeAddress(addressString, ctx.Config.ActiveNetParams.Prefix)
		if err != nil {
			return nil, errors.Errorf("Could not decode address '%s': %s", addressString, err)
		}
		scriptPublicKey, err := txscript.PayToAddrScript(address)
		if err != nil {
			return nil, errors.Errorf("Could not create a scriptPublicKey for address '%s': %s", addressString, err)
		}
		var scriptHex string
		reusableHexBuffer, scriptHex = encodeHexString(reusableHexBuffer, scriptPublicKey.Script)
		scriptPublicKeyString := utxoindex.ScriptPublicKeyString(scriptHex)
		addresses[i] = &UTXOsChangedNotificationAddress{
			Address:               addressString,
			ScriptPublicKeyString: scriptPublicKeyString,
		}
	}
	return addresses, nil
}
