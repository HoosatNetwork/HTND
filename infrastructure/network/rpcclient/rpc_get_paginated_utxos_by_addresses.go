package rpcclient

import "github.com/Hoosat-Oy/HTND/app/appmessage"

// GetUTXOsByAddresses sends an RPC request respective to the function's name and returns the RPC server's response
func (c *RPCClient) GetPaginatedUTXOsByAddresses(addresses []string, offset uint32, limit uint32) (*appmessage.GetUTXOsByAddressesResponseMessage, error) {
	err := c.rpcRouter.outgoingRoute().Enqueue(appmessage.NewGetPaginatedUTXOsByAddressesRequestMessage(addresses, offset, limit))
	if err != nil {
		return nil, err
	}
	response, err := c.route(appmessage.CmdGetPaginatedUTXOsByAddressesResponseMessage).DequeueWithTimeout(c.timeout)
	if err != nil {
		return nil, err
	}
	getUTXOsByAddressesResponse := response.(*appmessage.GetUTXOsByAddressesResponseMessage)
	if getUTXOsByAddressesResponse.Error != nil {
		return nil, c.convertRPCError(getUTXOsByAddressesResponse.Error)
	}
	return getUTXOsByAddressesResponse, nil
}
