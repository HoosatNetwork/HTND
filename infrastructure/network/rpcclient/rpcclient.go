package rpcclient

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/logger"
	routerpkg "github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter/router"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/rpcclient/grpcclient"
	"github.com/HoosatNetwork/HTND/v2/util/panics"
	"github.com/HoosatNetwork/HTND/v2/version"
	"github.com/pkg/errors"
)

const (
	defaultTimeout             = 10 * time.Minute
	initialVersionCheckTimeout = 5 * time.Second
)

// RPCClient is an RPC client
type RPCClient struct {
	*grpcclient.GRPCClient

	rpcAddress            string
	rpcRouter             *rpcRouter
	rpcRouterMutex        sync.RWMutex
	isConnected           atomic.Uint32
	isClosed              atomic.Uint32
	isReconnecting        atomic.Uint32
	lastDisconnectedNanos atomic.Int64
	isConnecting          atomic.Uint32

	// activeClient is the connection whose callbacks are acted on. A replaced connection's loops keep
	// reporting errors after it is closed, and those must not tear down the connection that replaced it.
	activeClient atomic.Pointer[grpcclient.GRPCClient]

	// timeoutNanos is time.Duration nanoseconds. connect() briefly lowers it around the initial
	// GetInfo call, and SetTimeout can be called from user code at any time, both racing an in-flight
	// request method reading it to build a DequeueWithTimeout call - see getTimeout/SetTimeout.
	timeoutNanos atomic.Int64

	// The node keeps notification registrations per connection, so the client keeps them to send again after
	// a reconnect. notificationRegistrations holds them per Notify request command and notificationDeliveries
	// the handler and delivery worker per notification command. Both maps are guarded by notificationsMutex.
	notificationsMutex        sync.Mutex
	notificationRegistrations map[appmessage.MessageCommand]*notificationRegistration
	notificationDeliveries    map[appmessage.MessageCommand]*notificationDelivery
}

// notificationRegistration is what the client keeps for one Notify request command. mutex is held for the
// whole of every request and response on this command's routes: registering, sending it again after a
// reconnect and unregistering. So none of them can overtake another, and none can take another's response
// from the shared response route. Waiting for mutex is not covered by the call timeout: a call queued behind
// another of the same command, such as a replay waiting on the node, can take longer than one timeout.
type notificationRegistration struct {
	mutex           sync.Mutex
	responseCommand appmessage.MessageCommand
	// register sends the Notify request a reconnect sends again and waits for its response; nil once
	// unregistered.
	register func(router *rpcRouter) error
	// registeredOn is the router whose node accepted register. A reconnect has nothing to send there.
	registeredOn *rpcRouter
	// lateResponses counts per response command the responses lateRouter still owes for requests that timed
	// out. The node answers the requests of a connection one at a time and in order, so these arrive before
	// the response to the next request and are dropped.
	lateRouter    *rpcRouter
	lateResponses map[appmessage.MessageCommand]int
	// addresses holds every address registered so far. Used for UTXOsChanged only.
	addresses []string
}

// notificationDelivery is the handler of one notification command and the wakeup of the one worker
// delivering it, see deliverNotifications.
type notificationDelivery struct {
	handler func(notification appmessage.Message)
	wake    chan struct{}
}

// notificationHandlers maps notification commands to the handlers their notifications are delivered to.
type notificationHandlers map[appmessage.MessageCommand]func(notification appmessage.Message)

// getTimeout returns the current RPC call timeout.
func (c *RPCClient) getTimeout() time.Duration {
	return time.Duration(c.timeoutNanos.Load())
}

// NewRPCClient сreates a new RPC client with a default call timeout value
func NewRPCClient(rpcAddress string) (*RPCClient, error) {
	rpcClient := &RPCClient{
		rpcAddress: rpcAddress,
	}
	rpcClient.timeoutNanos.Store(int64(defaultTimeout))
	err := rpcClient.connect()
	if err != nil {
		return nil, err
	}

	return rpcClient, nil
}

