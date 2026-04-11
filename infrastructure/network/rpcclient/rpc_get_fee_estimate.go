package rpcclient

import "github.com/Hoosat-Oy/HTND/app/appmessage"

// GetFeeEstimate sends an RPC request for fee estimation and returns the RPC server's response.
func (c *RPCClient) GetFeeEstimate() (*appmessage.GetFeeEstimateResponseMessage, error) {
	err := c.rpcRouter.outgoingRoute().Enqueue(appmessage.NewGetFeeEstimateRequestMessage())
	if err != nil {
		return nil, err
	}
	response, err := c.route(appmessage.CmdGetFeeEstimateResponseMessage).DequeueWithTimeout(c.timeout)
	if err != nil {
		return nil, err
	}
	getFeeEstimateResponse := response.(*appmessage.GetFeeEstimateResponseMessage)
	if getFeeEstimateResponse.Error != nil {
		return nil, c.convertRPCError(getFeeEstimateResponse.Error)
	}
	return getFeeEstimateResponse, nil
}
