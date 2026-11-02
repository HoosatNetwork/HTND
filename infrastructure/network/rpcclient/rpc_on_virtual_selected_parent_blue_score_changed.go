package rpcclient

import (
	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
)

// RegisterForVirtualSelectedParentBlueScoreChangedNotifications sends an RPC request respective to the function's
// name and returns the RPC server's response. Additionally, it starts listening for the appropriate notification
// using the given handler function
// A later call replaces the handler, and the client registers again after a reconnect.
func (c *RPCClient) RegisterForVirtualSelectedParentBlueScoreChangedNotifications(
	onVirtualSelectedParentBlueScoreChanged func(notification *appmessage.VirtualSelectedParentBlueScoreChangedNotificationMessage),
) error {
	return c.registerForNotifications(appmessage.CmdNotifyVirtualSelectedParentBlueScoreChangedRequestMessage,
		appmessage.CmdNotifyVirtualSelectedParentBlueScoreChangedResponseMessage, func(router *rpcRouter) error {
			err := router.outgoingRoute().Enqueue(appmessage.NewNotifyVirtualSelectedParentBlueScoreChangedRequestMessage())
			if err != nil {
				return err
			}
			response, err := router.routes[appmessage.CmdNotifyVirtualSelectedParentBlueScoreChangedResponseMessage].DequeueWithTimeout(c.getTimeout())
			if err != nil {
				return err
			}
			notifyVirtualSelectedParentBlueScoreChangedResponse := response.(*appmessage.NotifyVirtualSelectedParentBlueScoreChangedResponseMessage)
			if notifyVirtualSelectedParentBlueScoreChangedResponse.Error != nil {
				return c.convertRPCError(notifyVirtualSelectedParentBlueScoreChangedResponse.Error)
			}
			return nil
		}, notificationHandlers{
			appmessage.CmdVirtualSelectedParentBlueScoreChangedNotificationMessage: func(notification appmessage.Message) {
				onVirtualSelectedParentBlueScoreChanged(notification.(*appmessage.VirtualSelectedParentBlueScoreChangedNotificationMessage))
			},
		})
}