func (c *RPCClient) connect() error {
	c.isConnecting.Store(1)
	defer c.isConnecting.Store(0)

	rpcClient, err := grpcclient.Connect(c.rpcAddress)
	if err != nil {
		return errors.Wrapf(err, "error connecting to address %s", c.rpcAddress)
	}
	rpcClient.SetOnDisconnectedHandler(func() {
		if c.activeClient.Load() == rpcClient {
			c.handleClientDisconnected()
		}
	})
	rpcClient.SetOnErrorHandler(func(err error) {
		if c.activeClient.Load() == rpcClient {
			c.handleClientError(err)
		}
	})
	rpcRouter, err := buildRPCRouter()
	if err != nil {
		_ = rpcClient.Close()
		return errors.Wrapf(err, "error creating the RPC router")
	}

	c.isConnected.Store(1)
	c.activeClient.Store(rpcClient)
	rpcClient.AttachRouter(rpcRouter.router)

	c.GRPCClient = rpcClient
	c.rpcRouterMutex.Lock()
	c.rpcRouter = rpcRouter
	c.rpcRouterMutex.Unlock()
	c.wakeNotificationDeliveries()

	log.Debugf("Connected to %s", c.rpcAddress)

	originalTimeout := c.getTimeout()
	c.timeoutNanos.Store(int64(initialVersionCheckTimeout))
	getInfoResponse, err := c.GetInfo()
	c.timeoutNanos.Store(int64(originalTimeout))
	if err != nil {
		c.activeClient.CompareAndSwap(rpcClient, nil)
		c.rpcRouterMutex.RLock()
		rpcRouter := c.rpcRouter
		c.rpcRouterMutex.RUnlock()
		if rpcRouter != nil {
			rpcRouter.router.Close()
		}
		closeErr := c.GRPCClient.Close()
		if closeErr != nil {
			log.Warnf("Error closing failed RPC connection to %s: %s", c.rpcAddress, closeErr)
		}
		c.isConnected.Store(0)
		return errors.Wrapf(err, "error validating initial RPC connection to %s", c.rpcAddress)
	}

	localVersion := version.Version()
	remoteVersion := getInfoResponse.ServerVersion

	if localVersion != remoteVersion {
		log.Warnf("version mismatch, client: %s, server: %s - expected responses and requests may deviate", localVersion, remoteVersion)
	}

	return nil
}

func (c *RPCClient) disconnect() error {
	err := c.GRPCClient.Disconnect()
	if err != nil {
		return err
	}
	log.Debugf("Disconnected from %s", c.rpcAddress)
	return nil
}

// reconnectRetryDelay is how long Reconnect waits between connection attempts. It is a variable only so tests can
// shorten it.
var reconnectRetryDelay = 10 * time.Second

// Reconnect forces the client to attempt to reconnect to the address
// this client initially was connected to
func (c *RPCClient) Reconnect() error {
	if c.isClosed.Load() == 1 {
		return errors.Errorf("Cannot reconnect from a closed client")
	}

	// Protect against multiple threads attempting to reconnect at the same time
	swapped := c.isReconnecting.CompareAndSwap(0, 1)
	if !swapped {
		// Already reconnecting
		return nil
	}
	defer c.isReconnecting.Store(0)

	log.Warnf("Attempting to reconnect to %s", c.rpcAddress)

	// Disconnect if we're connected
	if c.isConnected.Load() == 1 {
		err := c.disconnect()
		if err != nil {
			return err
		}
	}

	c.releaseClient()

	// Attempt to connect until we succeed
	for {
		if c.isClosed.Load() == 1 {
			return errors.Errorf("Stopped reconnecting to %s because the client was closed", c.rpcAddress)
		}
		retryDelay := reconnectRetryDelay
		lastDisconnected := c.lastDisconnectedNanos.Load()
		if lastDisconnected == 0 || time.Since(time.Unix(0, lastDisconnected)) > retryDelay {
			err := c.connect()
			if err == nil {
				// Not waited for: a request can wait for the call timeout, and a disconnect meanwhile must be able
				// to start the next reconnect, which isReconnecting refuses until Reconnect returns.
				router := c.currentRPCRouter()
				spawn("reregisterNotifications", func() { c.reregisterNotifications(router) })
				return nil
			}
			log.Warnf("Could not automatically reconnect to %s: %s", c.rpcAddress, err)
			log.Warnf("Retrying in %s", retryDelay)
		}
		time.Sleep(retryDelay)
	}
}

