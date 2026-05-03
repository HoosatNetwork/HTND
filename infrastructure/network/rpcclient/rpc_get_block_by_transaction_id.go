package rpcclient

import "github.com/Hoosat-Oy/HTND/app/appmessage"

// GetBlockByTransactionID sends an RPC request respective to the function's name and returns the RPC server's response.
func (c *RPCClient) GetBlockByTransactionID(transactionID string, includeTransactions bool) (
	*appmessage.GetBlockByTransactionIDResponseMessage, error,
) {
	err := c.rpcRouter.outgoingRoute().Enqueue(
		appmessage.NewGetBlockByTransactionIDRequestMessage(transactionID, includeTransactions),
	)
	if err != nil {
		return nil, err
	}
	response, err := c.route(appmessage.CmdGetBlockByTransactionIDResponseMessage).DequeueWithTimeout(c.timeout)
	if err != nil {
		return nil, err
	}
	getBlockByTransactionIDResponse := response.(*appmessage.GetBlockByTransactionIDResponseMessage)
	if getBlockByTransactionIDResponse.Error != nil {
		return nil, c.convertRPCError(getBlockByTransactionIDResponse.Error)
	}
	return getBlockByTransactionIDResponse, nil
}
