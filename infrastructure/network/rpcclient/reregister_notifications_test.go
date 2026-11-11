package rpcclient

import (
	"net"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
	routerpkg "github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter/router"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter/server/grpcserver/protowire"
	"github.com/HoosatNetwork/HTND/v2/version"
	"github.com/pkg/errors"
	"google.golang.org/grpc"
)

// notificationRPCServer answers GetInfo and NotifyNewBlockTemplate requests, and hands each client stream to the
// test as a notificationConnection.
type notificationRPCServer struct {
	protowire.UnimplementedRPCServer
	connections chan *notificationConnection
}

// notificationConnection is one client stream. registered receives once per NotifyNewBlockTemplate request answered,
// notify sends the client a NewBlockTemplate notification and closing end closes the stream.
type notificationConnection struct {
	registered chan struct{}
	notify     chan struct{}
	end        chan struct{}
}

func (s *notificationRPCServer) MessageStream(stream protowire.RPC_MessageStreamServer) error {
	connection := &notificationConnection{
		registered: make(chan struct{}, 10),
		notify:     make(chan struct{}),
		end:        make(chan struct{}),
	}
	s.connections <- connection

	requests := make(chan appmessage.Message)
	go func() {
		defer close(requests)
		for {
			request, err := stream.Recv()
			if err != nil {
				return
			}
			appMessage, err := request.ToAppMessage()
			if err != nil {
				return
			}
			select {
			case requests <- appMessage:
			case <-stream.Context().Done():
				return
			}
		}
	}()

	for {
		var response appmessage.Message
		isNotifyResponse := false
		select {
		case <-connection.end:
			return nil
		case <-connection.notify:
			response = appmessage.NewNewBlockTemplateNotificationMessage()
		case request, ok := <-requests:
			if !ok {
				return nil
			}
			switch request.(type) {
			case *appmessage.GetInfoRequestMessage:
				response = appmessage.NewGetInfoResponseMessage("", 0, version.Version(), false, true, true, 0, 0, "")
			case *appmessage.NotifyNewBlockTemplateRequestMessage:
				response = appmessage.NewNotifyNewBlockTemplateResponseMessage()
				isNotifyResponse = true
			default:
				continue
			}
		}
		protoResponse, err := protowire.FromAppMessage(response)
		if err != nil {
			return err
		}
		if err := stream.Send(protoResponse); err != nil {
			return nil
		}
		if isNotifyResponse {
			connection.registered <- struct{}{}
		}
	}
}

func startNotificationRPCServer(t *testing.T) (*notificationRPCServer, string) {
	originalRetryDelay := reconnectRetryDelay
	reconnectRetryDelay = 20 * time.Millisecond
	t.Cleanup(func() { reconnectRetryDelay = originalRetryDelay })

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %+v", err)
	}
	notificationServer := &notificationRPCServer{connections: make(chan *notificationConnection, 10)}
	server := grpc.NewServer()
	protowire.RegisterRPCServer(server, notificationServer)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	return notificationServer, listener.Addr().String()
}

func receive[T any](t *testing.T, what string, channel <-chan T) T {
	select {
	case value := <-channel:
		return value
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		panic("unreachable")
	}
}

// expectNotificationDelivery sends one notification on connection and checks that afterwards current has seen
// want notifications and replaced none.
func expectNotificationDelivery(t *testing.T, connection *notificationConnection, current, replaced *atomic.Int32, want int32) {
	select {
	case connection.notify <- struct{}{}:
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out sending a notification")
	}
	waitFor(t, "the notification to be handled", func() bool { return current.Load()+replaced.Load() >= want })
	// Leave time for a duplicate delivery to show up.
	time.Sleep(300 * time.Millisecond)
	if got := current.Load(); got != want {
		t.Fatalf("handler saw %d notifications, want %d", got, want)
	}
	if got := replaced.Load(); got != 0 {
		t.Fatalf("a replaced handler saw %d notifications, want 0", got)
	}
}

