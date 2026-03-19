package rpchandlers

import (
	"sync"
	"time"

	"github.com/Hoosat-Oy/HTND/app/appmessage"
	"github.com/Hoosat-Oy/HTND/app/rpc/rpccontext"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/hashes"
	"github.com/Hoosat-Oy/HTND/infrastructure/network/netadapter/router"
)

var (
	getBlocksCache = make(map[string]struct {
		RPCBlocks   []*appmessage.RPCBlock
		BlockHashes []string
		timestamp   time.Time
	})
	getBlocksCacheMutex sync.Mutex
)

const getBlocksCacheTTL = time.Second

var blocksPool = sync.Pool{
	New: func() interface{} {
		return make([]*appmessage.RPCBlock, 0, 2)
	},
}

func cloneRPCBlocks(blocks []*appmessage.RPCBlock) []*appmessage.RPCBlock {
	if len(blocks) == 0 {
		return nil
	}
	clone := make([]*appmessage.RPCBlock, len(blocks))
	copy(clone, blocks)
	return clone
}

func releaseRPCBlocks(blocks []*appmessage.RPCBlock) {
	clear(blocks[:cap(blocks)])
	blocksPool.Put(blocks[:0])
}

func purgeExpiredGetBlocksCache(now time.Time) {
	for key, entry := range getBlocksCache {
		if now.Sub(entry.timestamp) >= getBlocksCacheTTL {
			delete(getBlocksCache, key)
		}
	}
}

// HandleGetBlocks handles the respectively named RPC command
func HandleGetBlocks(context *rpccontext.Context, _ *router.Router, request appmessage.Message) (appmessage.Message, error) {
	getBlocksRequest := request.(*appmessage.GetBlocksRequestMessage)

	// Validate that user didn't set IncludeTransactions without setting IncludeBlocks
	if !getBlocksRequest.IncludeBlocks && getBlocksRequest.IncludeTransactions {
		return &appmessage.GetBlocksResponseMessage{
			Error: appmessage.RPCErrorf(
				"If includeTransactions is set, then includeBlockVerboseData must be set as well"),
		}, nil
	}

	// Decode lowHash
	// If lowHash is empty - use genesis instead.
	lowHash := context.Config.ActiveNetParams.GenesisHash
	if getBlocksRequest.LowHash != "" {
		var err error
		lowHash, err = externalapi.NewDomainHashFromString(getBlocksRequest.LowHash)
		if err != nil {
			return &appmessage.GetBlocksResponseMessage{
				Error: appmessage.RPCErrorf("Could not decode lowHash %s: %s", getBlocksRequest.LowHash, err),
			}, nil
		}

		blockInfo, err := context.Domain.Consensus().GetBlockInfo(lowHash)
		if err != nil {
			return &appmessage.GetBlocksResponseMessage{
				Error: appmessage.RPCErrorf("Could not get block info for lowHash %s: %s", getBlocksRequest.LowHash, err),
			}, nil
		}

		if !blockInfo.HasHeader() {
			return &appmessage.GetBlocksResponseMessage{
				Error: appmessage.RPCErrorf("Could not find lowHash %s", getBlocksRequest.LowHash),
			}, nil
		}
	}
	cacheKey := lowHash.String()

	getBlocksCacheMutex.Lock()
	now := time.Now()
	purgeExpiredGetBlocksCache(now)
	cached, found := getBlocksCache[cacheKey]
	if found {
		getBlocksCacheMutex.Unlock()
		response := appmessage.NewGetBlocksResponseMessage()
		response.Blocks = cached.RPCBlocks
		response.BlockHashes = cached.BlockHashes
		return response, nil
	}
	getBlocksCacheMutex.Unlock()

	// Get hashes between lowHash and virtualSelectedParent
	virtualSelectedParent, err := context.Domain.Consensus().GetVirtualSelectedParent()
	if err != nil {
		return &appmessage.GetBlocksResponseMessage{
			Error: appmessage.RPCErrorf(
				"Couldn't get virtual selected parent: %s", err),
		}, nil
	}

	// We use +1 because lowHash is also returned
	// maxBlocks MUST be >= MergeSetSizeLimit + 1
	maxBlocks := context.Config.NetParams().MergeSetSizeLimit + 1
	blockHashes, highHash, err := context.Domain.Consensus().GetHashesBetween(lowHash, virtualSelectedParent, maxBlocks)
	if err != nil {
		return &appmessage.GetBlocksResponseMessage{
			Error: appmessage.RPCErrorf(
				"Couldn't get hashes between %s and %s: %s", lowHash, virtualSelectedParent, err),
		}, nil
	}

	// prepend low hash to make it inclusive
	blockHashes = append([]*externalapi.DomainHash{lowHash}, blockHashes...)

	// If the high hash is equal to virtualSelectedParent it means GetHashesBetween didn't skip any hashes, and
	// there's space to add the virtualSelectedParent's anticone, otherwise you can't add the anticone because
	// there's no guarantee that all of the anticone root ancestors will be present.
	if highHash.Equal(virtualSelectedParent) {
		virtualSelectedParentAnticone, err := context.Domain.Consensus().Anticone(virtualSelectedParent)
		if err != nil {
			return nil, err
		}
		blockHashes = append(blockHashes, virtualSelectedParentAnticone...)
	}

	// Prepare the response
	response := appmessage.NewGetBlocksResponseMessage()
	response.BlockHashes = hashes.ToStrings(blockHashes)
	if getBlocksRequest.IncludeBlocks {
		rpcBlocks := blocksPool.Get().([]*appmessage.RPCBlock)[:0]
		if cap(rpcBlocks) < len(blockHashes) {
			rpcBlocks = make([]*appmessage.RPCBlock, 0, len(blockHashes))
		}
		defer releaseRPCBlocks(rpcBlocks)
		rpcBlocks = rpcBlocks[:len(blockHashes)]
		for i, blockHash := range blockHashes {
			block, err := context.Domain.Consensus().GetBlockEvenIfHeaderOnly(blockHash)
			if err != nil {
				return nil, err
			}

			if getBlocksRequest.IncludeTransactions {
				rpcBlocks[i] = appmessage.DomainBlockToRPCBlock(block)
			} else {
				rpcBlocks[i] = appmessage.DomainBlockToRPCBlock(&externalapi.DomainBlock{Header: block.Header})
			}
			err = context.PopulateBlockWithVerboseData(rpcBlocks[i], block.Header, block, getBlocksRequest.IncludeTransactions)
			if err != nil {
				return nil, err
			}
		}
		response.Blocks = cloneRPCBlocks(rpcBlocks)
	}

	getBlocksCacheMutex.Lock()
	purgeExpiredGetBlocksCache(now)
	getBlocksCache[cacheKey] = struct {
		RPCBlocks   []*appmessage.RPCBlock
		BlockHashes []string
		timestamp   time.Time
	}{
		RPCBlocks:   response.Blocks,
		BlockHashes: response.BlockHashes,
		timestamp:   now,
	}
	getBlocksCacheMutex.Unlock()

	return response, nil
}