// releaseClient closes the current connection and its router. Reconnecting used to only half-close the
// stream, so every reconnect leaked the gRPC connection with its TCP socket and transport goroutines,
// and the old send loop stayed blocked on the old router. The connection is detached first, so the
// errors its loops report once it is closed are ignored.
func (c *RPCClient) releaseClient() {
	client := c.activeClient.Swap(nil)

	c.rpcRouterMutex.RLock()
	if c.rpcRouter != nil {
		c.rpcRouter.router.Close()
	}
	c.rpcRouterMutex.RUnlock()

	if client != nil {
		err := client.Close()
		if err != nil {
			log.Warnf("Error closing the previous RPC connection to %s: %s", c.rpcAddress, err)
		}
	}
}

func (c *RPCClient) handleClientDisconnected() {
	c.isConnected.Store(0)
	c.rpcRouterMutex.RLock()
	if c.rpcRouter != nil {
		c.rpcRouter.router.Close()
	}
	c.rpcRouterMutex.RUnlock()
	if c.isConnecting.Load() == 1 {
		return
	}
	if c.isClosed.Load() == 0 {
		err := c.disconnect()
		if err != nil {
			panic(err)
		}
		c.lastDisconnectedNanos.Store(time.Now().UnixNano())
		err = c.Reconnect()
		if err != nil {
			// Close() stops a reconnect in progress, and Reconnect reports that as an error. It is the client
			// shutting down, not a failure: this runs on a spawned receive or send loop, where a panic exits the
			// whole process.
			if c.isClosed.Load() == 1 {
				return
			}
			panic(err)
		}
	}
}

func (c *RPCClient) handleClientError(err error) {
	if c.isClosed.Load() == 1 {
		return
	}
	log.Warnf("Received error from client: %s", err)
	c.handleClientDisconnected()
}

// SetTimeout sets the timeout by which to wait for RPC responses
func (c *RPCClient) SetTimeout(timeout time.Duration) {
	c.timeoutNanos.Store(int64(timeout))
}

// Close closes the RPC client
func (c *RPCClient) Close() error {
	swapped := c.isClosed.CompareAndSwap(0, 1)
	if !swapped {
		return errors.Errorf("Cannot close a client that had already been closed")
	}
	c.activeClient.Store(nil)
	c.rpcRouterMutex.RLock()
	if c.rpcRouter != nil {
		c.rpcRouter.router.Close()
	}
	c.rpcRouterMutex.RUnlock()
	c.wakeNotificationDeliveries()
	return c.GRPCClient.Close()
}

// Address returns the address the RPC client connected to
func (c *RPCClient) Address() string {
	return c.rpcAddress
}

func (c *RPCClient) route(command appmessage.MessageCommand) *routerpkg.Route {
	c.rpcRouterMutex.RLock()
	defer c.rpcRouterMutex.RUnlock()
	return c.rpcRouter.routes[command]
}

// currentRPCRouter returns the router of the current connection.
func (c *RPCClient) currentRPCRouter() *rpcRouter {
	c.rpcRouterMutex.RLock()
	defer c.rpcRouterMutex.RUnlock()
	return c.rpcRouter
}

// notificationRegistration returns the kept registration for requestCommand, whose response arrives on
// responseCommand's route.
func (c *RPCClient) notificationRegistration(requestCommand, responseCommand appmessage.MessageCommand,
) *notificationRegistration {
	c.notificationsMutex.Lock()
	defer c.notificationsMutex.Unlock()
	if c.notificationRegistrations == nil {
		c.notificationRegistrations = make(map[appmessage.MessageCommand]*notificationRegistration)
	}
	registration, ok := c.notificationRegistrations[requestCommand]
	if !ok {
		registration = &notificationRegistration{responseCommand: responseCommand}
		c.notificationRegistrations[requestCommand] = registration
	}
	return registration
}

