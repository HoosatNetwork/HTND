package rpchandlers

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Hoosat-Oy/HTND/app/appmessage"
	"github.com/Hoosat-Oy/HTND/app/protocol/protocolerrors"
	"github.com/Hoosat-Oy/HTND/app/rpc/rpccontext"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/domain/consensus/ruleerrors"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/consensushashing"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/constants"
	"github.com/Hoosat-Oy/HTND/infrastructure/network/netadapter/router"
	"github.com/pkg/errors"
)

// HandleSubmitBlock processes the SubmitBlock RPC command
func HandleSubmitBlock(context *rpccontext.Context, _ *router.Router, request appmessage.Message) (appmessage.Message, error) {
	submitBlockRequest, ok := request.(*appmessage.SubmitBlockRequestMessage)
	if !ok {
		return nil, fmt.Errorf("invalid request type: expected *appmessage.SubmitBlockRequestMessage")
	}

	// Check node sync status
	if err := checkNodeSyncStatus(context); err != nil {
		return newErrorResponse(err, appmessage.RejectReasonIsInIBD), nil
	}

	// Validate block version
	if err := validateBlockVersion(context, submitBlockRequest); err != nil {
		return newErrorResponse(err, appmessage.RejectReasonBlockInvalid), nil
	}

	// Validate Proof of Work
	if err := validatePoW(context, submitBlockRequest); err != nil {
		return newErrorResponse(err, appmessage.RejectReasonBlockInvalid), nil
	}

	// Convert and validate block
	domainBlock, err := convertAndValidateBlock(submitBlockRequest)
	if err != nil {
		return newErrorResponse(err, appmessage.RejectReasonBlockInvalid), nil
	}

	// Validate DAA score if required
	if !submitBlockRequest.AllowNonDAABlocks {
		if err := validateDAAScore(context, domainBlock); err != nil {
			return newErrorResponse(err, appmessage.RejectReasonBlockInvalid), nil
		}
	}

	// Add block to consensus
	if err := context.ProtocolManager.AddBlock(domainBlock); err != nil {
		return handleBlockAddError(domainBlock, err), nil
	}

	logBlockAcceptance(domainBlock, len(submitBlockRequest.Block.Transactions))
	return appmessage.NewSubmitBlockResponseMessage(), nil
}

// validateBlockVersion checks if the block version is correct based on DAA score
func validateBlockVersion(context *rpccontext.Context, req *appmessage.SubmitBlockRequestMessage) error {
	expectedVersion := expectedBlockVersionForDAAScore(req.Block.Header.DAAScore, context.Config.ActiveNetParams.POWScores)
	if req.Block.Header.Version != expectedVersion {
		submitBlockRequestJSON, _ := json.MarshalIndent(req.Block, "", "    ")
		return fmt.Errorf("wrong block version: %s", string(submitBlockRequestJSON))
	}
	return nil
}

func expectedBlockVersionForDAAScore(daaScore uint64, powScores []uint64) uint32 {
	// Keep behavior consistent with consensus validation/building: DAA score 0 stays at version 1.
	if daaScore == 0 {
		return 1
	}
	version := uint32(1)
	for _, powScore := range powScores {
		if daaScore >= powScore {
			version++
		}
	}
	return version
}

// validatePoW checks if the Proof of Work is valid for the block
func validatePoW(context *rpccontext.Context, req *appmessage.SubmitBlockRequestMessage) error {
	if constants.GetBlockVersion() < constants.PoWIntegrityMinVersion {
		return nil
	}

	powHash := stripHexPrefix(req.PowHash)
	if powHash == "" {
		submitBlockRequestJSON, _ := json.MarshalIndent(req.Block, "", "    ")
		return fmt.Errorf("proof of work missing: %s", string(submitBlockRequestJSON))
	}
	return nil
}

// checkNodeSyncStatus verifies if the node is sufficiently synced
func checkNodeSyncStatus(context *rpccontext.Context) error {
	if context.Config.AllowSubmitBlockWhenNotSynced {
		return nil
	}

	if !context.ProtocolManager.Context().HasPeers() {
		return fmt.Errorf("node is not synced - no peers connected")
	}

	if context.ProtocolManager.Context().IsIBDRunning() {
		return fmt.Errorf("node is not synced - IBD running")
	}

	isSynced, err := context.ProtocolManager.Context().IsNearlySynced()
	if err != nil {
		return fmt.Errorf("failed to check sync status: %w", err)
	}
	if !isSynced {
		return fmt.Errorf("node is not synced")
	}
	return nil
}

// convertAndValidateBlock converts RPC block to domain block and validates it
func convertAndValidateBlock(req *appmessage.SubmitBlockRequestMessage) (*externalapi.DomainBlock, error) {
	domainBlock, err := appmessage.RPCBlockToDomainBlock(req.Block, stripHexPrefix(req.PowHash))
	if err != nil {
		return nil, fmt.Errorf("could not parse block: %w", err)
	}
	if domainBlock.PoWHash == "" {
		return nil, fmt.Errorf("invalid PoW hash")
	}
	return domainBlock, nil
}

// validateDAAScore checks if the block's DAA score is within acceptable range
func validateDAAScore(context *rpccontext.Context, block *externalapi.DomainBlock) error {
	virtualDAAScore, err := context.Domain.Consensus().GetVirtualDAAScore()
	if err != nil {
		return fmt.Errorf("failed to get virtual DAA score: %w", err)
	}

	daaWindowSize := uint64(context.Config.NetParams().DifficultyAdjustmentWindowSize[int(constants.GetBlockVersion())-1])
	if virtualDAAScore > daaWindowSize && block.Header.DAAScore() < virtualDAAScore-daaWindowSize {
		return fmt.Errorf("block DAA score %d is too far behind virtual's DAA score %d",
			block.Header.DAAScore(), virtualDAAScore)
	}
	return nil
}

// handleBlockAddError processes errors from adding block to consensus
func handleBlockAddError(block *externalapi.DomainBlock, err error) *appmessage.SubmitBlockResponseMessage {
	isProtocolOrRuleError := errors.As(err, &ruleerrors.RuleError{}) || errors.As(err, &protocolerrors.ProtocolError{})
	if !isProtocolOrRuleError {
		return newErrorResponse(fmt.Errorf("block rejected: %w", err), appmessage.RejectReasonBlockInvalid)
	}

	if errors.Is(err, ruleerrors.ErrInvalidPoW) {
		log.Warnf("Invalid PoW for block %s: %v", block.PoWHash, err)
		// Note: Consider implementing banning logic here
	} else {
		log.Warnf("Rule/protocol error for block: %v", err)
	}

	return newErrorResponse(fmt.Errorf("block rejected: %w", err), appmessage.RejectReasonBlockInvalid)
}

// newErrorResponse creates a new SubmitBlockResponseMessage with error
func newErrorResponse(err error, reason appmessage.RejectReason) *appmessage.SubmitBlockResponseMessage {
	return &appmessage.SubmitBlockResponseMessage{
		Error:        appmessage.RPCErrorf("%s", err.Error()),
		RejectReason: reason,
	}
}

// logBlockAcceptance logs successful block acceptance
func logBlockAcceptance(block *externalapi.DomainBlock, txCount int) {
	log.Infof("Accepted block %s via submit with %d tx",
		consensushashing.BlockHash(block), txCount)
	log.Debugf("Accepted PoW hash %s", block.PoWHash)
}

// stripHexPrefix removes "0x" prefix from hex string
func stripHexPrefix(hexStr string) string {
	return strings.Replace(hexStr, "0x", "", 1)
}
