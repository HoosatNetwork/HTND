package connmanager

import (
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Hoosat-Oy/HTND/app/appmessage"
	"github.com/Hoosat-Oy/HTND/infrastructure/network/dnsseed"
	"github.com/pkg/errors"

	"github.com/Hoosat-Oy/HTND/infrastructure/network/addressmanager"

	"github.com/Hoosat-Oy/HTND/infrastructure/network/netadapter"

	"github.com/Hoosat-Oy/HTND/infrastructure/config"
)

// connectionRequest represents a user request (either through CLI or RPC) to connect to a certain node
type connectionRequest struct {
	address       string
	isPermanent   bool
	nextAttempt   time.Time
	retryDuration time.Duration
}

// ConnectionManager monitors that the current active connections satisfy the requirements of
// outgoing, requested and incoming connections
type ConnectionManager struct {
	cfg            *config.Config
	netAdapter     *netadapter.NetAdapter
	addressManager *addressmanager.AddressManager

	activeRequested  map[string]*connectionRequest
	pendingRequested map[string]*connectionRequest
	activeOutgoing   map[string]struct{}
	targetOutgoing   int
	activeIncoming   map[string]struct{}
	maxIncoming      int

	stop                   uint32
	connectionRequestsLock sync.RWMutex

	resetLoopChan chan struct{}
	loopTicker    *time.Ticker
}

// New instantiates a new instance of a ConnectionManager
func New(cfg *config.Config, netAdapter *netadapter.NetAdapter, addressManager *addressmanager.AddressManager) (*ConnectionManager, error) {
	c := &ConnectionManager{
		cfg:              cfg,
		netAdapter:       netAdapter,
		addressManager:   addressManager,
		activeRequested:  map[string]*connectionRequest{},
		pendingRequested: map[string]*connectionRequest{},
		activeOutgoing:   map[string]struct{}{},
		activeIncoming:   map[string]struct{}{},
		resetLoopChan:    make(chan struct{}),
		loopTicker:       time.NewTicker(connectionsLoopInterval),
	}

	connectPeers := cfg.AddPeers
	if len(cfg.ConnectPeers) > 0 {
		connectPeers = cfg.ConnectPeers
	}

	c.maxIncoming = cfg.MaxInboundPeers
	c.targetOutgoing = cfg.TargetOutboundPeers

	for _, connectPeer := range connectPeers {
		c.pendingRequested[connectPeer] = &connectionRequest{
			address:     connectPeer,
			isPermanent: true,
		}
	}

	return c, nil
}

// Start begins the operation of the ConnectionManager
func (c *ConnectionManager) Start() {
	spawn("ConnectionManager.connectionsLoop", c.connectionsLoop)
}

// Stop halts the operation of the ConnectionManager
func (c *ConnectionManager) Stop() {
	atomic.StoreUint32(&c.stop, 1)

	for _, connection := range c.netAdapter.P2PConnections() {
		connection.Disconnect()
	}

	c.loopTicker.Stop()
	// Force the next iteration so the connection loop will stop immediately and not after `connectionsLoopInterval`.
	c.run()
}

func (c *ConnectionManager) run() {
	c.resetLoopChan <- struct{}{}
}

func (c *ConnectionManager) initiateConnection(address string) error {
	log.Debugf("Connecting to %s", address)
	return c.netAdapter.P2PConnect(address)
}

const connectionsLoopInterval = 30 * time.Second

func (c *ConnectionManager) connectionsLoop() {
	for atomic.LoadUint32(&c.stop) == 0 {
		connections := c.netAdapter.P2PConnections()

		// We convert the connections list to a set, so that connections can be found quickly
		// Then we go over the set, classifying connection by category: requested, outgoing or incoming.
		// Every step removes all matching connections so that once we get to checkIncomingConnections -
		// the only connections left are the incoming ones
		connSet := convertToSet(connections)

		c.checkRequestedConnections(connSet)

		c.checkOutgoingConnections(connSet)

		c.checkIncomingConnections(connSet)

		c.waitTillNextIteration()
	}
}

// ConnectionCount returns the count of the connected connections
func (c *ConnectionManager) ConnectionCount() int {
	return c.netAdapter.P2PConnectionCount()
}

// ErrCannotBanPermanent is the error returned when trying to ban a permanent peer.
var ErrCannotBanPermanent = errors.New("ErrCannotBanPermanent")

// Ban marks the given netConnection as banned
func (c *ConnectionManager) Ban(netConnection *netadapter.NetConnection) error {
	if c.isPermanent(netConnection.Address()) {
		return errors.Wrapf(ErrCannotBanPermanent, "Cannot ban %s because it's a permanent connection", netConnection.Address())
	}

	return c.addressManager.Ban(netConnection.NetAddress())
}

// BanByIP bans the given IP and disconnects from all the connection with that IP.
func (c *ConnectionManager) BanByIP(ip net.IP) error {
	ipHasPermanentConnection, err := c.ipHasPermanentConnection(ip)
	if err != nil {
		return err
	}

	if ipHasPermanentConnection {
		return errors.Wrapf(ErrCannotBanPermanent, "Cannot ban %s because it's a permanent connection", ip)
	}

	connections := c.netAdapter.P2PConnections()
	for _, conn := range connections {
		if conn.NetAddress().IP.Equal(ip) {
			conn.Disconnect()
		}
	}

	return c.addressManager.Ban(appmessage.NewNetAddressIPPort(ip, 0))
}

