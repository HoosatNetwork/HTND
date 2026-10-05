package grpcserver

import (
	"github.com/pkg/errors"

	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter/server"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter/server/grpcserver/protowire"
	"github.com/HoosatNetwork/HTND/v2/util/panics"
)

type rpcServer struct {
	protowire.UnimplementedRPCServer
	gRPCServer
}

// DefaultRPCMaxMessageSize is the built-in RPC message ceiling: 1GiB.
//
// HTN-166: an unauthenticated client can make the node buffer up to this. Unlike the P2P ceiling,
// lowering this cannot partition the node from the network - it only bounds what RPC clients may
// send and receive - but it can still break a legitimate large query (a very large
// GetUtxosByAddresses response, say), so the default is left alone and the value is made settable
// instead. An operator exposing RPC beyond localhost should lower it.
const DefaultRPCMaxMessageSize = 1024 * 1024 * 1024

// RPCMaxMessageSize is the effective RPC ceiling, also used by the in-process RPC client for its own
// call limits so that a client and server in the same binary agree. Set once at startup via
// SetMaxMessageSizes, before any server or client is created.
var RPCMaxMessageSize = DefaultRPCMaxMessageSize

// minimumSaneMaxMessageSize is not a hard floor - it is the point below which a limit is more likely
// to be a mistake than a decision, and is warned about. A single P2P message can legitimately carry
// a large batch of blocks or a pruning-point UTXO chunk.
const minimumSaneMaxMessageSize = 16 * 1024 * 1024

// SetMaxMessageSizes overrides the P2P and RPC message ceilings. A value of 0 leaves that ceiling at
// its built-in default.
//
// It must be called before NewP2PServer or NewRPCServer, since gRPC captures the limits when the
// server is constructed. It is deliberately not safe for concurrent use with server creation:
// startup is single-threaded, and a limit that could change under a running server would be worse
// than one that cannot change at all.
func SetMaxMessageSizes(p2pSize, rpcSize int) error {
	if p2pSize < 0 || rpcSize < 0 {
		return errors.Errorf("message size limits cannot be negative (p2p %d, rpc %d)", p2pSize, rpcSize)
	}

	if p2pSize != 0 {
		if p2pSize < minimumSaneMaxMessageSize {
			log.Warnf("P2P max message size is set to %d bytes, below %d. A single P2P message can "+
				"legitimately carry a large batch of blocks or a pruning-point UTXO chunk; a ceiling "+
				"this low can reject honest peers and stall IBD.", p2pSize, minimumSaneMaxMessageSize)
		}
		p2pMaxMessageSize = p2pSize
	}
	if rpcSize != 0 {
		if rpcSize < minimumSaneMaxMessageSize {
			log.Warnf("RPC max message size is set to %d bytes, below %d. Large query responses may "+
				"be rejected.", rpcSize, minimumSaneMaxMessageSize)
		}
		RPCMaxMessageSize = rpcSize
	}

	log.Infof("Message size limits: P2P %d bytes, RPC %d bytes", p2pMaxMessageSize, RPCMaxMessageSize)
	return nil
}

// NewRPCServer creates a new RPCServer
func NewRPCServer(listeningAddresses []string, rpcMaxInboundConnections int) (server.Server, error) {
	gRPCServer := newGRPCServer(listeningAddresses, RPCMaxMessageSize, rpcMaxInboundConnections, "RPC")
	rpcServer := &rpcServer{gRPCServer: *gRPCServer}
	protowire.RegisterRPCServer(gRPCServer.server, rpcServer)
	return rpcServer, nil
}

func (r *rpcServer) MessageStream(stream protowire.RPC_MessageStreamServer) error {
	defer panics.HandlePanic(log, "rpcServer.MessageStream", nil)

	return r.handleInboundConnection(stream.Context(), stream)
}