// TestReconnectReregistersNotifications pins that a notification registration survives a reconnect. The node keeps
// registrations per connection, and the client never sent them again on the connection a reconnect opened, so after
// a node restart a stratum bridge or miner stopped getting new block templates without any error.
func TestReconnectReregistersNotifications(t *testing.T) {
	server, address := startNotificationRPCServer(t)

	client, err := NewRPCClient(address)
	if err != nil {
		t.Fatalf("NewRPCClient: %+v", err)
	}
	defer client.Close()
	client.SetTimeout(5 * time.Second)
	first := receive(t, "the first connection", server.connections)

	var handled, replaced atomic.Int32
	err = client.RegisterForNewBlockTemplateNotifications(func(*appmessage.NewBlockTemplateNotificationMessage) {
		handled.Add(1)
	})
	if err != nil {
		t.Fatalf("RegisterForNewBlockTemplateNotifications: %+v", err)
	}
	receive(t, "the registration", first.registered)
	expectNotificationDelivery(t, first, &handled, &replaced, 1)

	// The node goes away: the client reconnects on its own.
	close(first.end)
	second := receive(t, "the client to reconnect", server.connections)
	receive(t, "the registration to be sent again on the new connection", second.registered)
	expectNotificationDelivery(t, second, &handled, &replaced, 2)
	if extra := len(second.registered); extra != 0 {
		t.Fatalf("the registration was sent %d extra times on the new connection", extra)
	}
}

// TestRegisteringNotificationsAgainReplacesTheHandler pins that registering for a notification type again replaces
// the earlier registration instead of adding to it, before and after a reconnect: each notification reaches only
// the latest handler, once, and a reconnect sends the registration once.
func TestRegisteringNotificationsAgainReplacesTheHandler(t *testing.T) {
	server, address := startNotificationRPCServer(t)

	client, err := NewRPCClient(address)
	if err != nil {
		t.Fatalf("NewRPCClient: %+v", err)
	}
	defer client.Close()
	client.SetTimeout(5 * time.Second)
	first := receive(t, "the first connection", server.connections)

	var replaced, current atomic.Int32
	for _, counter := range []*atomic.Int32{&replaced, &current} {
		err = client.RegisterForNewBlockTemplateNotifications(func(*appmessage.NewBlockTemplateNotificationMessage) {
			counter.Add(1)
		})
		if err != nil {
			t.Fatalf("RegisterForNewBlockTemplateNotifications: %+v", err)
		}
		receive(t, "the registration", first.registered)
	}
	for want := int32(1); want <= 3; want++ {
		expectNotificationDelivery(t, first, &current, &replaced, want)
	}

	close(first.end)
	second := receive(t, "the client to reconnect", server.connections)
	receive(t, "the registration to be sent again on the new connection", second.registered)
	for want := int32(4); want <= 6; want++ {
		expectNotificationDelivery(t, second, &current, &replaced, want)
	}
	if extra := len(second.registered); extra != 0 {
		t.Fatalf("the registration was sent %d extra times on the new connection", extra)
	}
}

// The tests below drive the client's notification internals on routers without a connection, to order
// registration, a reconnect's replay and unregistration deterministically.

// TestReplayDoesNotOverwriteNewerRegistration pins that a reconnect sending a registration again cannot undo a
// newer registration made meanwhile. The replay used to send a copy of the registration taken before it waited,
// so it could send it after the newer one, and the node kept the older setting.
func TestReplayDoesNotOverwriteNewerRegistration(t *testing.T) {
	c, router := newTestNotificationClient(t)
	requestCommand := appmessage.CmdNotifyVirtualSelectedParentChainChangedRequestMessage
	responseCommand := appmessage.CmdNotifyVirtualSelectedParentChainChangedResponseMessage
	var nodeSetting atomic.Int32
	entered, resume := make(chan struct{}), make(chan struct{})
	keepTestRegistration(c, requestCommand, responseCommand, func(*rpcRouter) error {
		close(entered)
		<-resume
		nodeSetting.Store(1)
		return nil
	})

	replayed := make(chan struct{})
	go func() { c.reregisterNotifications(router); close(replayed) }()
	receive(t, "the replay to start sending", entered)
	registered := make(chan error, 1)
	go func() {
		registered <- c.registerForNotifications(requestCommand, responseCommand, func(*rpcRouter) error {
			nodeSetting.Store(2)
			return nil
		}, nil)
	}()
	// Let the newer registration finish first if it can.
	select {
	case err := <-registered:
		registered <- err
	case <-time.After(200 * time.Millisecond):
	}
	close(resume)
	receive(t, "the replay", replayed)
	if err := receive(t, "the registration", registered); err != nil {
		t.Fatalf("registerForNotifications: %+v", err)
	}
	if got := nodeSetting.Load(); got != 2 {
		t.Fatalf("the replay replaced the newer registration: the node has setting %d, want 2", got)
	}
}

