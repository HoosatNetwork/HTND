package rpcclient

import "github.com/HoosatNetwork/HTND/v2/app/appmessage"

// GetMempoolEntriesByAddresses sends an RPC request respective to the function's name and returns the RPC server's response
func (c *RPCClient) GetMempoolEntriesByAddresses(addresses []string, includeOrphanPool bool, filterTransactionPool bool) (*appmessage.GetMempoolEntriesByAddressesResponseMessage, error) {
	err := c.outgoingRoute().Enqueue(appmessage.NewGetMempoolEntriesByAddressesRequestMessage(addresses, includeOrphanPool, filterTransactionPool))
	if err != nil {
		return nil, err
	}
	response, err := c.route(appmessage.CmdGetMempoolEntriesByAddressesResponseMessage).DequeueWithTimeout(c.getTimeout())
	if err != nil {
		return nil, err
	}
	getMempoolEntriesByAddressesResponse := response.(*appmessage.GetMempoolEntriesByAddressesResponseMessage)
	if getMempoolEntriesByAddressesResponse.Error != nil {
		return nil, c.convertRPCError(getMempoolEntriesByAddressesResponse.Error)
	}
	return getMempoolEntriesByAddressesResponse, nil
}
