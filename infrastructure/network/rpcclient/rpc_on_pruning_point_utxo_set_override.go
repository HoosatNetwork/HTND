package rpcclient

import (
	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
)

// RegisterPruningPointUTXOSetNotifications sends an RPC request respective to the function's name and returns the RPC server's response.
// Additionally, it starts listening for the appropriate notification using the given handler function
// A later call replaces the handler, and the client registers again after a reconnect.
func (c *RPCClient) RegisterPruningPointUTXOSetNotifications(onPruningPointUTXOSetNotifications func()) error {
	return c.registerForNotifications(appmessage.CmdNotifyPruningPointUTXOSetOverrideRequestMessage,
		appmessage.CmdNotifyPruningPointUTXOSetOverrideResponseMessage, func(router *rpcRouter) error {
			err := router.outgoingRoute().Enqueue(appmessage.NewNotifyPruningPointUTXOSetOverrideRequestMessage())
			if err != nil {
				return err
			}
			response, err := router.routes[appmessage.CmdNotifyPruningPointUTXOSetOverrideResponseMessage].DequeueWithTimeout(c.getTimeout())
			if err != nil {
				return err
			}
			notifyPruningPointUTXOSetOverrideResponse := response.(*appmessage.NotifyPruningPointUTXOSetOverrideResponseMessage)
			if notifyPruningPointUTXOSetOverrideResponse.Error != nil {
				return c.convertRPCError(notifyPruningPointUTXOSetOverrideResponse.Error)
			}
			return nil
		}, notificationHandlers{
			appmessage.CmdPruningPointUTXOSetOverrideNotificationMessage: func(notification appmessage.Message) {
				_ = notification.(*appmessage.PruningPointUTXOSetOverrideNotificationMessage) // Sanity check the type
				onPruningPointUTXOSetNotifications()
			},
		})
}

// UnregisterPruningPointUTXOSetNotifications sends an RPC request respective to the function's name and returns the RPC server's response.
// Additionally, it stops listening for the appropriate notification using the given handler function
// The handler stays set, but a reconnect no longer registers again.
func (c *RPCClient) UnregisterPruningPointUTXOSetNotifications() error {
	// Under the registration's mutex, so a reconnect cannot send the registration again after this.
	registration := c.notificationRegistration(appmessage.CmdNotifyPruningPointUTXOSetOverrideRequestMessage,
		appmessage.CmdNotifyPruningPointUTXOSetOverrideResponseMessage)
	registration.mutex.Lock()
	defer registration.mutex.Unlock()
	err := c.sendNotificationRequest(registration, c.currentRPCRouter(),
		appmessage.CmdStopNotifyingPruningPointUTXOSetOverrideResponseMessage, func(router *rpcRouter) error {
			err := router.outgoingRoute().Enqueue(appmessage.NewStopNotifyingPruningPointUTXOSetOverrideRequestMessage())
			if err != nil {
				return err
			}
			response, err := router.routes[appmessage.CmdStopNotifyingPruningPointUTXOSetOverrideResponseMessage].DequeueWithTimeout(c.getTimeout())
			if err != nil {
				return err
			}
			stopNotifyPruningPointUTXOSetOverrideResponse := response.(*appmessage.StopNotifyingPruningPointUTXOSetOverrideResponseMessage)
			if stopNotifyPruningPointUTXOSetOverrideResponse.Error != nil {
				return c.convertRPCError(stopNotifyPruningPointUTXOSetOverrideResponse.Error)
			}
			return nil
		})
	if err != nil {
		return err
	}
	// Stop sending the registration again after a reconnect. Like before, the handler stays registered.
	registration.register = nil
	registration.registeredOn = nil
	return nil
}
