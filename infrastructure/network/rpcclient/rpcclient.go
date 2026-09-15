package rpcclient

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/HoosatNetwork/HTND/app/appmessage"
	"github.com/HoosatNetwork/HTND/infrastructure/logger"
	routerpkg "github.com/HoosatNetwork/HTND/infrastructure/network/netadapter/router"
	"github.com/HoosatNetwork/HTND/infrastructure/network/rpcclient/grpcclient"
	"github.com/HoosatNetwork/HTND/util/panics"
	"github.com/HoosatNetwork/HTND/version"
	"github.com/pkg/errors"
)

const (
	defaultTimeout             = 10 * time.Minute
	initialVersionCheckTimeout = 5 * time.Second
)

// RPCClient is an RPC client
type RPCClient struct {
	*grpcclient.GRPCClient

	rpcAddress           string
	rpcRouter            *rpcRouter
	rpcRouterMutex       sync.RWMutex
	isConnected          atomic.Uint32
	isClosed             atomic.Uint32
	isReconnecting       atomic.Uint32
	lastDisconnectedTime time.Time
	isConnecting         atomic.Uint32

	// activeClient is the connection whose callbacks are acted on. A replaced connection's loops keep
	// reporting errors after it is closed, and those must not tear down the connection that replaced it.
	activeClient atomic.Pointer[grpcclient.GRPCClient]

	timeout time.Duration
}

// NewRPCClient сreates a new RPC client with a default call timeout value
func NewRPCClient(rpcAddress string) (*RPCClient, error) {
	rpcClient := &RPCClient{
		rpcAddress: rpcAddress,
		timeout:    defaultTimeout,
	}
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

	log.Debugf("Connected to %s", c.rpcAddress)

	originalTimeout := c.timeout
	c.timeout = initialVersionCheckTimeout
	getInfoResponse, err := c.GetInfo()
	c.timeout = originalTimeout
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
		if time.Since(c.lastDisconnectedTime) > retryDelay {
			err := c.connect()
			if err == nil {
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
		c.lastDisconnectedTime = time.Now()
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
	c.timeout = timeout
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
