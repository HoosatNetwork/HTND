package rpchandlers

import (
	"time"

	"github.com/Hoosat-Oy/HTND/app/appmessage"
	"github.com/Hoosat-Oy/HTND/app/rpc/rpccontext"
	"github.com/Hoosat-Oy/HTND/domain/consensus/database"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/infrastructure/network/netadapter/router"
	"github.com/pkg/errors"
)

// getBlockWithRetry attempts to get a block with retry logic for transient failures
func GetBlockEvenIfHeaderOnlyWithRetry(context *rpccontext.Context, hash *externalapi.DomainHash, maxAttempts int) (*externalapi.DomainBlock, error) {
	backoffDurations := []time.Duration{100 * time.Millisecond, 200 * time.Millisecond, 300 * time.Millisecond, 400 * time.Millisecond, 500 * time.Millisecond}

	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		block, err := context.Domain.Consensus().GetBlockEvenIfHeaderOnly(hash)
		if err == nil {
			if attempt > 0 {
				log.Infof("Successfully retrieved block %s after %d retry attempts", hash, attempt)
			}
			return block, nil
		}

		lastErr = err

		// Don't retry on certain types of errors
		if !shouldRetryError(err) {
			log.Debugf("Non-retryable error for block %s: %s", hash, err)
			return nil, err
		}

		// If this isn't the last attempt, wait before retrying
		if attempt < maxAttempts-1 {
			backoffDuration := backoffDurations[attempt]
			log.Warnf("Failed to get block %s on attempt %d/%d: %s. Retrying in %v",
				hash, attempt+1, maxAttempts, err, backoffDuration)
			time.Sleep(backoffDuration)
		}
	}

	log.Errorf("Failed to get block %s after %d attempts. Last error: %s", hash, maxAttempts, lastErr)
	return nil, lastErr
}

// shouldRetryError determines if an error is worth retrying
func shouldRetryError(err error) bool {
	// Retry on database not found errors and other transient errors
	if database.IsNotFoundError(err) {
		return true
	}

	// Retry on general consensus errors that might be transient
	// Don't retry on validation errors or permanent failures
	return !errors.Is(err, rpccontext.ErrBuildBlockVerboseDataInvalidBlock)
}

// HandleGetBlock handles the respectively named RPC command
func HandleGetBlock(context *rpccontext.Context, _ *router.Router, request appmessage.Message) (appmessage.Message, error) {
	getBlockRequest := request.(*appmessage.GetBlockRequestMessage)

	// Load the raw block bytes from the database.
	hash, err := externalapi.NewDomainHashFromString(getBlockRequest.Hash)
	if err != nil {
		errorMessage := &appmessage.GetBlockResponseMessage{}
		errorMessage.Error = appmessage.RPCErrorf("Hash could not be parsed: %s", err)
		return errorMessage, nil
	}

	block, err := context.Domain.Consensus().GetBlockEvenIfHeaderOnly(hash)
	if err != nil {
		errorMessage := &appmessage.GetBlockResponseMessage{}
		errorMessage.Error = appmessage.RPCErrorf("Block %s error %s", hash, err.Error())
		return errorMessage, nil
	}

	response := appmessage.NewGetBlockResponseMessage()

	if getBlockRequest.IncludeTransactions {
		response.Block = appmessage.DomainBlockToRPCBlock(block)
	} else {
		response.Block = appmessage.DomainBlockToRPCBlock(&externalapi.DomainBlock{Header: block.Header})
	}

	err = context.PopulateBlockWithVerboseData(response.Block, block.Header, block, getBlockRequest.IncludeTransactions)
	if err != nil {
		if errors.Is(err, rpccontext.ErrBuildBlockVerboseDataInvalidBlock) {
			errorMessage := &appmessage.GetBlockResponseMessage{}
			errorMessage.Error = appmessage.RPCErrorf("Block %s is invalid", hash)
			return errorMessage, nil
		}
		errorMessage := &appmessage.GetBlockResponseMessage{}
		errorMessage.Error = appmessage.RPCErrorf("Block %s caused an error", hash)
		return errorMessage, err
	}

	return response, nil
}
