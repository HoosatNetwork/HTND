package common

import (
	"time"

	peerpkg "github.com/Hoosat-Oy/HTND/app/protocol/peer"
	routerpkg "github.com/Hoosat-Oy/HTND/infrastructure/network/netadapter/router"

	"github.com/pkg/errors"
)

// DefaultTimeout is the default duration to wait for enqueuing/dequeuing
// to/from routes.
const DefaultTimeout = 600 * time.Second

// ErrPeerWithSameIDExists signifies that a peer with the same ID already exist.
var (
	ErrPeerWithSameIDExists = errors.New("ready peer with the same ID already exists")
	ErrHandshakeTimeout     = errors.New("handshake timed out")
)

type flowExecuteFunc func(peer *peerpkg.Peer)

// Flow is a a data structure that is used in order to associate a p2p flow to some route in a router.
type Flow struct {
	Name        string
	ExecuteFunc flowExecuteFunc
}

// FlowInitializeFunc is a function that is used in order to initialize a flow
type FlowInitializeFunc func(route *routerpkg.Route, peer *peerpkg.Peer) error
