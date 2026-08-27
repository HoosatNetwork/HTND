package coinbasemanager

import (
	"encoding/binary"
	"strconv"

	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/ruleerrors"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/hashes"
	"github.com/pkg/errors"
)

// coinbaseEntropyActivationVersion is the block version, activated by a hard fork,
// from which per-block entropy is folded into the coinbase payload. Blocks below
// this version keep the pre-fork payload layout so already-mined blocks continue
// to validate unchanged.
const coinbaseEntropyActivationVersion = 8

// CoinbaseTimestampEntropyActivationVersion is the block version, activated by a second hard
// fork, from which the block's own header timestamp is additionally folded into coinbase
// entropy. coinbaseEntropyActivationVersion's own entropy (merge set + DAA score) is a pure
// function of a block's PARENT SET, not of the individual block itself, so it fails at exactly
// the case its doc comment names as the target: two sibling blocks built on the same tip compute
// byte-identical entropy under it, and therefore still produce colliding coinbase transaction IDs
// whenever they also share a miner script (always true for a solo miner or a fixed-address pool).
// Timestamp is chosen by the miner before the coinbase is built (see blockBuilder.buildBlock,
// which now picks it up front and reuses the same value for both the coinbase and the header), is
// independently recomputable by any validator from the block's own already-known stored header,
// and in practice differs between any two blocks even if built moments apart on the same parents.
const CoinbaseTimestampEntropyActivationVersion = 9

// coinbaseEntropy derives per-block entropy from the block's own full merge set
// (blues and reds, in their GHOSTDAG-canonical order, which starts with the
// selected parent) and DAA score. It's folded into the coinbase payload so that
// two blocks whose blue score, merge set and miner-supplied data would otherwise
// coincide (e.g. sibling blocks mined on the same parents) don't produce
// colliding coinbase transaction IDs. Every input is already-established
// consensus state, known identically at both block-build time and validation
// time, so this stays fully deterministic and verifiable.
//
// From CoinbaseTimestampEntropyActivationVersion onward, the block's own header timestamp
// (milliseconds) is additionally folded in - see that constant's doc comment for why the earlier
// inputs alone aren't sufficient. timestamp must be blockVersion's own block's timestamp: the
// candidate the builder is about to commit to its header when constructing, or the already-mined
// block's stored header value when validating (see coinbaseManager.blockTimestamp).
//
// Only the first 8 bytes of the underlying hash are kept: that's already far more
// collision resistance than this non-adversarial uniqueness check needs, and
// keeping it small avoids also having to raise the network-wide
// MaxCoinbasePayloadLength.
func coinbaseEntropy(ghostdagData *externalapi.BlockGHOSTDAGData, daaScore uint64, blockVersion uint16, timestamp int64) [lengthOfEntropy]byte {
	writer := hashes.NewCoinbaseEntropyHashWriter()
	for _, blockHash := range ghostdagData.MergeSetBlues() {
		writer.InfallibleWrite(blockHash.ByteSlice())
	}
	for _, blockHash := range ghostdagData.MergeSetReds() {
		writer.InfallibleWrite(blockHash.ByteSlice())
	}
	var daaScoreBytes [uint64Len]byte
	binary.LittleEndian.PutUint64(daaScoreBytes[:], daaScore)
	writer.InfallibleWrite(daaScoreBytes[:])

	if blockVersion >= CoinbaseTimestampEntropyActivationVersion {
		var timestampBytes [uint64Len]byte
		binary.LittleEndian.PutUint64(timestampBytes[:], uint64(timestamp))
		writer.InfallibleWrite(timestampBytes[:])
	}

	var entropy [lengthOfEntropy]byte
	copy(entropy[:], writer.Finalize().ByteSlice())
	return entropy
}

func scriptLengthByte(length int) byte {
	parsedLength, err := strconv.ParseUint(strconv.Itoa(length), 10, 8)
	if err != nil {
		panic(err)
	}
	var lengthBytes [8]byte
	binary.BigEndian.PutUint64(lengthBytes[:], parsedLength)
	return lengthBytes[7]
}

const (
	uint64Len                   = 8
	uint16Len                   = 2
	lengthOfSubsidy             = uint64Len
	lengthOfEntropy             = uint64Len
	lengthOfScriptPubKeyLength  = 1
	lengthOfVersionScriptPubKey = uint16Len
)

