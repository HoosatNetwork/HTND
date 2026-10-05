package rpcclient

import "github.com/HoosatNetwork/HTND/v2/app/appmessage"

// GetWalletUTXOs sends an RPC request respective to the function's name and returns the RPC server's response
func (c *RPCClient) GetWalletUTXOs(extendedPublicKeys []string, minimumSignatures uint32, ecdsa bool, gapLimit uint32,
	limit uint32,
) (*appmessage.GetWalletUTXOsResponseMessage, error) {
	err := c.outgoingRoute().Enqueue(appmessage.NewGetWalletUTXOsRequestMessage(extendedPublicKeys, minimumSignatures,
		ecdsa, gapLimit, limit))
	if err != nil {
		return nil, err
	}
	response, err := c.route(appmessage.CmdGetWalletUTXOsResponseMessage).DequeueWithTimeout(c.getTimeout())
	if err != nil {
		return nil, err
	}
	getWalletUTXOsResponse := response.(*appmessage.GetWalletUTXOsResponseMessage)
	if getWalletUTXOsResponse.Error != nil {
		return nil, c.convertRPCError(getWalletUTXOsResponse.Error)
	}
	return getWalletUTXOsResponse, nil
}
