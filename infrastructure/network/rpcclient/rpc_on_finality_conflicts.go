package rpcclient

import (
	"github.com/Hoosat-Oy/HTND/app/appmessage"
	routerpkg "github.com/Hoosat-Oy/HTND/infrastructure/network/netadapter/router"
	"github.com/pkg/errors"
)

// RegisterForFinalityConflictsNotifications sends an RPC request respective to the function's name and returns the RPC server's response.
// Additionally, it starts listening for the appropriate notification using the given handler function
func (c *RPCClient) RegisterForFinalityConflictsNotifications(
	onFinalityConflict func(notification *appmessage.FinalityConflictNotificationMessage),
	onFinalityConflictResolved func(notification *appmessage.FinalityConflictResolvedNotificationMessage),
) error {
	err := c.rpcRouter.outgoingRoute().Enqueue(appmessage.NewNotifyFinalityConflictsRequestMessage())
	if err != nil {
		return err
	}
	response, err := c.route(appmessage.CmdNotifyFinalityConflictsResponseMessage).DequeueWithTimeout(c.timeout)
	if err != nil {
		return err
	}
	notifyFinalityConflictsResponse := response.(*appmessage.NotifyFinalityConflictsResponseMessage)
	if notifyFinalityConflictsResponse.Error != nil {
		return c.convertRPCError(notifyFinalityConflictsResponse.Error)
	}
	spawn("RegisterForFinalityConflictsNotifications-finalityConflict", func() {
		defer func() {
			_ = recover()
		}()
		for {
			notification, err := c.route(appmessage.CmdFinalityConflictNotificationMessage).DequeueWithTimeout(c.timeout)
			if err != nil {
				if errors.Is(err, routerpkg.ErrRouteClosed) {
					break
				}
				// Timeout or other error: exit goroutine gracefully
				return
			}
			finalityConflictNotification, ok := notification.(*appmessage.FinalityConflictNotificationMessage)
			if !ok {
				// Unexpected type, skip
				continue
			}
			// Recover from panics in handler
			func() {
				defer func() {
					_ = recover()
				}()
				onFinalityConflict(finalityConflictNotification)
			}()
		}
	})
	spawn("RegisterForFinalityConflictsNotifications-finalityConflictResolved", func() {
		defer func() {
			_ = recover()
		}()
		for {
			notification, err := c.route(appmessage.CmdFinalityConflictResolvedNotificationMessage).DequeueWithTimeout(c.timeout)
			if err != nil {
				if errors.Is(err, routerpkg.ErrRouteClosed) {
					break
				}
				// Timeout or other error: exit goroutine gracefully
				return
			}
			finalityConflictResolvedNotification, ok := notification.(*appmessage.FinalityConflictResolvedNotificationMessage)
			if !ok {
				// Unexpected type, skip
				continue
			}
			// Recover from panics in handler
			func() {
				defer func() {
					if r := recover(); r != nil {
						log.Warnf("panic in finality conflict resolved handler: %v", r)
					}
				}()
				onFinalityConflictResolved(finalityConflictResolvedNotification)
			}()
		}
	})
	return nil
}
