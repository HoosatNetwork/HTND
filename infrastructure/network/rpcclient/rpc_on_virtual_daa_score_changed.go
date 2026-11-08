package rpcclient

import (
	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
)

// RegisterForVirtualDaaScoreChangedNotifications sends an RPC request respective to the function's
// name and returns the RPC server's response. Additionally, it starts listening for the appropriate notification
// using the given handler function
// A later call replaces the handler, and the client registers again after a reconnect.
func (c *RPCClient) RegisterForVirtualDaaScoreChangedNotifications(
	onVirtualDaaScoreChanged func(notification *appmessage.VirtualDaaScoreChangedNotificationMessage),
) error {
	return c.registerForNotifications(appmessage.CmdNotifyVirtualDaaScoreChangedRequestMessage,
		appmessage.CmdNotifyVirtualDaaScoreChangedResponseMessage, func(router *rpcRouter) error {
			err := router.outgoingRoute().Enqueue(appmessage.NewNotifyVirtualDaaScoreChangedRequestMessage())
			if err != nil {
				return err
			}
			response, err := router.routes[appmessage.CmdNotifyVirtualDaaScoreChangedResponseMessage].DequeueWithTimeout(c.getTimeout())
			if err != nil {
				return err
			}
			notifyVirtualDaaScoreChangedResponse := response.(*appmessage.NotifyVirtualDaaScoreChangedResponseMessage)
			if notifyVirtualDaaScoreChangedResponse.Error != nil {
				return c.convertRPCError(notifyVirtualDaaScoreChangedResponse.Error)
			}
			return nil
		}, notificationHandlers{
			appmessage.CmdVirtualDaaScoreChangedNotificationMessage: func(notification appmessage.Message) {
				onVirtualDaaScoreChanged(notification.(*appmessage.VirtualDaaScoreChangedNotificationMessage))
			},
		})
}