// prefixLengthForVersion returns the length of the fixed, consensus-derived
// payload prefix (blue score + subsidy, plus entropy from
// coinbaseEntropyActivationVersion onward) for the given block version.
func prefixLengthForVersion(blockVersion uint16) int {
	if blockVersion >= coinbaseEntropyActivationVersion {
		return uint64Len + lengthOfSubsidy + lengthOfEntropy
	}
	return uint64Len + lengthOfSubsidy
}

// serializeCoinbasePayload builds the coinbase payload based on the provided scriptPubKey and extra
// data. blockVersion must be the coinbase-owning block's own header version - see
// ExtractCoinbaseDataBlueScoreAndSubsidyForVersion's doc comment for why the ambient
// constants.GetBlockVersion() can't be assumed here either.
func (c *coinbaseManager) serializeCoinbasePayload(blueScore uint64,
	coinbaseData *externalapi.DomainCoinbaseData, subsidy uint64, entropy [lengthOfEntropy]byte, blockVersion uint16,
) ([]byte, error) {
	scriptLengthOfScriptPubKey := len(coinbaseData.ScriptPublicKey.Script)
	if scriptLengthOfScriptPubKey > int(c.coinbasePayloadScriptPublicKeyMaxLength) {
		return nil, errors.Wrapf(ruleerrors.ErrBadCoinbasePayloadLen, "coinbase's payload script public key is "+
			"longer than the max allowed length of %d", c.coinbasePayloadScriptPublicKeyMaxLength)
	}
	scriptLengthOfScriptPubKeyByte := scriptLengthByte(scriptLengthOfScriptPubKey)

	prefixLength := prefixLengthForVersion(blockVersion)
	payload := make([]byte, prefixLength+lengthOfVersionScriptPubKey+lengthOfScriptPubKeyLength+scriptLengthOfScriptPubKey+len(coinbaseData.ExtraData))
	binary.LittleEndian.PutUint64(payload[:uint64Len], blueScore)
	binary.LittleEndian.PutUint64(payload[uint64Len:uint64Len+lengthOfSubsidy], subsidy)
	if prefixLength > uint64Len+lengthOfSubsidy {
		copy(payload[uint64Len+lengthOfSubsidy:prefixLength], entropy[:])
	}

	binary.LittleEndian.PutUint16(payload[prefixLength:], coinbaseData.ScriptPublicKey.Version)
	payload[prefixLength+lengthOfVersionScriptPubKey] = scriptLengthOfScriptPubKeyByte
	copy(payload[prefixLength+lengthOfVersionScriptPubKey+lengthOfScriptPubKeyLength:], coinbaseData.ScriptPublicKey.Script)
	copy(payload[prefixLength+lengthOfVersionScriptPubKey+lengthOfScriptPubKeyLength+scriptLengthOfScriptPubKey:], coinbaseData.ExtraData)

	return payload, nil
}

// ModifyCoinbasePayload modifies the coinbase payload based on the provided scriptPubKey and extra
// data. blockVersion must be the coinbase-owning block's own header version - not the ambient
// constants.GetBlockVersion(), which can have advanced past this specific template's version by
// the time it's modified (e.g. a held template being re-customized after the node crossed a
// version threshold), producing the wrong prefix length for this payload's actual layout and
// corrupting the script-pubkey/extra-data fields being copied in.
func ModifyCoinbasePayload(payload []byte, coinbaseData *externalapi.DomainCoinbaseData, coinbasePayloadScriptPublicKeyMaxLength uint8, blockVersion uint16) ([]byte, error) {
	scriptLengthOfScriptPubKey := len(coinbaseData.ScriptPublicKey.Script)
	if scriptLengthOfScriptPubKey > int(coinbasePayloadScriptPublicKeyMaxLength) {
		return nil, errors.Wrapf(ruleerrors.ErrBadCoinbasePayloadLen, "coinbase's payload script public key is "+
			"longer than the max allowed length of %d", coinbasePayloadScriptPublicKeyMaxLength)
	}
	scriptLengthOfScriptPubKeyByte := scriptLengthByte(scriptLengthOfScriptPubKey)

	prefixLength := prefixLengthForVersion(blockVersion)
	newPayloadLen := prefixLength + lengthOfVersionScriptPubKey + lengthOfScriptPubKeyLength + scriptLengthOfScriptPubKey + len(coinbaseData.ExtraData)
	if len(payload) != newPayloadLen {
		newPayload := make([]byte, newPayloadLen)
		copyLength := prefixLength
		if len(payload) < copyLength {
			copyLength = len(payload)
		}
		copy(newPayload, payload[:copyLength])
		payload = newPayload
	}

	binary.LittleEndian.PutUint16(payload[prefixLength:prefixLength+lengthOfVersionScriptPubKey], coinbaseData.ScriptPublicKey.Version)
	payload[prefixLength+lengthOfVersionScriptPubKey] = scriptLengthOfScriptPubKeyByte
	copy(payload[prefixLength+lengthOfVersionScriptPubKey+lengthOfScriptPubKeyLength:], coinbaseData.ScriptPublicKey.Script)
	copy(payload[prefixLength+lengthOfVersionScriptPubKey+lengthOfScriptPubKeyLength+scriptLengthOfScriptPubKey:], coinbaseData.ExtraData)

	return payload, nil
}

