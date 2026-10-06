package appmessage

import (
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
)

// MaxRequestIBDBlocksHashes is the maximum number of hashes that can be in a single
// RequestIBDBlocks message. Unlike relay's inv-driven MaxRequestRelayBlocksHashes, every legitimate
// IBD block request is bounded by the syncee's own batch loop (getIBDBatchSize() in
// app/protocol/flows/v8/blockrelay, currently 99*5 = 495 - both the normal per-chunk request and the
// missing-hash retry stay within it), so this can be capped much lower: 4x that batch size,
// generously covering retries without letting a peer ask a syncer to serve the whole stored DAG in
// one message. Hardcoded rather than importing blockrelay's constant to avoid a layering cycle
// (blockrelay already imports appmessage); keep it a small multiple of getIBDBatchSize() if that
// ever changes.
const MaxRequestIBDBlocksHashes = 4 * 495

// MsgRequestIBDBlocks implements the Message interface and represents a hoosat
// RequestIBDBlocks message. It is used to request blocks as part of the IBD
// protocol.
type MsgRequestIBDBlocks struct {
	baseMessage
	Hashes []*externalapi.DomainHash
}

// Command returns the protocol command string for the message. This is part
// of the Message interface implementation.
func (msg *MsgRequestIBDBlocks) Command() MessageCommand {
	return CmdRequestIBDBlocks
}

// NewMsgRequestIBDBlocks returns a new MsgRequestIBDBlocks.
func NewMsgRequestIBDBlocks(hashes []*externalapi.DomainHash) *MsgRequestIBDBlocks {
	return &MsgRequestIBDBlocks{
		Hashes: hashes,
	}
}
