package grpcserver

import (
	"context"
	"net"
	"time"

	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter/server"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter/server/grpcserver/protowire"
	"github.com/HoosatNetwork/HTND/v2/util/panics"
	"github.com/pkg/errors"
	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/encoding/gzip"
	"google.golang.org/grpc/peer"
)

type p2pServer struct {
	protowire.UnimplementedP2PServer
	gRPCServer
}

// DefaultP2PMaxMessageSize is the built-in P2P message ceiling: 4GiB, raised from 1GiB by
// cff75920a.
//
// HTN-166: this is very large, and an unauthenticated peer can make this node buffer up to it. The
// default is deliberately NOT changed here. A P2P ceiling that is too low rejects legitimate large
// IBD messages and partitions this node from the network, and nobody has measured what the real
// maximum legitimate message is on this chain - so any smaller number chosen here would be a guess
// with a network-split failure mode. What this does instead is make the ceiling settable, so an
// operator who HAS measured their traffic can lower it without rebuilding, and so the value is
// visible in the startup log rather than buried in a constant.
const DefaultP2PMaxMessageSize = 1024 * 1024 * 1024 * 4

// p2pMaxMessageSize is the effective ceiling. Set once at startup via SetMaxMessageSizes, before
// any server is created.
var p2pMaxMessageSize = DefaultP2PMaxMessageSize

// p2pMaxInboundConnections is the max amount of inbound connections for the P2P server.
// Note that inbound connections are not limited by the gRPC server. (A value of 0 means
// unlimited inbound connections.) The P2P limiting logic is more applicative, and as such
// is handled in the ConnectionManager instead.
const p2pMaxInboundConnections = 0

// NewP2PServer creates a new P2PServer
func NewP2PServer(listeningAddresses []string) (server.P2PServer, error) {
	gRPCServer := newGRPCServer(listeningAddresses, p2pMaxMessageSize, p2pMaxInboundConnections, "P2P")
	p2pServer := &p2pServer{gRPCServer: *gRPCServer}
	protowire.RegisterP2PServer(gRPCServer.server, p2pServer)
	return p2pServer, nil
}

func (p *p2pServer) MessageStream(stream protowire.P2P_MessageStreamServer) error {
	defer panics.HandlePanic(log, "p2pServer.MessageStream", nil)

	return p.handleInboundConnection(stream.Context(), stream)
}

// Connect connects to the given address
// This is part of the P2PServer interface
func (p *p2pServer) Connect(address string) (server.Connection, error) {
	log.Debugf("%s Dialing to %s", p.name, address)

	// Use modern gRPC client with better connection management and backoff
	connectParams := grpc.ConnectParams{
		Backoff: backoff.Config{
			BaseDelay:  1.0 * time.Second,
			Multiplier: 1.6,
			Jitter:     0.2,
			MaxDelay:   120 * time.Second,
		},
		MinConnectTimeout: 5 * time.Second,
	}

	gRPCClientConnection, err := grpc.NewClient(address,
		grpc.WithConnectParams(connectParams),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, errors.Wrapf(err, "%s error connecting to %s", p.name, address)
	}
	connectionTransferred := false
	defer func() {
		if !connectionTransferred && gRPCClientConnection != nil {
			_ = gRPCClientConnection.Close()
		}
	}()

	client := protowire.NewP2PClient(gRPCClientConnection)
	compress := p.compressionFallback.shouldCompress(address)
	stream, err := client.MessageStream(context.Background(), p2pStreamCallOptions(compress)...)
	if err != nil {
		return nil, errors.Wrapf(err, "%s error getting client stream for %s", p.name, address)
	}
	connectionTransferred = true

	peerInfo, ok := peer.FromContext(stream.Context())
	if !ok {
		return nil, errors.Errorf("%s error getting stream peer info from context for %s", p.name, address)
	}
	tcpAddress, ok := peerInfo.Addr.(*net.TCPAddr)
	if !ok {
		return nil, errors.Errorf("non-tcp addresses are not supported")
	}

	connection := newConnection(&p.gRPCServer, tcpAddress, stream, gRPCClientConnection)
	connection.dialAddress = address
	connection.compressed = compress

	err = p.onConnectedHandler(connection)
	if err != nil {
		return nil, err
	}

	log.Debugf("%s Connected to %s", p.name, address)

	return connection, nil
}

// p2pStreamCallOptions returns the call options for an outbound P2P stream. compress requests gzip;
// without it the stream is plain. Either way the gzip codec stays registered, so compressed messages
// from peers that declare gzip are still decoded.
func p2pStreamCallOptions(compress bool) []grpc.CallOption {
	options := []grpc.CallOption{
		grpc.MaxCallRecvMsgSize(p2pMaxMessageSize),
		grpc.MaxCallSendMsgSize(p2pMaxMessageSize),
	}
	if compress {
		options = append(options, grpc.UseCompressor(gzip.Name))
	}
	return options
}
