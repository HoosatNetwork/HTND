package grpcclient

import (
	"context"
	"io"
	"time"

	"github.com/Hoosat-Oy/HTND/app/appmessage"
	"github.com/Hoosat-Oy/HTND/infrastructure/network/netadapter/router"
	"github.com/Hoosat-Oy/HTND/infrastructure/network/netadapter/server/grpcserver"
	"github.com/Hoosat-Oy/HTND/infrastructure/network/netadapter/server/grpcserver/protowire"
	"github.com/pkg/errors"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
)

const defaultStreamSetupTimeout = 30 * time.Second

// OnErrorHandler defines a handler function for when errors occur
type OnErrorHandler func(err error)

// OnDisconnectedHandler defines a handler function for when the client disconnected
type OnDisconnectedHandler func()

// GRPCClient is a gRPC-based RPC client
type GRPCClient struct {
	stream                protowire.RPC_MessageStreamClient
	connection            *grpc.ClientConn
	onErrorHandler        OnErrorHandler
	onDisconnectedHandler OnDisconnectedHandler
}

// Connect connects to the RPC server with the given address
func Connect(address string) (*GRPCClient, error) {
	gRPCConnection, err := grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, errors.Wrapf(err, "error connecting to %s", address)
	}
	err = waitForConnectionReady(gRPCConnection, defaultStreamSetupTimeout)
	if err != nil {
		_ = gRPCConnection.Close()
		return nil, errors.Wrapf(err, "error waiting for connection to %s", address)
	}

	grpcClient := protowire.NewRPCClient(gRPCConnection)
	stream, err := grpcClient.MessageStream(context.Background(),
		grpc.MaxCallRecvMsgSize(grpcserver.RPCMaxMessageSize), grpc.MaxCallSendMsgSize(grpcserver.RPCMaxMessageSize))
	if err != nil {
		_ = gRPCConnection.Close()
		return nil, errors.Wrapf(err, "error getting client stream for %s", address)
	}
	return &GRPCClient{stream: stream, connection: gRPCConnection}, nil
}

func waitForConnectionReady(connection *grpc.ClientConn, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	for {
		state := connection.GetState()
		switch state {
		case connectivity.Ready:
			return nil
		case connectivity.Shutdown:
			return errors.New("gRPC connection shut down before becoming ready")
		case connectivity.Idle:
			connection.Connect()
		}

		if !connection.WaitForStateChange(ctx, state) {
			return errors.Errorf("timed out waiting for gRPC connection readiness after %s", timeout)
		}
	}
}

// Close closes the underlying grpc connection
func (c *GRPCClient) Close() error {
	return c.connection.Close()
}

// Disconnect disconnects from the RPC server
func (c *GRPCClient) Disconnect() error {
	return c.stream.CloseSend()
}

// SetOnErrorHandler sets the client's onErrorHandler
func (c *GRPCClient) SetOnErrorHandler(onErrorHandler OnErrorHandler) {
	c.onErrorHandler = onErrorHandler
}

// SetOnDisconnectedHandler sets the client's onDisconnectedHandler
func (c *GRPCClient) SetOnDisconnectedHandler(onDisconnectedHandler OnDisconnectedHandler) {
	c.onDisconnectedHandler = onDisconnectedHandler
}

// AttachRouter attaches the given router to the client and starts
// sending/receiving messages via it
func (c *GRPCClient) AttachRouter(router *router.Router) {
	spawn("GRPCClient.AttachRouter-sendLoop", func() {
		for {
			message, err := router.OutgoingRoute().Dequeue()
			if err != nil {
				c.handleError(err)
				return
			}
			err = c.send(message)
			if err != nil {
				c.handleError(err)
				return
			}
		}
	})
	spawn("GRPCClient.AttachRouter-receiveLoop", func() {
		for {
			message, err := c.receive()
			if err != nil {
				c.handleError(err)
				return
			}
			err = router.EnqueueIncomingMessage(message)
			if err != nil {
				c.handleError(err)
				return
			}
		}
	})
}

func (c *GRPCClient) send(requestAppMessage appmessage.Message) error {
	request, err := protowire.FromAppMessage(requestAppMessage)
	if err != nil {
		return errors.Wrapf(err, "error converting the request")
	}
	return c.stream.Send(request)
}

func (c *GRPCClient) receive() (appmessage.Message, error) {
	response, err := c.stream.Recv()
	if err != nil {
		return nil, err
	}
	return response.ToAppMessage()
}

func (c *GRPCClient) handleError(err error) {
	if errors.Is(err, io.EOF) {
		if c.onDisconnectedHandler != nil {
			c.onDisconnectedHandler()
		}
		return
	}
	if errors.Is(err, router.ErrRouteClosed) {
		err := c.Disconnect()
		if err != nil {
			panic(err)
		}
		return
	}
	if c.onErrorHandler != nil {
		c.onErrorHandler(err)
		return
	}
	panic(err)
}
