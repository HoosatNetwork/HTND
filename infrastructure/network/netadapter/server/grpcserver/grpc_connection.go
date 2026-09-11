package grpcserver

import (
	"net"
	"sync"
	"sync/atomic"

	"github.com/HoosatNetwork/HTND/infrastructure/network/netadapter/router"
	"github.com/HoosatNetwork/HTND/infrastructure/network/netadapter/server/grpcserver/protowire"
	"github.com/pkg/errors"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/HoosatNetwork/HTND/infrastructure/network/netadapter/server"
	"google.golang.org/grpc"
)

type gRPCConnection struct {
	server                   *gRPCServer
	address                  *net.TCPAddr
	stream                   grpcStream
	router                   *router.Router
	lowLevelClientConnection *grpc.ClientConn

	// streamLock protects concurrent access to stream.
	// Note that it's an RWMutex. Despite what the name
	// implies, we use it to RLock() send() and receive() because
	// they can work perfectly fine in parallel, and Lock()
	// closeSend() because it must run alone.
	streamLock sync.RWMutex

	stopChan                chan struct{}
	onDisconnectedHandler   server.OnDisconnectedHandler
	onInvalidMessageHandler server.OnInvalidMessageHandler

	isConnected uint32

	// dialAddress is the address an outbound connection was dialed with, exactly as passed to
	// Connect, so a compression fallback is recorded under the key the next dial looks up. Empty for
	// inbound connections. compressed says whether that outbound stream requested gzip.
	dialAddress string
	compressed  bool
}

type grpcStream interface {
	Send(*protowire.HoosatdMessage) error
	Recv() (*protowire.HoosatdMessage, error)
}

func newConnection(server *gRPCServer, address *net.TCPAddr, stream grpcStream,
	lowLevelClientConnection *grpc.ClientConn,
) *gRPCConnection {
	connection := &gRPCConnection{
		server:                   server,
		address:                  address,
		stream:                   stream,
		stopChan:                 make(chan struct{}),
		isConnected:              1,
		lowLevelClientConnection: lowLevelClientConnection,
	}
	return connection
}

func (c *gRPCConnection) Start(router *router.Router) {
	if c.onDisconnectedHandler == nil {
		panic(errors.New("onDisconnectedHandler is nil"))
	}

	c.router = router

	spawn("gRPCConnection.Start-connectionLoops", func() {
		err := c.connectionLoops()
		if err != nil {
			if isCompressionFramingError(err) {
				c.handleCompressionFramingError(err)
				return
			}
			status, isStatus := status.FromError(err)
			if isStatus {
				switch status.Code() {
				case codes.Canceled:
					log.Debugf("Connection canceled for %s: %s", c.address, err)
				case codes.Unavailable:
				case codes.Unknown:
					log.Errorf("Untrusted peer detected for %s, immediately disconnecting: %s", c.address, err)
					c.Disconnect()
					return
				default:
					log.Errorf("Status error from connectionLoops for %s: %s (code: %s, details: %s)",
						c.address, err, status.Code(), status.Message())
				}
			} else {
				log.Debugf("Unknown error from connectionLoops for %s: %s", c.address, err)
			}
		}
	})
}

// handleCompressionFramingError deals with a stream that grpc-go ended because a message's
// compressed flag contradicted the stream's declared encoding. connectionLoops has already
// disconnected; what is left is making the next connection to this peer work.
func (c *gRPCConnection) handleCompressionFramingError(err error) {
	switch {
	case !c.IsOutbound():
		// The peer dialed us and chose the encoding itself; there is nothing to change on this side.
		log.Warnf("Inbound peer %s sent a message whose compression contradicts its stream's declared "+
			"encoding, which ends the stream: %s", c.address, err)
	case c.compressed:
		c.server.compressionFallback.disableFor(c.dialAddress)
		log.Warnf("gzip stream to %s failed on compression framing (%s). Connections to it will not "+
			"request compression for the next %s", c.dialAddress, err, compressionFallbackDuration)
	default:
		// This stream did not request compression, so this node sent nothing compressed and gave the
		// peer no reason to compress. The flagged message was the peer's own.
		log.Warnf("Stream to %s failed on compression framing even though it did not request "+
			"compression (%s) - that peer sends malformed messages regardless of what this node asks for",
			c.dialAddress, err)
	}
}

func (c *gRPCConnection) String() string {
	return c.Address().String()
}

func (c *gRPCConnection) IsConnected() bool {
	return atomic.LoadUint32(&c.isConnected) != 0
}

func (c *gRPCConnection) SetOnDisconnectedHandler(onDisconnectedHandler server.OnDisconnectedHandler) {
	c.onDisconnectedHandler = onDisconnectedHandler
}

func (c *gRPCConnection) SetOnInvalidMessageHandler(onInvalidMessageHandler server.OnInvalidMessageHandler) {
	c.onInvalidMessageHandler = onInvalidMessageHandler
}

func (c *gRPCConnection) IsOutbound() bool {
	return c.lowLevelClientConnection != nil
}

// Disconnect disconnects the connection
// Calling this function a second time doesn't do anything
//
// This is part of the Connection interface
func (c *gRPCConnection) Disconnect() {
	// Multiple goroutines can race to disconnect (receive loop, send loop, higher layers).
	// Ensure we only run the disconnect sequence once.
	if !atomic.CompareAndSwapUint32(&c.isConnected, 1, 0) {
		return
	}

	close(c.stopChan)

	if c.IsOutbound() {
		c.closeSend()
		log.Debugf("Disconnected from %s", c)
	}

	log.Debugf("Disconnecting from %s", c)
	if c.onDisconnectedHandler != nil {
		c.onDisconnectedHandler()
	}
}

func (c *gRPCConnection) Address() *net.TCPAddr {
	return c.address
}

func (c *gRPCConnection) receive() (*protowire.HoosatdMessage, error) {
	// We use RLock here and in send() because they can work
	// in parallel. closeSend(), however, must not have either
	// receive() nor send() running while it's running.
	c.streamLock.RLock()
	defer c.streamLock.RUnlock()

	if c.stream == nil {
		return nil, errors.New("grpc stream is nil -receive- (connection closed)")
	}
	return c.stream.Recv()
}

func (c *gRPCConnection) send(message *protowire.HoosatdMessage) error {
	// We use RLock here and in receive() because they can work
	// in parallel. closeSend(), however, must not have either
	// receive() nor send() running while it's running.
	c.streamLock.RLock()
	defer c.streamLock.RUnlock()

	if c.stream == nil {
		return errors.New("grpc stream is nil -send- (connection closed)")
	}
	return c.stream.Send(message)
}

func (c *gRPCConnection) closeSend() {
	c.streamLock.Lock()
	defer c.streamLock.Unlock()

	if c.stream == nil {
		return
	}

	clientStream, ok := c.stream.(grpc.ClientStream)
	if ok {
		// ignore error because we don't really know what's the status of the connection
		_ = clientStream.CloseSend()
	}

	if c.lowLevelClientConnection != nil {
		_ = c.lowLevelClientConnection.Close()
	}
}