// ExtractCoinbaseDataBlueScoreAndSubsidyForVersion deserializes the coinbase payload to its
// component (scriptPubKey, extra data, and subsidy). blockVersion must be the coinbase-owning
// block's own header version - not necessarily the ambient constants.GetBlockVersion(), which
// tracks whichever block is currently being built/relayed and can be stale or ahead when parsing
// a coinbase belonging to a different block (e.g. a merge-set block's coinbase looked up by hash,
// or a historical block validated in batch during IBD with trusted data). Blocks whose merge set
// spans the entropy hard fork boundary can mix pre- and post-fork coinbase payloads, so this must
// always be threaded through explicitly rather than assumed.
func (c *coinbaseManager) ExtractCoinbaseDataBlueScoreAndSubsidyForVersion(coinbaseTx *externalapi.DomainTransaction, blockVersion uint16) (
	blueScore uint64, coinbaseData *externalapi.DomainCoinbaseData, subsidy uint64, err error,
) {
	prefixLength := prefixLengthForVersion(blockVersion)
	minLength := prefixLength + lengthOfVersionScriptPubKey + lengthOfScriptPubKeyLength
	if len(coinbaseTx.Payload) < minLength {
		return 0, nil, 0, errors.Wrapf(ruleerrors.ErrBadCoinbasePayloadLen,
			"coinbase payload is less than the minimum length of %d", minLength)
	}

	blueScore = binary.LittleEndian.Uint64(coinbaseTx.Payload[:uint64Len])
	subsidy = binary.LittleEndian.Uint64(coinbaseTx.Payload[uint64Len : uint64Len+lengthOfSubsidy])

	scriptPubKeyVersion := binary.LittleEndian.Uint16(coinbaseTx.Payload[prefixLength : prefixLength+uint16Len])

	scriptPubKeyScriptLength := coinbaseTx.Payload[prefixLength+lengthOfVersionScriptPubKey]

	if scriptPubKeyScriptLength > c.coinbasePayloadScriptPublicKeyMaxLength {
		return 0, nil, 0, errors.Wrapf(ruleerrors.ErrBadCoinbasePayloadLen, "coinbase's payload script public key is "+
			"longer than the max allowed length of %d", c.coinbasePayloadScriptPublicKeyMaxLength)
	}

	if len(coinbaseTx.Payload) < minLength+int(scriptPubKeyScriptLength) {
		return 0, nil, 0, errors.Wrapf(ruleerrors.ErrBadCoinbasePayloadLen,
			"coinbase payload doesn't have enough bytes to contain a script public key of %d bytes", scriptPubKeyScriptLength)
	}
	scriptEnd := prefixLength + lengthOfVersionScriptPubKey + lengthOfScriptPubKeyLength + int(scriptPubKeyScriptLength)
	scriptPubKeyScript := coinbaseTx.Payload[prefixLength+lengthOfVersionScriptPubKey+lengthOfScriptPubKeyLength : scriptEnd]

	return blueScore, &externalapi.DomainCoinbaseData{
		ScriptPublicKey: &externalapi.ScriptPublicKey{Script: scriptPubKeyScript, Version: scriptPubKeyVersion},
		ExtraData:       coinbaseTx.Payload[scriptEnd:],
	}, subsidy, nil
}
