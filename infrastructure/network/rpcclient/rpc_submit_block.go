package rpcclient

import (
	"github.com/Hoosat-Oy/HTND/app/appmessage"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
)

func (c *RPCClient) submitBlock(block *externalapi.DomainBlock, powHash *externalapi.DomainHash, allowNonDAABlocks bool) (appmessage.RejectReason, error) {
	submitBlockRequest := appmessage.NewSubmitBlockRequestMessage(appmessage.DomainBlockToRPCBlock(block), allowNonDAABlocks, powHash)
	err := c.rpcRouter.outgoingRoute().Enqueue(submitBlockRequest)
	if err != nil {
		return appmessage.RejectReasonNone, err
	}
	response, err := c.route(appmessage.CmdSubmitBlockResponseMessage).DequeueWithTimeout(c.timeout)
	if err != nil {
		return appmessage.RejectReasonNone, err
	}
	submitBlockResponse := response.(*appmessage.SubmitBlockResponseMessage)
	if submitBlockResponse.Error != nil {
		return submitBlockResponse.RejectReason, c.convertRPCError(submitBlockResponse.Error)
	}
	return appmessage.RejectReasonNone, nil
}

// SubmitBlock sends an RPC request respective to the function's name and returns the RPC server's response
func (c *RPCClient) SubmitBlock(block *externalapi.DomainBlock, powHash *externalapi.DomainHash) (appmessage.RejectReason, error) {
	return c.submitBlock(block, powHash, false)
}

// SubmitBlockAlsoIfNonDAA operates the same as SubmitBlock with the exception that `allowNonDAABlocks` is set to true
func (c *RPCClient) SubmitBlockAlsoIfNonDAA(block *externalapi.DomainBlock, powHash *externalapi.DomainHash) (appmessage.RejectReason, error) {
	return c.submitBlock(block, powHash, true)
}
