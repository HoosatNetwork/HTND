package rpcclient

import "github.com/HoosatNetwork/HTND/app/appmessage"

// GetTransactionStatus sends an RPC request respective to the function's name and returns the RPC server's response.
func (c *RPCClient) GetTransactionStatus(transactionID string) (*appmessage.GetTransactionStatusResponseMessage, error) {
	err := c.rpcRouter.outgoingRoute().Enqueue(appmessage.NewGetTransactionStatusRequestMessage(transactionID))
	if err != nil {
		return nil, err
	}
	response, err := c.route(appmessage.CmdGetTransactionStatusResponseMessage).DequeueWithTimeout(c.timeout)
	if err != nil {
		return nil, err
	}
	getTransactionStatusResponse := response.(*appmessage.GetTransactionStatusResponseMessage)
	if getTransactionStatusResponse.Error != nil {
		return nil, c.convertRPCError(getTransactionStatusResponse.Error)
	}
	return getTransactionStatusResponse, nil
}