// TestObsoleteReplayDoesNotStartSecondDelivery pins that the replay of a connection that was already replaced
// cannot leave two deliveries running for one notification command. It used to take over the command's loop
// ownership for its closed router, so the next registration started a second loop on the current router.
func TestObsoleteReplayDoesNotStartSecondDelivery(t *testing.T) {
	c, current := newTestNotificationClient(t)
	old := newTestRPCRouter(t)
	old.router.Close()
	command := appmessage.CmdNewBlockTemplateNotificationMessage
	entered, release := make(chan struct{}, 2), make(chan struct{})
	defer close(release)
	register := func() {
		err := c.registerForNotifications(appmessage.CmdNotifyNewBlockTemplateRequestMessage,
			appmessage.CmdNotifyNewBlockTemplateResponseMessage, func(*rpcRouter) error { return nil },
			notificationHandlers{command: func(appmessage.Message) { entered <- struct{}{}; <-release }})
		if err != nil {
			t.Fatalf("registerForNotifications: %+v", err)
		}
	}

	register()
	c.reregisterNotifications(current)
	// The replay of an earlier reconnect, scheduled only now.
	c.reregisterNotifications(old)
	register()
	for i := 0; i < 2; i++ {
		if err := current.routes[command].Enqueue(appmessage.NewNewBlockTemplateNotificationMessage()); err != nil {
			t.Fatalf("Enqueue: %+v", err)
		}
	}
	receive(t, "the first notification", entered)
	select {
	case <-entered:
		t.Fatalf("two deliveries of one notification command called the handler at once")
	case <-time.After(100 * time.Millisecond):
	}
}