// IsBanned returns whether the given netConnection is banned
func (c *ConnectionManager) IsBanned(netConnection *netadapter.NetConnection) (bool, error) {
	if c.isPermanent(netConnection.Address()) {
		return false, nil
	}

	return c.addressManager.IsBanned(netConnection.NetAddress())
}

func (c *ConnectionManager) waitTillNextIteration() {
	select {
	case <-c.resetLoopChan:
		c.loopTicker.Reset(connectionsLoopInterval)
	case <-c.loopTicker.C:
	}
}

func (c *ConnectionManager) isPermanent(addressString string) bool {
	c.connectionRequestsLock.RLock()
	defer c.connectionRequestsLock.RUnlock()

	connReq, _, err := c.findRequestedConnection(addressString)
	if err != nil {
		log.Warnf("Failed to determine whether %s is a permanent connection: %s", addressString, err)
		return false
	}

	return connReq != nil && connReq.isPermanent
}

func (c *ConnectionManager) ipHasPermanentConnection(ip net.IP) (bool, error) {
	c.connectionRequestsLock.RLock()
	defer c.connectionRequestsLock.RUnlock()

	for addr, conn := range c.activeRequested {
		if !conn.isPermanent {
			continue
		}

		ips, err := c.extractAddressIPs(addr)
		if err != nil {
			return false, err
		}

		for _, extractedIP := range ips {
			if extractedIP.Equal(ip) {
				return true, nil
			}
		}
	}

	for addr, conn := range c.pendingRequested {
		if !conn.isPermanent {
			continue
		}

		ips, err := c.extractAddressIPs(addr)
		if err != nil {
			return false, err
		}

		for _, extractedIP := range ips {
			if extractedIP.Equal(ip) {
				return true, nil
			}
		}
	}

	return false, nil
}

func (c *ConnectionManager) extractAddressIPs(address string) ([]net.IP, error) {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}

	ip := net.ParseIP(host)
	if ip == nil {
		return c.cfg.Lookup(host)
	}

	return []net.IP{ip}, nil
}

func (c *ConnectionManager) findRequestedConnection(address string) (*connectionRequest, string, error) {
	if connReq, ok := c.activeRequested[address]; ok {
		return connReq, address, nil
	}

	if connReq, ok := c.pendingRequested[address]; ok {
		return connReq, address, nil
	}

	matchingAddress, connReq, err := c.findMatchingRequestedConnectionByResolvedAddress(address, c.activeRequested)
	if err != nil {
		return nil, "", err
	}
	if connReq != nil {
		return connReq, matchingAddress, nil
	}

	matchingAddress, connReq, err = c.findMatchingRequestedConnectionByResolvedAddress(address, c.pendingRequested)
	if err != nil {
		return nil, "", err
	}
	if connReq != nil {
		return connReq, matchingAddress, nil
	}

	return nil, "", nil
}

func (c *ConnectionManager) findMatchingRequestedConnectionByResolvedAddress(address string,
	requested map[string]*connectionRequest,
) (string, *connectionRequest, error) {
	for requestedAddress, connReq := range requested {
		matches, err := c.addressesMatch(requestedAddress, address)
		if err != nil {
			return "", nil, err
		}
		if matches {
			return requestedAddress, connReq, nil
		}
	}

	return "", nil, nil
}

func (c *ConnectionManager) addressesMatch(firstAddress, secondAddress string) (bool, error) {
	firstHost, firstPort, err := net.SplitHostPort(firstAddress)
	if err != nil {
		return false, err
	}

	secondHost, secondPort, err := net.SplitHostPort(secondAddress)
	if err != nil {
		return false, err
	}

	if firstPort != secondPort {
		return false, nil
	}

	firstIPs, err := c.hostIPs(firstHost)
	if err != nil {
		return false, err
	}

	secondIPs, err := c.hostIPs(secondHost)
	if err != nil {
		return false, err
	}

	for _, firstIP := range firstIPs {
		for _, secondIP := range secondIPs {
			if firstIP.Equal(secondIP) {
				return true, nil
			}
		}
	}

	return false, nil
}

func (c *ConnectionManager) hostIPs(host string) ([]net.IP, error) {
	ip := net.ParseIP(host)
	if ip != nil {
		return []net.IP{ip}, nil
	}

	return c.cfg.Lookup(host)
}

func (c *ConnectionManager) seedFromDNS() {
	cfg := c.cfg
	if c.needsMoreOutboundPeers() && !cfg.DisableDNSSeed {
		dnsseed.SeedFromDNS(cfg.NetParams(), cfg.DNSSeed, false, nil,
			cfg.Lookup, func(addresses []*appmessage.NetAddress) {
				// Hoosatd uses a lookup of the dns seeder here. Since seeder returns
				// IPs of nodes and not its own IP, we can not know real IP of
				// source. So we'll take first returned address as source.
				_ = c.addressManager.AddAddresses(addresses...)
			})

		dnsseed.SeedFromGRPC(cfg.NetParams(), cfg.GRPCSeed, false, nil,
			func(addresses []*appmessage.NetAddress) {
				_ = c.addressManager.AddAddresses(addresses...)
			})
	}
}

func (c *ConnectionManager) needsMoreOutboundPeers() bool {
	return len(c.activeOutgoing) < c.targetOutgoing
}
