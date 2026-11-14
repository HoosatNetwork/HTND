package handshake

import (
	"net"
	"sync"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
	"github.com/HoosatNetwork/HTND/v2/app/protocol/common"
	peerpkg "github.com/HoosatNetwork/HTND/v2/app/protocol/peer"
	"github.com/HoosatNetwork/HTND/v2/app/protocol/protocolerrors"
	"github.com/HoosatNetwork/HTND/v2/domain"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/config"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database/pebble"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/addressmanager"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter/id"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter/router"
	"github.com/pkg/errors"
)

// TestHandshakeLocalErrorDisconnectsWithoutPanic gives the handshake's error handler an error that is neither a
// closed route nor a protocol error, as a local database failure is. The peer must be dropped without a ban and the
// node must keep running.
func TestHandshakeLocalErrorDisconnectsWithoutPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("handleError panicked on a local error: %v", r)
		}
	}()

	isStopping := uint32(0)
	errChan := make(chan error, 1)
	handleError(errors.New("local database failure"), "ReceiveVersion", &isStopping, errChan)

	err := <-errChan
	var protocolErr protocolerrors.ProtocolError
	if !errors.As(err, &protocolErr) {
		t.Fatalf("got %T (%v), want a protocol error", err, err)
	}
	if protocolErr.ShouldBan {
		t.Fatalf("a local error must disconnect without banning the peer")
	}
}

// failingPutDatabase fails every Put once failPuts is set, as a database with a full disk does.
type failingPutDatabase struct {
	database.Database
	failPuts bool
}

func (d *failingPutDatabase) Put(key *database.Key, value []byte) error {
	if d.failPuts {
		return errors.New("disk full")
	}
	return d.Database.Put(key, value)
}

// peersContext keeps the peers in a map, as the flow context does.
type peersContext struct {
	handshakeContext
	mutex sync.Mutex
	peers map[id.ID]*peerpkg.Peer
}

func (c *peersContext) AddToPeers(peer *peerpkg.Peer) error {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if _, ok := c.peers[*peer.ID()]; ok {
		return common.ErrPeerWithSameIDExists
	}
	c.peers[*peer.ID()] = peer
	return nil
}

func (c *peersContext) RemoveFromPeers(peer *peerpkg.Peer) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if c.peers[*peer.ID()] == peer {
		delete(c.peers, *peer.ID())
	}
}

type zeroDAAConsensus struct{ externalapi.Consensus }

func (zeroDAAConsensus) GetVirtualDAAScore() (uint64, error) { return 0, nil }

type consensusDomain struct {
	domain.Domain
	consensus externalapi.Consensus
}

func (d consensusDomain) Consensus() externalapi.Consensus { return d.consensus }

// TestHandshakeAddAddressesFailureRemovesPeer fails the address store's write after a handshake that otherwise
// completed. The peer was already added to the peers list, so HandleHandshake must take it out again: the caller
// only removes peers it received, and an entry left behind keeps that peer ID from ever connecting again.
func TestHandshakeAddAddressesFailureRemovesPeer(t *testing.T) {
	netConnection, netAdapter := loopbackNetConnection(t)

	cfg := config.DefaultConfig()
	pebbleDB, err := pebble.OpenPebbleDB(t.TempDir(), 8)
	if err != nil {
		t.Fatal(err)
	}
	defer pebbleDB.Close()
	db := &failingPutDatabase{Database: pebbleDB}
	addressManager, err := addressmanager.New(addressmanager.NewConfig(cfg), db)
	if err != nil {
		t.Fatal(err)
	}
	db.failPuts = true

	context := &peersContext{
		handshakeContext: handshakeContext{cfg: cfg, netAdapter: netAdapter, addressManager: addressManager,
			domain: consensusDomain{consensus: zeroDAAConsensus{}}},
		peers: map[id.ID]*peerpkg.Peer{},
	}

	peerID, err := id.GenerateID()
	if err != nil {
		t.Fatal(err)
	}
	// A routable address, so that the address manager has something to store.
	peerAddress := appmessage.NewNetAddressIPPort(net.ParseIP("8.8.8.8"), 42420)
	receiveVersionRoute, sendVersionRoute := router.NewRoute("receive version"), router.NewRoute("send version")
	outgoingRoute := router.NewRoute("outgoing")
	err = receiveVersionRoute.Enqueue(appmessage.NewMsgVersion(peerAddress, peerID, cfg.ActiveNetParams.Name, nil,
		cfg.ProtocolVersion))
	if err != nil {
		t.Fatal(err)
	}
	if err := sendVersionRoute.Enqueue(appmessage.NewMsgVerAck()); err != nil {
		t.Fatal(err)
	}

	peer, err := HandleHandshake(context, netConnection, receiveVersionRoute, sendVersionRoute, outgoingRoute)
	if err == nil {
		t.Fatalf("HandleHandshake succeeded (peer %v), want the address store's write error", peer)
	}
	if len(context.peers) != 0 {
		t.Fatalf("%d peer(s) left in the peers list after a failed handshake, want none", len(context.peers))
	}
}
