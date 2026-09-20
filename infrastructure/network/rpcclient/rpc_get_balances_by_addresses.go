package rpcclient

import "github.com/HoosatNetwork/HTND/app/appmessage"

// GetBalancesByAddresses sends an RPC request respective to the function's name and returns the RPC server's response
func (c *RPCClient) GetBalancesByAddresses(addresses []string) (*appmessage.GetBalancesByAddressesResponseMessage, error) {
	err := c.rpcRouter.outgoingRoute().Enqueue(appmessage.NewGetBalancesByAddressesRequest(addresses))
	if err != nil {
		return nil, err
	}
	response, err := c.route(appmessage.CmdGetBalancesByAddressesResponseMessage).Dequeue()
	if err != nil {
		return nil, err
	}
	getBalancesByAddressesResponse := response.(*appmessage.GetBalancesByAddressesResponseMessage)
	if getBalancesByAddressesResponse.Error != nil {
		return nil, c.convertRPCError(getBalancesByAddressesResponse.Error)
	}
	return getBalancesByAddressesResponse, nil
}
