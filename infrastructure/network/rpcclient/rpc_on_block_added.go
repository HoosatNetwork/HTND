package rpcclient

import (
	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
)

// RegisterForBlockAddedNotifications sends an RPC request respective to the function's name and returns the RPC server's response.
// Additionally, it starts listening for the appropriate notification using the given handler function
// A later call replaces the handler, and the client registers again after a reconnect.
func (c *RPCClient) RegisterForBlockAddedNotifications(onBlockAdded func(notification *appmessage.BlockAddedNotificationMessage)) error {
	return c.registerForNotifications(appmessage.CmdNotifyBlockAddedRequestMessage,
		appmessage.CmdNotifyBlockAddedResponseMessage, func(router *rpcRouter) error {
			err := router.outgoingRoute().Enqueue(appmessage.NewNotifyBlockAddedRequestMessage())
			if err != nil {
				return err
			}
			response, err := router.routes[appmessage.CmdNotifyBlockAddedResponseMessage].DequeueWithTimeout(c.getTimeout())
			if err != nil {
				return err
			}
			notifyBlockAddedResponse := response.(*appmessage.NotifyBlockAddedResponseMessage)
			if notifyBlockAddedResponse.Error != nil {
				return c.convertRPCError(notifyBlockAddedResponse.Error)
			}
			return nil
		}, notificationHandlers{
			appmessage.CmdBlockAddedNotificationMessage: func(notification appmessage.Message) {
				onBlockAdded(notification.(*appmessage.BlockAddedNotificationMessage))
			},
		})
}