// registerForNotifications sends a Notify request with register and, once the node accepted it, keeps register
// to send again after a reconnect and delivers the notifications in handlers to their handlers. Both replace
// any earlier registration for requestCommand.
func (c *RPCClient) registerForNotifications(requestCommand, responseCommand appmessage.MessageCommand,
	register func(router *rpcRouter) error, handlers notificationHandlers,
) error {
	registration := c.notificationRegistration(requestCommand, responseCommand)
	// Not bounded by the call timeout, see the notificationRegistration type.
	registration.mutex.Lock()
	defer registration.mutex.Unlock()
	return c.registerLocked(registration, c.currentRPCRouter(), register, register, handlers)
}

// registerLocked sends send on router and, once the node accepted it, keeps keep as what a reconnect sends
// again and installs handlers. Must be called with registration.mutex held.
func (c *RPCClient) registerLocked(registration *notificationRegistration, router *rpcRouter,
	send, keep func(router *rpcRouter) error, handlers notificationHandlers,
) error {
	err := c.sendNotificationRequest(registration, router, registration.responseCommand, send)
	if err != nil {
		return err
	}
	registration.register = keep
	registration.registeredOn = router
	c.setNotificationHandlers(handlers)
	return nil
}

// sendNotificationRequest calls send, which sends a request on router and waits for its response on the route
// of responseCommand. The responses still owed there for earlier requests that timed out are dropped first.
// Must be called with registration.mutex held.
func (c *RPCClient) sendNotificationRequest(registration *notificationRegistration, router *rpcRouter,
	responseCommand appmessage.MessageCommand, send func(router *rpcRouter) error,
) error {
	if registration.lateRouter != router {
		registration.lateRouter = router
		registration.lateResponses = make(map[appmessage.MessageCommand]int)
	}
	for registration.lateResponses[responseCommand] > 0 {
		_, err := router.routes[responseCommand].DequeueWithTimeout(c.getTimeout())
		if err != nil {
			return err
		}
		registration.lateResponses[responseCommand]--
	}
	err := send(router)
	if errors.Is(err, routerpkg.ErrTimeout) {
		registration.lateResponses[responseCommand]++
	}
	return err
}

// reregisterNotifications sends the kept notification registrations again on router, the one a reconnect just
// connected, each on its own goroutine so one that waits on the node does not hold up the others. It returns
// once all of them are done.
func (c *RPCClient) reregisterNotifications(router *rpcRouter) {
	c.notificationsMutex.Lock()
	registrations := make(map[appmessage.MessageCommand]*notificationRegistration, len(c.notificationRegistrations))
	for requestCommand, registration := range c.notificationRegistrations {
		registrations[requestCommand] = registration
	}
	c.notificationsMutex.Unlock()

	var waitGroup sync.WaitGroup
	for requestCommand, registration := range registrations {
		waitGroup.Add(1)
		spawn("reregisterNotification-"+requestCommand.String(), func() {
			defer waitGroup.Done()
			c.reregisterNotification(requestCommand, registration, router)
		})
	}
	waitGroup.Wait()
}

// reregisterNotification sends registration again on router. It sends the registration current when it holds
// the registration's mutex, and nothing when it was unregistered, already accepted on router, or router is no
// longer the current one: the reconnect that replaced router sends it on its own router. A timeout, or a full
// outgoing route that the connection outlives, is retried, so the node may get the same Notify request more than
// once; registering is idempotent on the node. Any other error ends it: a refusal is logged and left for the next
// reconnect.
func (c *RPCClient) reregisterNotification(requestCommand appmessage.MessageCommand,
	registration *notificationRegistration, router *rpcRouter,
) {
	for {
		registration.mutex.Lock()
		if registration.register == nil || registration.registeredOn == router ||
			router != c.currentRPCRouter() || c.isClosed.Load() == 1 {
			registration.mutex.Unlock()
			return
		}
		err := c.sendNotificationRequest(registration, router, registration.responseCommand, registration.register)
		if err == nil {
			registration.registeredOn = router
		}
		registration.mutex.Unlock()

		switch {
		case err == nil || errors.Is(err, routerpkg.ErrRouteClosed):
			return
		case errors.Is(err, routerpkg.ErrTimeout), errors.Is(err, routerpkg.ErrRouteCapacityReached):
			log.Warnf("Could not send %s again after reconnecting to %s: %s. Retrying in %s",
				requestCommand, c.rpcAddress, err, reconnectRetryDelay)
			time.Sleep(reconnectRetryDelay)
		default:
			log.Warnf("Could not send %s again after reconnecting to %s: %s", requestCommand, c.rpcAddress, err)
			return
		}
	}
}

