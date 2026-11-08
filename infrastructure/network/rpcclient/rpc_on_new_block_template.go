package rpcclient

import (
	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
)

// RegisterForNewBlockTemplateNotifications sends an RPC request respective to the function's name and returns the RPC server's response.
// Additionally, it starts listening for the appropriate notification using the given handler function
// A later call replaces the handler, and the client registers again after a reconnect.
func (c *RPCClient) RegisterForNewBlockTemplateNotifications(onNewBlockTemplate func(notification *appmessage.NewBlockTemplateNotificationMessage)) error {
	return c.registerForNotifications(appmessage.CmdNotifyNewBlockTemplateRequestMessage,
		appmessage.CmdNotifyNewBlockTemplateResponseMessage, func(router *rpcRouter) error {
			err := router.outgoingRoute().Enqueue(appmessage.NewNotifyNewBlockTemplateRequestMessage())
			if err != nil {
				return err
			}
			response, err := router.routes[appmessage.CmdNotifyNewBlockTemplateResponseMessage].DequeueWithTimeout(c.getTimeout())
			if err != nil {
				return err
			}
			notifyNewBlockTemplateResponse := response.(*appmessage.NotifyNewBlockTemplateResponseMessage)
			if notifyNewBlockTemplateResponse.Error != nil {
				return c.convertRPCError(notifyNewBlockTemplateResponse.Error)
			}
			return nil
		}, notificationHandlers{
			appmessage.CmdNewBlockTemplateNotificationMessage: func(notification appmessage.Message) {
				onNewBlockTemplate(notification.(*appmessage.NewBlockTemplateNotificationMessage))
			},
		})
}
