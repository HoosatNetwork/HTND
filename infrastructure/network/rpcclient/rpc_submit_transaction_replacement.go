package rpcclient

import (
	"strings"

	"github.com/Hoosat-Oy/HTND/app/appmessage"
)

// SubmitTransactionReplacement sends an RPC request respective to the function's name and returns the RPC server's response.
func (c *RPCClient) SubmitTransactionReplacement(transaction *appmessage.RPCTransaction, transactionID string) (*appmessage.SubmitTransactionReplacementResponseMessage, error) {
	return c.SubmitTransactionReplacementWithPriority(transaction, transactionID, nil)
}

// SubmitTransactionReplacementWithPriority sends an RPC request respective to the function's name and returns the RPC server's response.
func (c *RPCClient) SubmitTransactionReplacementWithPriority(transaction *appmessage.RPCTransaction, transactionID string,
	isHighPriority *bool,
) (*appmessage.SubmitTransactionReplacementResponseMessage, error) {
	err := c.rpcRouter.outgoingRoute().Enqueue(appmessage.NewSubmitTransactionReplacementRequestMessageWithPriority(transaction, isHighPriority))
	if err != nil {
		return nil, err
	}
	for {
		response, err := c.route(appmessage.CmdSubmitTransactionReplacementResponseMessage).DequeueWithTimeout(c.timeout)
		if err != nil {
			return nil, err
		}
		submitTransactionReplacementResponse := response.(*appmessage.SubmitTransactionReplacementResponseMessage)
		// Match the response to the expected ID. If they are different it means we got an old response which we
		// previously timed-out on, so we log and continue waiting for the correct current response.
		if submitTransactionReplacementResponse.TransactionID != transactionID {
			if submitTransactionReplacementResponse.Error != nil {
				// A non-updated Kaspad might return an empty ID in the case of error, so in
				// such a case we fallback to checking if the error contains the expected ID
				if submitTransactionReplacementResponse.TransactionID != "" || !strings.Contains(submitTransactionReplacementResponse.Error.Message, transactionID) {
					log.Warnf("SubmitTransactionReplacement: received an error response for previous request: %s", submitTransactionReplacementResponse.Error)
					continue
				}
			} else {
				log.Warnf("SubmitTransactionReplacement: received a successful response for previous request with ID %s",
					submitTransactionReplacementResponse.TransactionID)
				continue
			}
		}
		if submitTransactionReplacementResponse.Error != nil {
			return nil, c.convertRPCError(submitTransactionReplacementResponse.Error)
		}

		return submitTransactionReplacementResponse, nil
	}
}
