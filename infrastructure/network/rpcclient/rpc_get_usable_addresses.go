package rpcclient

import "github.com/Hoosat-Oy/HTND/app/appmessage"

// GetUsableAddresses sends an RPC request respective to the function's name and returns the RPC server's response
func (c *RPCClient) GetUsableAddresses(addresses []string) (*appmessage.GetUsableAddressesResponseMessage, error) {
	err := c.rpcRouter.outgoingRoute().Enqueue(appmessage.NewGetUsableAddressesRequest(addresses))
	if err != nil {
		return nil, err
	}
	log.Infof("Enqueued NewGetUsableAddressesRequest")
	response, err := c.route(appmessage.CmdGetUsableAddressesResponseMessage).Dequeue()
	if err != nil {
		return nil, err
	}
	log.Infof("Got GetUsableAddressesResponseMessage response")
	getUsableAddressesResponse := response.(*appmessage.GetUsableAddressesResponseMessage)
	if getUsableAddressesResponse.Error != nil {
		return nil, c.convertRPCError(getUsableAddressesResponse.Error)
	}
	return getUsableAddressesResponse, nil
}
