package rpcclient

import "github.com/HoosatNetwork/HTND/v2/app/appmessage"

// GetUsableAddresses sends an RPC request respective to the function's name and returns the RPC server's response
func (c *RPCClient) GetUsableAddresses(addresses []string) (*appmessage.GetUsableAddressesResponseMessage, error) {
	err := c.outgoingRoute().Enqueue(appmessage.NewGetUsableAddressesRequest(addresses))
	if err != nil {
		return nil, err
	}
	log.Debugf("Enqueued NewGetUsableAddressesRequest")
	// Wait with the client's timeout like every other call. htnwallet makes this call while holding its
	// server lock, so waiting without one let a node that never answered hang the wallet entirely.
	response, err := c.route(appmessage.CmdGetUsableAddressesResponseMessage).DequeueWithTimeout(c.getTimeout())
	if err != nil {
		return nil, err
	}
	log.Debugf("Got GetUsableAddressesResponseMessage response")
	getUsableAddressesResponse := response.(*appmessage.GetUsableAddressesResponseMessage)
	if getUsableAddressesResponse.Error != nil {
		return nil, c.convertRPCError(getUsableAddressesResponse.Error)
	}
	return getUsableAddressesResponse, nil
}