// setNotificationHandlers makes handlers the ones notifications of their commands are delivered to, starting
// the delivery worker of a command that has none yet.
func (c *RPCClient) setNotificationHandlers(handlers notificationHandlers) {
	c.notificationsMutex.Lock()
	defer c.notificationsMutex.Unlock()
	if c.notificationDeliveries == nil {
		c.notificationDeliveries = make(map[appmessage.MessageCommand]*notificationDelivery)
	}
	for command, handler := range handlers {
		delivery, ok := c.notificationDeliveries[command]
		if ok {
			delivery.handler = handler
			continue
		}
		delivery = &notificationDelivery{handler: handler, wake: make(chan struct{}, 1)}
		c.notificationDeliveries[command] = delivery
		spawn("deliverNotifications-"+command.String(), func() { c.deliverNotifications(command, delivery) })
	}
}

// deliverNotifications is the one worker delivering the notifications of command, for the life of the client.
// It reads them from the route of the current router, and moves to the next router only once the route it
// reads is closed, which happens before a connection is replaced, and its handler call has returned. So the
// handlers of a command are never called concurrently. Handlers are called without any client lock held and may
// call the client, including registering again. A notification taken from the route before a new handler was
// set may still reach the handler it replaced; every later one reaches the new handler.
func (c *RPCClient) deliverNotifications(command appmessage.MessageCommand, delivery *notificationDelivery) {
	var router *rpcRouter
	for c.isClosed.Load() == 0 {
		current := c.currentRPCRouter()
		if current == router {
			<-delivery.wake
			continue
		}
		router = current
		route := router.routes[command]
		for {
			notification, err := route.Dequeue()
			if err != nil {
				if errors.Is(err, routerpkg.ErrRouteClosed) {
					break
				}
				panic(err)
			}
			c.notificationsMutex.Lock()
			handler := delivery.handler
			c.notificationsMutex.Unlock()
			handler(notification)
		}
	}
}

// wakeNotificationDeliveries wakes the delivery workers waiting for a new router or for the client to close.
func (c *RPCClient) wakeNotificationDeliveries() {
	c.notificationsMutex.Lock()
	defer c.notificationsMutex.Unlock()
	for _, delivery := range c.notificationDeliveries {
		select {
		case delivery.wake <- struct{}{}:
		default:
		}
	}
}

// outgoingRoute returns the active router's outgoing route. Every rpc_*.go request method goes
// through this instead of reading c.rpcRouter directly: connect() replaces c.rpcRouter under
// rpcRouterMutex on every (re)connection, and a request goroutine reading the field unguarded raced
// that write - the same class of bug route() above was already written to avoid.
func (c *RPCClient) outgoingRoute() *routerpkg.Route {
	c.rpcRouterMutex.RLock()
	defer c.rpcRouterMutex.RUnlock()
	return c.rpcRouter.outgoingRoute()
}

// ErrRPC is an error in the RPC protocol
var ErrRPC = errors.New("rpc error")

func (c *RPCClient) convertRPCError(rpcError *appmessage.RPCError) error {
	return errors.Wrap(ErrRPC, rpcError.Message)
}

// SetLogger uses a specified Logger to output package logging info
func (c *RPCClient) SetLogger(backend *logger.Backend, level logger.Level) {
	const logSubsystem = "RPCC"
	log = backend.Logger(logSubsystem)
	log.SetLevel(level)
	spawn = panics.GoroutineWrapperFunc(log)
}