// TestSlowHandlerIsNotCalledConcurrentlyAcrossReconnect pins that a handler still running when its connection is
// replaced is not called again for the new connection until it returns. A reconnect used to start a new loop
// right away, beside the one blocked in the handler.
func TestSlowHandlerIsNotCalledConcurrentlyAcrossReconnect(t *testing.T) {
	c, old := newTestNotificationClient(t)
	current := newTestRPCRouter(t)
	command := appmessage.CmdNewBlockTemplateNotificationMessage
	entered, release := make(chan struct{}, 2), make(chan struct{})
	err := c.registerForNotifications(appmessage.CmdNotifyNewBlockTemplateRequestMessage,
		appmessage.CmdNotifyNewBlockTemplateResponseMessage, func(*rpcRouter) error { return nil },
		notificationHandlers{command: func(appmessage.Message) { entered <- struct{}{}; <-release }})
	if err != nil {
		t.Fatalf("registerForNotifications: %+v", err)
	}
	if err := old.routes[command].Enqueue(appmessage.NewNewBlockTemplateNotificationMessage()); err != nil {
		t.Fatalf("Enqueue: %+v", err)
	}
	receive(t, "the handler to be called", entered)

	old.router.Close()
	switchTestRouter(c, current)
	c.reregisterNotifications(current)
	if err := current.routes[command].Enqueue(appmessage.NewNewBlockTemplateNotificationMessage()); err != nil {
		t.Fatalf("Enqueue: %+v", err)
	}
	select {
	case <-entered:
		t.Fatalf("the handler was called for the new connection while still running for the old one")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	receive(t, "the notification from the new connection", entered)
}

// TestRegistrationCopiesAddresses pins that the addresses a reconnect registers again are the ones passed, not
// whatever the caller's slice holds by then. The kept registration used to keep the caller's slice.
func TestRegistrationCopiesAddresses(t *testing.T) {
	c, router := newTestNotificationClient(t)
	addresses := []string{"original"}
	expectUTXOsChangedRegistration(t, c, router, addresses, []string{"original"})
	addresses[0] = "mutated after return"

	next := newTestRPCRouter(t)
	router.router.Close()
	switchTestRouter(c, next)
	expectUTXOsChangedReplay(t, c, next, []string{"original"})
}

// TestReconnectRegistersAllUTXOsChangedAddresses pins that a reconnect registers the addresses of every
// UTXOsChanged registration, as the node had them: it adds each call's addresses to the earlier ones. Only the
// latest call's addresses used to be sent again. On the connection that has the earlier addresses only the new
// ones are sent.
func TestReconnectRegistersAllUTXOsChangedAddresses(t *testing.T) {
	c, router := newTestNotificationClient(t)
	expectUTXOsChangedRegistration(t, c, router, []string{"a"}, []string{"a"})
	expectUTXOsChangedRegistration(t, c, router, []string{"b"}, []string{"b"})

	next := newTestRPCRouter(t)
	router.router.Close()
	switchTestRouter(c, next)
	expectUTXOsChangedReplay(t, c, next, []string{"a", "b"})
}

// TestReplayDoesNotUndoUnregister pins that a reconnect sending the pruning point UTXO set registration again
// cannot send it after UnregisterPruningPointUTXOSetNotifications succeeded, which left the node notifying.
func TestReplayDoesNotUndoUnregister(t *testing.T) {
	c, router := newTestNotificationClient(t)
	entered, resume := make(chan struct{}), make(chan struct{})
	keepTestRegistration(c, appmessage.CmdNotifyPruningPointUTXOSetOverrideRequestMessage,
		appmessage.CmdNotifyPruningPointUTXOSetOverrideResponseMessage, func(router *rpcRouter) error {
			close(entered)
			<-resume
			return router.outgoingRoute().Enqueue(appmessage.NewNotifyPruningPointUTXOSetOverrideRequestMessage())
		})

	replayed := make(chan struct{})
	go func() { c.reregisterNotifications(router); close(replayed) }()
	receive(t, "the replay to start sending", entered)
	err := router.routes[appmessage.CmdStopNotifyingPruningPointUTXOSetOverrideResponseMessage].Enqueue(
		appmessage.NewStopNotifyingPruningPointUTXOSetOverrideResponseMessage())
	if err != nil {
		t.Fatalf("Enqueue: %+v", err)
	}
	unregistered := make(chan error, 1)
	go func() { unregistered <- c.UnregisterPruningPointUTXOSetNotifications() }()
	// Let the unregistration finish first if it can.
	select {
	case err := <-unregistered:
		unregistered <- err
	case <-time.After(200 * time.Millisecond):
	}
	close(resume)
	receive(t, "the replay", replayed)
	if err := receive(t, "the unregistration", unregistered); err != nil {
		t.Fatalf("UnregisterPruningPointUTXOSetNotifications: %+v", err)
	}

	var last appmessage.MessageCommand
	for router.outgoingRoute().Length() > 0 {
		request, err := router.outgoingRoute().Dequeue()
		if err != nil {
			t.Fatalf("Dequeue: %+v", err)
		}
		last = request.Command()
	}
	if last != appmessage.CmdStopNotifyingPruningPointUTXOSetOverrideRequestMessage {
		t.Fatalf("the last request sent was %s, want the unregistration", last)
	}
}

// TestTimedOutRegistrationDoesNotStarveOthers pins that a registration the node does not answer after a
// reconnect does not keep the others from being sent. The replay used to retry it forever before moving on.
func TestTimedOutRegistrationDoesNotStarveOthers(t *testing.T) {
	c, router := newTestNotificationClient(t)
	c.SetTimeout(5 * time.Millisecond)
	originalRetryDelay := reconnectRetryDelay
	reconnectRetryDelay = time.Millisecond
	t.Cleanup(func() { reconnectRetryDelay = originalRetryDelay })
	var blockTemplateAttempts, blockAddedAttempts atomic.Int32
	keepTestRegistration(c, appmessage.CmdNotifyNewBlockTemplateRequestMessage,
		appmessage.CmdNotifyNewBlockTemplateResponseMessage, func(*rpcRouter) error {
			blockTemplateAttempts.Add(1)
			return errors.WithStack(routerpkg.ErrTimeout)
		})
	keepTestRegistration(c, appmessage.CmdNotifyBlockAddedRequestMessage,
		appmessage.CmdNotifyBlockAddedResponseMessage, func(*rpcRouter) error {
			blockAddedAttempts.Add(1)
			return errors.WithStack(routerpkg.ErrTimeout)
		})

	replayed := make(chan struct{})
	go func() { c.reregisterNotifications(router); close(replayed) }()
	time.Sleep(100 * time.Millisecond)
	c.isClosed.Store(1)
	receive(t, "the replay to stop once the client is closed", replayed)
	if blockTemplateAttempts.Load() == 0 || blockAddedAttempts.Load() == 0 {
		t.Fatalf("a timed-out registration starved another: %d and %d attempts",
			blockTemplateAttempts.Load(), blockAddedAttempts.Load())
	}
}

// TestReplayRetriesWhenOutgoingRouteIsFull pins that a reconnect sends a registration again once a full outgoing
// route has drained. A full route fails the enqueue without closing the connection, so giving up left the open
// connection without the registration until the next reconnect.
func TestReplayRetriesWhenOutgoingRouteIsFull(t *testing.T) {
	c, old := newTestNotificationClient(t)
	originalRetryDelay := reconnectRetryDelay
	reconnectRetryDelay = time.Millisecond
	t.Cleanup(func() { reconnectRetryDelay = originalRetryDelay })
	t.Cleanup(func() { c.isClosed.Store(1); c.wakeNotificationDeliveries() })
	err := old.routes[appmessage.CmdNotifyNewBlockTemplateResponseMessage].Enqueue(appmessage.NewNotifyNewBlockTemplateResponseMessage())
	if err != nil {
		t.Fatalf("Enqueue: %+v", err)
	}
	err = c.RegisterForNewBlockTemplateNotifications(func(*appmessage.NewBlockTemplateNotificationMessage) {})
	if err != nil {
		t.Fatalf("RegisterForNewBlockTemplateNotifications: %+v", err)
	}

	next := newTestRPCRouter(t)
	old.router.Close()
	switchTestRouter(c, next)
	outgoing := next.outgoingRoute()
	for i := 0; i < outgoing.Capacity(); i++ {
		if err := outgoing.Enqueue(appmessage.NewGetInfoRequestMessage()); err != nil {
			t.Fatalf("Enqueue: %+v", err)
		}
	}
	full := make(chan struct{}, 1)
	outgoing.SetOnCapacityReachedHandler(func(*routerpkg.Route, appmessage.Message) {
		select {
		case full <- struct{}{}:
		default:
		}
	})
	err = next.routes[appmessage.CmdNotifyNewBlockTemplateResponseMessage].Enqueue(appmessage.NewNotifyNewBlockTemplateResponseMessage())
	if err != nil {
		t.Fatalf("Enqueue: %+v", err)
	}
	replayed := make(chan struct{})
	go func() { c.reregisterNotifications(next); close(replayed) }()
	receive(t, "the replay to find the outgoing route full", full)

	// Drain the route, still open, until the registration shows up.
	for {
		request, err := outgoing.DequeueWithTimeout(time.Second)
		if err != nil {
			t.Fatalf("the registration was not sent again once the outgoing route drained: %+v", err)
		}
		if request.Command() == appmessage.CmdNotifyNewBlockTemplateRequestMessage {
			break
		}
	}
	receive(t, "the replay", replayed)
}

// TestLateResponseIsNotTakenByNextRegistration pins that the response to a registration that timed out is not
// taken as the response to the next registration of the same notifications.
func TestLateResponseIsNotTakenByNextRegistration(t *testing.T) {
	c, router := newTestNotificationClient(t)
	c.SetTimeout(50 * time.Millisecond)
	onChainChanged := func(*appmessage.VirtualSelectedParentChainChangedNotificationMessage) {}
	err := c.RegisterForVirtualSelectedParentChainChangedNotifications(true, onChainChanged)
	if !errors.Is(err, routerpkg.ErrTimeout) {
		t.Fatalf("got %v, want a timeout", err)
	}

	responses := router.routes[appmessage.CmdNotifyVirtualSelectedParentChainChangedResponseMessage]
	late := appmessage.NewNotifyVirtualSelectedParentChainChangedResponseMessage()
	late.Error = appmessage.RPCErrorf("the response to the request that timed out")
	for _, response := range []appmessage.Message{late, appmessage.NewNotifyVirtualSelectedParentChainChangedResponseMessage()} {
		if err := responses.Enqueue(response); err != nil {
			t.Fatalf("Enqueue: %+v", err)
		}
	}
	c.SetTimeout(time.Second)
	err = c.RegisterForVirtualSelectedParentChainChangedNotifications(false, onChainChanged)
	if err != nil {
		t.Fatalf("the registration took the response of the one that timed out: %v", err)
	}
}

// TestHandlerCanRegisterAgain pins that handlers are called without a client lock held: one registering again
// from inside the handler does not deadlock.
func TestHandlerCanRegisterAgain(t *testing.T) {
	c, router := newTestNotificationClient(t)
	responses := router.routes[appmessage.CmdNotifyNewBlockTemplateResponseMessage]
	for i := 0; i < 2; i++ {
		if err := responses.Enqueue(appmessage.NewNotifyNewBlockTemplateResponseMessage()); err != nil {
			t.Fatalf("Enqueue: %+v", err)
		}
	}
	registeredAgain := make(chan error, 1)
	err := c.RegisterForNewBlockTemplateNotifications(func(*appmessage.NewBlockTemplateNotificationMessage) {
		registeredAgain <- c.RegisterForNewBlockTemplateNotifications(func(*appmessage.NewBlockTemplateNotificationMessage) {})
	})
	if err != nil {
		t.Fatalf("RegisterForNewBlockTemplateNotifications: %+v", err)
	}
	err = router.routes[appmessage.CmdNewBlockTemplateNotificationMessage].Enqueue(appmessage.NewNewBlockTemplateNotificationMessage())
	if err != nil {
		t.Fatalf("Enqueue: %+v", err)
	}
	if err := receive(t, "the handler to register again", registeredAgain); err != nil {
		t.Fatalf("RegisterForNewBlockTemplateNotifications from the handler: %+v", err)
	}
}

// expectUTXOsChangedRegistration registers for UTXOsChanged notifications with addresses on router and checks
// that the request sent has want.
func expectUTXOsChangedRegistration(t *testing.T, c *RPCClient, router *rpcRouter, addresses, want []string) {
	err := router.routes[appmessage.CmdNotifyUTXOsChangedResponseMessage].Enqueue(appmessage.NewNotifyUTXOsChangedResponseMessage())
	if err != nil {
		t.Fatalf("Enqueue: %+v", err)
	}
	err = c.RegisterForUTXOsChangedNotifications(addresses, func(*appmessage.UTXOsChangedNotificationMessage) {})
	if err != nil {
		t.Fatalf("RegisterForUTXOsChangedNotifications: %+v", err)
	}
	expectUTXOsChangedRequest(t, router, want)
}

// expectUTXOsChangedReplay sends the kept registrations again on router and checks that the UTXOsChanged request
// sent has want.
func expectUTXOsChangedReplay(t *testing.T, c *RPCClient, router *rpcRouter, want []string) {
	err := router.routes[appmessage.CmdNotifyUTXOsChangedResponseMessage].Enqueue(appmessage.NewNotifyUTXOsChangedResponseMessage())
	if err != nil {
		t.Fatalf("Enqueue: %+v", err)
	}
	c.reregisterNotifications(router)
	expectUTXOsChangedRequest(t, router, want)
}

func expectUTXOsChangedRequest(t *testing.T, router *rpcRouter, want []string) {
	request, err := router.outgoingRoute().DequeueWithTimeout(time.Second)
	if err != nil {
		t.Fatalf("Dequeue: %+v", err)
	}
	// The node keeps a set, so the order does not matter.
	got := slices.Sorted(slices.Values(request.(*appmessage.NotifyUTXOsChangedRequestMessage).Addresses))
	if !slices.Equal(got, slices.Sorted(slices.Values(want))) {
		t.Fatalf("the request has addresses %q, want %q", got, want)
	}
}

// newTestRPCRouter returns a router that is closed when the test ends.
func newTestRPCRouter(t *testing.T) *rpcRouter {
	router, err := buildRPCRouter()
	if err != nil {
		t.Fatalf("buildRPCRouter: %+v", err)
	}
	t.Cleanup(router.router.Close)
	return router
}

// newTestNotificationClient returns a client without a connection, whose current router is the one returned.
func newTestNotificationClient(t *testing.T) (*RPCClient, *rpcRouter) {
	router := newTestRPCRouter(t)
	c := &RPCClient{rpcRouter: router}
	c.SetTimeout(time.Second)
	return c, router
}

// keepTestRegistration keeps register as a registration accepted on an earlier connection.
func keepTestRegistration(c *RPCClient, requestCommand, responseCommand appmessage.MessageCommand,
	register func(router *rpcRouter) error,
) {
	c.notificationRegistration(requestCommand, responseCommand).register = register
}

// switchTestRouter makes router the current one, as connect does.
func switchTestRouter(c *RPCClient, router *rpcRouter) {
	c.rpcRouterMutex.Lock()
	c.rpcRouter = router
	c.rpcRouterMutex.Unlock()
	c.wakeNotificationDeliveries()
}
