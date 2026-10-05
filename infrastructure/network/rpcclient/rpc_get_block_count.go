package rpcclient

import "github.com/HoosatNetwork/HTND/v2/app/appmessage"

// GetBlockCount sends an RPC request respective to the function's name and returns the RPC server's response
func (c *RPCClient) GetBlockCount() (*appmessage.GetBlockCountResponseMessage, error) {
	err := c.outgoingRoute().Enqueue(appmessage.NewGetBlockCountRequestMessage())
	if err != nil {
		return nil, err
	}
	response, err := c.route(appmessage.CmdGetBlockCountResponseMessage).DequeueWithTimeout(c.getTimeout())
	if err != nil {
		return nil, err
	}
	getBlockCountResponse := response.(*appmessage.GetBlockCountResponseMessage)
	if getBlockCountResponse.Error != nil {
		return nil, c.convertRPCError(getBlockCountResponse.Error)
	}
	return getBlockCountResponse, nil
}
