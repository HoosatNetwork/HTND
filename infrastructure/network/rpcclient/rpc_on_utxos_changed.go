package rpcclient

import (
	"slices"

	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
)

// RegisterForUTXOsChangedNotifications sends an RPC request respective to the function's name and returns the RPC server's response.
// Additionally, it starts listening for the appropriate notification using the given handler function.
// Like the node, the client adds the addresses to those of earlier calls, and a reconnect registers all of them
// again. A later call replaces the handler.
func (c *RPCClient) RegisterForUTXOsChangedNotifications(addresses []string,
	onUTXOsChanged func(notification *appmessage.UTXOsChangedNotificationMessage),
) error {
	registration := c.notificationRegistration(appmessage.CmdNotifyUTXOsChangedRequestMessage,
		appmessage.CmdNotifyUTXOsChangedResponseMessage)
	registration.mutex.Lock()
	defer registration.mutex.Unlock()

	allAddresses := slices.Clone(registration.addresses)
	known := make(map[string]struct{}, len(allAddresses)+len(addresses))
	for _, address := range allAddresses {
		known[address] = struct{}{}
	}
	for _, address := range addresses {
		if _, ok := known[address]; !ok {
			known[address] = struct{}{}
			allAddresses = append(allAddresses, address)
		}
	}
	// The node already has the earlier addresses on the connection that accepted them; any other needs all.
	router := c.currentRPCRouter()
	send := allAddresses
	if registration.registeredOn == router {
		send = slices.Clone(addresses)
	}
	err := c.registerLocked(registration, router, c.notifyUTXOsChanged(send), c.notifyUTXOsChanged(allAddresses),
		notificationHandlers{
			appmessage.CmdUTXOsChangedNotificationMessage: func(notification appmessage.Message) {
				onUTXOsChanged(notification.(*appmessage.UTXOsChangedNotificationMessage))
			},
		})
	if err != nil {
		return err
	}
	registration.addresses = allAddresses
	return nil
}

// notifyUTXOsChanged returns the registration function sending a NotifyUTXOsChanged request for addresses.
func (c *RPCClient) notifyUTXOsChanged(addresses []string) func(router *rpcRouter) error {
	return func(router *rpcRouter) error {
		err := router.outgoingRoute().Enqueue(appmessage.NewNotifyUTXOsChangedRequestMessage(addresses))
		if err != nil {
			return err
		}
		response, err := router.routes[appmessage.CmdNotifyUTXOsChangedResponseMessage].DequeueWithTimeout(c.getTimeout())
		if err != nil {
			return err
		}
		notifyUTXOsChangedResponse := response.(*appmessage.NotifyUTXOsChangedResponseMessage)
		if notifyUTXOsChangedResponse.Error != nil {
			return c.convertRPCError(notifyUTXOsChangedResponse.Error)
		}
		return nil
	}
}
