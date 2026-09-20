package integration

import (
	"encoding/hex"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/kaspanet/go-secp256k1"

	"github.com/HoosatNetwork/HTND/domain/dagconfig"
	"github.com/HoosatNetwork/HTND/infrastructure/config"
	"github.com/HoosatNetwork/HTND/util"
)

const (
	defaultTimeout = 30 * time.Second
)

// NOTE: Integration tests need mining address private keys that are real schnorr
// private keys (32-byte hex), because some tests sign spends from coinbase UTXOs.
// Keep these deterministic.
var (
	addressInitOnce sync.Once

	p2pAddress1 string
	p2pAddress2 string
	p2pAddress3 string
	p2pAddress4 string
	p2pAddress5 string

	rpcAddress1 string
	rpcAddress2 string
	rpcAddress3 string
	rpcAddress4 string
	rpcAddress5 string

	miningAddress1PrivateKey = "0000000000000000000000000000000000000000000000000000000000000001"
	miningAddress2PrivateKey = "0000000000000000000000000000000000000000000000000000000000000002"
	miningAddress3PrivateKey = "0000000000000000000000000000000000000000000000000000000000000003"

	miningAddress1 = mustSchnorrAddressFromPrivateKeyHex(miningAddress1PrivateKey)
	miningAddress2 = mustSchnorrAddressFromPrivateKeyHex(miningAddress2PrivateKey)
	miningAddress3 = mustSchnorrAddressFromPrivateKeyHex(miningAddress3PrivateKey)
)

func init() {
	initTestAddresses()
}

func initTestAddresses() {
	addressInitOnce.Do(func() {
		p2pAddress1 = reserveLoopbackAddress()
		p2pAddress2 = reserveLoopbackAddress()
		p2pAddress3 = reserveLoopbackAddress()
		p2pAddress4 = reserveLoopbackAddress()
		p2pAddress5 = reserveLoopbackAddress()

		rpcAddress1 = reserveLoopbackAddress()
		rpcAddress2 = reserveLoopbackAddress()
		rpcAddress3 = reserveLoopbackAddress()
		rpcAddress4 = reserveLoopbackAddress()
		rpcAddress5 = reserveLoopbackAddress()
	})
}

// Reserved ports are taken from below the lowest ephemeral range any of the platforms these tests run
// on allocates from - Linux starts at 32768, Windows at 49152 - so that the kernel can never hand one
// of them out on its own.
const (
	minReservedPort = 20000
	maxReservedPort = 32000
)

// reservedListeners keeps every address handed out by reserveLoopbackAddress bound until the node that
// was given it is ready to listen on it, guarded by reservedListenersLock because harnesses are torn
// down from teardown goroutines.
var (
	reservedListenersLock sync.Mutex
	reservedListeners     = make(map[string]net.Listener)
	// Offset by pid so that two test binaries running at once - go test builds one per package, and CI
	// runs several packages in parallel - do not march through the same ports in the same order.
	nextPortToTry = minReservedPort + os.Getpid()%(maxReservedPort-minReservedPort)
)

// reserveLoopbackAddress finds a free loopback port and keeps holding it until releaseReservedAddress
// hands it over to the node that was given it.
//
// This used to listen on "127.0.0.1:0" and close the listener immediately, keeping only the address.
// That draws from the kernel's ephemeral range - the very range it also assigns to outgoing
// connections, which these tests make by the hundred - and left the port free from then until a node
// bound it. A client connection that happened to be assigned that exact port made the node's own bind
// fail with "address already in use", which kills the whole test binary rather than one test.
func reserveLoopbackAddress() string {
	reservedListenersLock.Lock()
	defer reservedListenersLock.Unlock()

	portCount := maxReservedPort - minReservedPort
	for attempt := 0; attempt < portCount; attempt++ {
		port := minReservedPort + (nextPortToTry-minReservedPort+attempt)%portCount
		address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
		listener, err := net.Listen("tcp", address)
		if err != nil {
			// Taken by something else on this machine - including another test binary's reservation.
			continue
		}

		nextPortToTry = port + 1
		reservedListeners[address] = listener
		return address
	}

	panic("no free loopback port in the reserved range")
}

// releaseReservedAddress drops the reservation on address so the node about to start can bind it. It
// is a no-op for an address that was never reserved, or whose reservation was already released by an
// earlier harness reusing the same address.
func releaseReservedAddress(address string) {
	reservedListenersLock.Lock()
	defer reservedListenersLock.Unlock()

	listener, ok := reservedListeners[address]
	if !ok {
		return
	}
	delete(reservedListeners, address)
	listener.Close()
}

func mustSchnorrAddressFromPrivateKeyHex(privateKeyHex string) string {
	privateKeyBytes, err := hex.DecodeString(privateKeyHex)
	if err != nil {
		panic(err)
	}
	keyPair, err := secp256k1.DeserializeSchnorrPrivateKeyFromSlice(privateKeyBytes)
	if err != nil {
		panic(err)
	}
	publicKey, err := keyPair.SchnorrPublicKey()
	if err != nil {
		panic(err)
	}
	publicKeySerialized, err := publicKey.Serialize()
	if err != nil {
		panic(err)
	}
	addr, err := util.NewAddressPublicKey(publicKeySerialized[:], util.Bech32PrefixHoosatSim)
	if err != nil {
		panic(err)
	}
	return addr.EncodeAddress()
}

func setConfig(t *testing.T, harness *appHarness, protocolVersion uint32) {
	initTestAddresses()
	harness.config = commonConfig()
	harness.config.AppDir = randomDirectory(t)
	harness.config.Listeners = []string{harness.p2pAddress}
	harness.config.RPCListeners = []string{harness.rpcAddress}
	harness.config.UTXOIndex = harness.utxoIndex
	harness.config.AllowSubmitBlockWhenNotSynced = true
	if protocolVersion != 0 {
		harness.config.ProtocolVersion = protocolVersion
	}

	if harness.overrideDAGParams != nil {
		harness.config.ActiveNetParams = harness.overrideDAGParams
	}

	// Integration tests shouldn't burn CPU on PoW solving.
	harness.config.ActiveNetParams.SkipProofOfWork = true
}

func commonConfig() *config.Config {
	commonConfig := config.DefaultConfig()

	*commonConfig.ActiveNetParams = dagconfig.SimnetParams // Copy so that we can make changes safely
	commonConfig.ActiveNetParams.SkipProofOfWork = true
	commonConfig.ActiveNetParams.BlockCoinbaseMaturity = 10
	commonConfig.TargetOutboundPeers = 0
	commonConfig.DisableDNSSeed = true
	commonConfig.Simnet = true
	commonConfig.AutoUpdateEnabled = config.Bool(false)

	return commonConfig
}

func randomDirectory(t *testing.T) string {
	dir, err := os.MkdirTemp("", "integration-test")
	if err != nil {
		// If the system temp directory is full (or otherwise unavailable),
		// fall back to a temp folder under the repository.
		fallbackBase := filepath.Join(".", ".tmp")
		if mkErr := os.MkdirAll(fallbackBase, 0o755); mkErr != nil {
			t.Fatalf("Error creating fallback temp directory for test: %+v", mkErr)
		}
		dir, err = os.MkdirTemp(fallbackBase, "integration-test")
		if err != nil {
			t.Fatalf("Error creating temporary directory for test: %+v", err)
		}
	}

	t.Cleanup(func() {
		_ = os.RemoveAll(dir)
	})

	return dir
}
