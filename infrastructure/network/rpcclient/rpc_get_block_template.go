package rpcclient

import "github.com/HoosatNetwork/HTND/v2/app/appmessage"

// GetBlockTemplate sends an RPC request respective to the function's name and returns the RPC server's response
func (c *RPCClient) GetBlockTemplate(miningAddress, extraData string) (*appmessage.GetBlockTemplateResponseMessage, error) {
	err := c.outgoingRoute().Enqueue(appmessage.NewGetBlockTemplateRequestMessage(miningAddress, extraData))
	if err != nil {
		return nil, err
	}
	response, err := c.route(appmessage.CmdGetBlockTemplateResponseMessage).DequeueWithTimeout(c.getTimeout())
	if err != nil {
		return nil, err
	}
	getBlockTemplateResponse := response.(*appmessage.GetBlockTemplateResponseMessage)
	if getBlockTemplateResponse.Error != nil {
		return nil, c.convertRPCError(getBlockTemplateResponse.Error)
	}
	return getBlockTemplateResponse, nil
}
