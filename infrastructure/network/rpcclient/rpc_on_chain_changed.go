package rpcclient

import (
	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
)

// RegisterForVirtualSelectedParentChainChangedNotifications sends an RPC request respective to the function's name and returns the RPC server's response.
// Additionally, it starts listening for the appropriate notification using the given handler function
// A later call replaces the handler, and the client registers again after a reconnect.
func (c *RPCClient) RegisterForVirtualSelectedParentChainChangedNotifications(includeAcceptedTransactionIDs bool,
	onChainChanged func(notification *appmessage.VirtualSelectedParentChainChangedNotificationMessage),
) error {
	return c.registerForNotifications(appmessage.CmdNotifyVirtualSelectedParentChainChangedRequestMessage,
		appmessage.CmdNotifyVirtualSelectedParentChainChangedResponseMessage, func(router *rpcRouter) error {
			err := router.outgoingRoute().Enqueue(
				appmessage.NewNotifyVirtualSelectedParentChainChangedRequestMessage(includeAcceptedTransactionIDs))
			if err != nil {
				return err
			}
			response, err := router.routes[appmessage.CmdNotifyVirtualSelectedParentChainChangedResponseMessage].DequeueWithTimeout(c.getTimeout())
			if err != nil {
				return err
			}
			notifyChainChangedResponse := response.(*appmessage.NotifyVirtualSelectedParentChainChangedResponseMessage)
			if notifyChainChangedResponse.Error != nil {
				return c.convertRPCError(notifyChainChangedResponse.Error)
			}
			return nil
		}, notificationHandlers{
			appmessage.CmdVirtualSelectedParentChainChangedNotificationMessage: func(notification appmessage.Message) {
				onChainChanged(notification.(*appmessage.VirtualSelectedParentChainChangedNotificationMessage))
			},
		})
}
