package server

import (
	"fmt"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/keys"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/libhtnwallet"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter/server/grpcserver/protowire"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/rpcclient"
	"github.com/HoosatNetwork/HTND/v2/version"
	"google.golang.org/grpc"
)

// usableAddressesNode answers GetUsableAddresses from a set of addresses holding coins, and counts what it
// is asked.
type usableAddressesNode struct {
	protowire.UnimplementedRPCServer

	mu        sync.Mutex
	usable    map[string]bool
	calls     int
	addresses int

	// When cacheTTL is set, each address's answer is kept for cacheTTL as the node does, on the clock now.
	cacheTTL time.Duration
	now      func() time.Time
	cache    map[string]usableAddressesNodeCacheEntry
}

type usableAddressesNodeCacheEntry struct {
	usable    bool
	checkedAt time.Time
}

// isUsable answers for one address, from the cache when it holds a fresh answer. The caller holds n.mu.
func (n *usableAddressesNode) isUsable(address string) bool {
	if n.cacheTTL == 0 {
		return n.usable[address]
	}
	if entry, ok := n.cache[address]; ok && n.now().Sub(entry.checkedAt) <= n.cacheTTL {
		return entry.usable
	}
	if n.cache == nil {
		n.cache = make(map[string]usableAddressesNodeCacheEntry)
	}
	n.cache[address] = usableAddressesNodeCacheEntry{usable: n.usable[address], checkedAt: n.now()}
	return n.usable[address]
}

// askAsAnotherClient asks about an address as another wallet using the same node does, filling its cache.
func (n *usableAddressesNode) askAsAnotherClient(address string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.isUsable(address)
}

func (n *usableAddressesNode) MessageStream(stream protowire.RPC_MessageStreamServer) error {
	for {
		request, err := stream.Recv()
		if err != nil {
			return nil
		}
		appMessage, err := request.ToAppMessage()
		if err != nil {
			return err
		}
		var response appmessage.Message
		switch message := appMessage.(type) {
		case *appmessage.GetInfoRequestMessage:
			response = appmessage.NewGetInfoResponseMessage("", 0, version.Version(), false, true, true, 0, 0, "")
		case *appmessage.GetUsableAddressesRequestMessage:
			usable := []string{}
			n.mu.Lock()
			n.calls++
			n.addresses += len(message.Addresses)
			for _, address := range message.Addresses {
				if n.isUsable(address) {
					usable = append(usable, address)
				}
			}
			n.mu.Unlock()
			response = appmessage.NewGetUsableAddressesResponse(usable)
		default:
			continue
		}
		protoResponse, err := protowire.FromAppMessage(response)
		if err != nil {
			return err
		}
		if err := stream.Send(protoResponse); err != nil {
			return nil
		}
	}
}

// pay makes the addresses hold a coin.
func (n *usableAddressesNode) pay(addresses ...string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, address := range addresses {
		n.usable[address] = true
	}
}

// asked returns the GetUsableAddresses calls and the addresses in them since the last call of asked.
func (n *usableAddressesNode) asked() (calls, addresses int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	calls, addresses = n.calls, n.addresses
	n.calls, n.addresses = 0, 0
	return calls, addresses
}

// rescanTestAddress names the stand-in address newRescanTestServer puts at an index of a key chain.
func rescanTestAddress(keyChain uint8, index uint32) string {
	return fmt.Sprintf("test:%d/%d", keyChain, index)
}

// newRescanTestServer returns a wallet server whose key chains were used up to the given indexes, talking
// to node. Its address batches hold one stand-in address per index and key chain instead of derived ones:
// deriving 75,000 indexes takes minutes, and the scan only passes the strings to the node and back.
func newRescanTestServer(t *testing.T, node *usableAddressesNode, lastUsedExternalIndex, lastUsedInternalIndex uint32,
) *server {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %+v", err)
	}
	grpcServer := grpc.NewServer()
	protowire.RegisterRPCServer(grpcServer, node)
	go func() { _ = grpcServer.Serve(listener) }()
	t.Cleanup(grpcServer.Stop)

	client, err := rpcclient.NewRPCClient(listener.Addr().String())
	if err != nil {
		t.Fatalf("NewRPCClient: %+v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	client.SetTimeout(5 * time.Second)

	// No extended public keys: the far scan, which is not cached, then derives nothing.
	keysFile := &keys.File{MinimumSignatures: 1}
	if err := keysFile.SetPath(&dagconfig.TestnetParams, filepath.Join(t.TempDir(), "keys.json"), true); err != nil {
		t.Fatalf("SetPath: %+v", err)
	}
	if err := keysFile.SetLastUsedExternalIndex(lastUsedExternalIndex); err != nil {
		t.Fatalf("SetLastUsedExternalIndex: %+v", err)
	}
	if err := keysFile.SetLastUsedInternalIndex(lastUsedInternalIndex); err != nil {
		t.Fatalf("SetLastUsedInternalIndex: %+v", err)
	}

	s := &server{
		params:               &dagconfig.TestnetParams,
		keysFile:             keysFile,
		backgroundRPCClient:  client,
		addressSet:           make(walletAddressSet),
		recentAddressBatches: make(map[uint32]walletAddressSet),
	}
	lastBatch := min(s.maxUsedIndex()+2*numIndexesToQueryForRecentAddresses, scanIndexLimit-1)
	for start := uint32(0); start <= lastBatch; start += numIndexesToQueryForRecentAddresses {
		addRescanTestBatch(s, start)
	}
	return s
}

// addRescanTestBatch puts the stand-in addresses of the batch starting at start in the batch cache.
func addRescanTestBatch(s *server, start uint32) {
	batch := make(walletAddressSet, 2*numIndexesToQueryForRecentAddresses)
	for index := start; index < start+numIndexesToQueryForRecentAddresses; index++ {
		for _, keyChain := range keyChains {
			batch[rescanTestAddress(keyChain, index)] = &walletAddress{index: index, keyChain: keyChain}
		}
	}
	s.recentAddressBatches[start] = batch
}

func mustSync(t *testing.T, sync func() error) {
	t.Helper()
	if err := sync(); err != nil {
		t.Fatalf("sync: %+v", err)
	}
}

const (
	external = libhtnwallet.ExternalKeychain
	internal = libhtnwallet.InternalKeychain
)

// TestSyncOfLargeWalletScansFewBatches pins what a sync asks the node once the wallet is synced. A wallet
// that compounded has tens of thousands of used change indexes, nearly all empty, and every sync asked the
// node about every one of them: the first sync below is what every sync used to be.
func TestSyncOfLargeWalletScansFewBatches(t *testing.T) {
	node := &usableAddressesNode{usable: map[string]bool{}}
	s := newRescanTestServer(t, node, 1500, 75000)

	mustSync(t, s.collectRecentAddresses)
	firstCalls, firstAddresses := node.asked()
	if firstCalls != 76 || firstAddresses != 152000 {
		t.Fatalf("the first sync made %d calls with %d addresses, expected 76 with 152000", firstCalls, firstAddresses)
	}
	if s.nextSyncStartIndex != 76000 {
		t.Fatalf("the first sync left nextSyncStartIndex at %d", s.nextSyncStartIndex)
	}

	mustSync(t, s.sync)
	calls, addresses := node.asked()
	// The far scan, the batches at 0, 1000 and 2000 around external index 1500, the batches at 74000 and
	// 75000 around internal index 75000, and one old batch.
	if calls != 7 || addresses != 12000 {
		t.Fatalf("a sync made %d calls with %d addresses, expected 7 with 12000", calls, addresses)
	}

	// From oldBatchRecheckDelay on, every sync also re-checks the old batch scanned that long before.
	now := time.Now().Add(oldBatchRecheckDelay)
	s.now = func() time.Time { return now }
	mustSync(t, s.sync)
	steadyCalls, steadyAddresses := node.asked()
	if steadyCalls != 8 || steadyAddresses != 14000 {
		t.Fatalf("a sync with a re-check made %d calls with %d addresses, expected 8 with 14000", steadyCalls,
			steadyAddresses)
	}
	t.Logf("first sync (every sync before): %d calls, %d addresses; later syncs: %d calls, %d addresses; "+
		"with the re-check: %d calls, %d addresses", firstCalls, firstAddresses, calls, addresses, steadyCalls,
		steadyAddresses)
}

// TestSyncOfSmallWalletScansWhatItDid pins that a wallet with few used addresses is not asked about any
// batch twice in a sync: all its batches are recent ones, which leaves collectOldAddresses nothing.
func TestSyncOfSmallWalletScansWhatItDid(t *testing.T) {
	node := &usableAddressesNode{usable: map[string]bool{}}
	s := newRescanTestServer(t, node, 500, 20)

	mustSync(t, s.collectRecentAddresses)
	if calls, addresses := node.asked(); calls != 2 || addresses != 4000 {
		t.Fatalf("the first sync made %d calls with %d addresses, expected 2 with 4000", calls, addresses)
	}
	mustSync(t, s.sync)
	// The far scan and the batches at 0 and 1000, as before.
	if calls, addresses := node.asked(); calls != 3 || addresses != 4000 {
		t.Fatalf("a sync made %d calls with %d addresses, expected 3 with 4000", calls, addresses)
	}
}

// TestFirstSyncFindsEveryUsedAddress pins that the first sync still scans every index up to the last used
// one, in both key chains, whatever the last used index of each is.
func TestFirstSyncFindsEveryUsedAddress(t *testing.T) {
	paid := []string{
		rescanTestAddress(external, 5),
		rescanTestAddress(internal, 10500),
		rescanTestAddress(external, 40500), // Past the external key chain's own last used index.
		rescanTestAddress(internal, 74999),
	}
	node := &usableAddressesNode{usable: map[string]bool{}}
	node.pay(paid...)
	s := newRescanTestServer(t, node, 1500, 75000)

	mustSync(t, s.collectRecentAddresses)
	for _, address := range paid {
		if _, ok := s.addressSet[address]; !ok {
			t.Fatalf("the first sync did not find %s", address)
		}
	}
	if s.nextSyncStartIndex <= s.maxUsedIndex() {
		t.Fatalf("the first sync left nextSyncStartIndex at %d", s.nextSyncStartIndex)
	}
}

// TestCoinsAtOldAddressesAreFoundWithinARound pins the two delays of finding coins that reach an address
// after the first sync: one sync for an address near the last used index of its key chain, and one round
// of collectOldAddresses - a sync for each batch of used indexes - for any older one.
func TestCoinsAtOldAddressesAreFoundWithinARound(t *testing.T) {
	node := &usableAddressesNode{usable: map[string]bool{}}
	s := newRescanTestServer(t, node, 1500, 75000)
	mustSync(t, s.collectRecentAddresses)
	// This sync rescans the old batch at 3000, so the coin below at external index 3500 arrives just after
	// its batch was scanned: the longest wait there is.
	mustSync(t, s.sync)
	if s.nextRescanIndex != 4000 {
		t.Fatalf("the first old batch scanned left nextRescanIndex at %d", s.nextRescanIndex)
	}

	recent := []string{rescanTestAddress(external, 600), rescanTestAddress(internal, 74100)}
	old := []string{rescanTestAddress(external, 3500), rescanTestAddress(internal, 10500)}
	node.pay(recent...)
	node.pay(old...)

	mustSync(t, s.sync)
	for _, address := range recent {
		if _, ok := s.addressSet[address]; !ok {
			t.Fatalf("one sync did not find the coin at the recent address %s", address)
		}
	}

	round := int(s.maxUsedIndex()/numIndexesToQueryForRecentAddresses) + 1
	foundAfter := map[string]int{}
	for syncs := 1; syncs <= round && len(foundAfter) < len(old); syncs++ {
		if syncs > 1 {
			mustSync(t, s.sync)
		}
		for _, address := range old {
			if _, ok := s.addressSet[address]; ok && foundAfter[address] == 0 {
				foundAfter[address] = syncs
			}
		}
	}
	for _, address := range old {
		if foundAfter[address] == 0 {
			t.Fatalf("%d syncs did not find the coin at the old address %s", round, address)
		}
	}
	t.Logf("coins at old addresses found after %v syncs, within the round of %d", foundAfter, round)
}

// TestOldBatchSkippedOnClosedRouteIsRetried pins that a sync skipped because the RPC route was closed
// claims nothing: the old batch it would have scanned is the one the next sync scans.
func TestOldBatchSkippedOnClosedRouteIsRetried(t *testing.T) {
	node := &usableAddressesNode{usable: map[string]bool{}}
	s := newRescanTestServer(t, node, 1500, 75000)
	mustSync(t, s.collectRecentAddresses)
	mustSync(t, s.sync)
	nextSyncStartIndex, nextRescanIndex := s.nextSyncStartIndex, s.nextRescanIndex

	client := s.backgroundRPCClient
	s.backgroundRPCClient = closedRPCClient(t)
	mustSync(t, s.sync)
	if s.nextSyncStartIndex != nextSyncStartIndex || s.nextRescanIndex != nextRescanIndex {
		t.Fatalf("a skipped sync moved nextSyncStartIndex from %d to %d and nextRescanIndex from %d to %d",
			nextSyncStartIndex, s.nextSyncStartIndex, nextRescanIndex, s.nextRescanIndex)
	}

	s.backgroundRPCClient = client
	address := rescanTestAddress(internal, nextRescanIndex+500)
	node.pay(address)
	mustSync(t, s.sync)
	if _, ok := s.addressSet[address]; !ok {
		t.Fatalf("the sync after a skipped one did not scan the old batch at %d", nextRescanIndex)
	}
}

// TestRescanRoundEndsWhereItStarted pins that the round of collectOldAddresses ends where the last used
// index + 1000 was when it started. If its end moved with the last used index, a wallet using 1000 new
// indexes per sync would keep the cursor from ever wrapping back to a coin just behind it.
func TestRescanRoundEndsWhereItStarted(t *testing.T) {
	node := &usableAddressesNode{usable: map[string]bool{}}
	s := newRescanTestServer(t, node, 1500, 75000)
	mustSync(t, s.collectRecentAddresses)
	mustSync(t, s.sync)
	if s.nextRescanIndex != 4000 {
		t.Fatalf("the first old batch scanned left nextRescanIndex at %d", s.nextRescanIndex)
	}
	roundEnd := s.rescanRoundEnd
	// Just behind the cursor: the longest wait in this round.
	address := rescanTestAddress(external, 3500)
	node.pay(address)

	bound := int(roundEnd/numIndexesToQueryForRecentAddresses) + 1
	for syncs := 1; syncs <= bound; syncs++ {
		if err := s.keysFile.SetLastUsedInternalIndex(s.keysFile.LastUsedInternalIndex() + 1000); err != nil {
			t.Fatalf("SetLastUsedInternalIndex: %+v", err)
		}
		mustSync(t, s.sync)
		if _, ok := s.addressSet[address]; ok {
			t.Logf("found after %d syncs while the last used index grew by 1000 per sync; bound %d", syncs, bound)
			return
		}
	}
	t.Fatalf("%d syncs did not find the coin at %s while the last used index grew by 1000 per sync", bound, address)
}

// TestUnscannableLastUsedIndexIsIgnored pins that a recorded last used index no address can have neither
// wraps the scan's range ends around nor makes a scan reach the end of the index space, and that the first
// sync still scans the first batch. scanIndexLimit is lowered to 10,000 so the whole space is small.
func TestUnscannableLastUsedIndexIsIgnored(t *testing.T) {
	defer func(limit uint32) { scanIndexLimit = limit }(scanIndexLimit)
	scanIndexLimit = 10000

	paid := []string{rescanTestAddress(external, 5), rescanTestAddress(internal, 9999)}
	node := &usableAddressesNode{usable: map[string]bool{}}
	node.pay(paid...)
	// External: 4294966296, whose + 1000 wraps to 0 and would end the first sweep before batch 0.
	s := newRescanTestServer(t, node, 4294966296, 9500)

	mustSync(t, s.collectRecentAddresses)
	if calls, addresses := node.asked(); calls != 10 || addresses != 20000 {
		t.Fatalf("the first sync made %d calls with %d addresses, expected 10 with 20000", calls, addresses)
	}
	for _, address := range paid {
		if _, ok := s.addressSet[address]; !ok {
			t.Fatalf("the first sync did not find %s", address)
		}
	}
	if s.nextSyncStartIndex <= s.maxUsedIndex() {
		t.Fatalf("the first sync left nextSyncStartIndex at %d", s.nextSyncStartIndex)
	}

	// The coin at external 5 replaced the impossible recorded value.
	if got := s.keysFile.LastUsedExternalIndex(); got != 5 {
		t.Fatalf("the recorded external last used index is %d, expected 5", got)
	}

	mustSync(t, s.sync)
	// No far scan (the space is scanned), the batches at 0 and 1000 around external index 5, the batches
	// at 8000 and 9000 around internal index 9500, and the old batch at 2000. The batch at 10000 is due but
	// empty.
	if calls, addresses := node.asked(); calls != 5 || addresses != 10000 {
		t.Fatalf("a sync made %d calls with %d addresses, expected 5 with 10000", calls, addresses)
	}
	for range 50 {
		mustSync(t, s.sync)
		if calls, _ := node.asked(); calls > 5 {
			t.Fatalf("a sync made %d calls; the index space holds no more batches", calls)
		}
		if s.nextRescanIndex > scanIndexLimit || s.rescanRoundEnd > scanIndexLimit {
			t.Fatalf("the rescan cursor left the index space: nextRescanIndex %d, rescanRoundEnd %d",
				s.nextRescanIndex, s.rescanRoundEnd)
		}
	}
}

// TestStaleNegativeAnswerIsRechecked pins that an old batch whose scan got a cached answer older than a
// coin is scanned again once the node's cache has expired, rather than a round later. The node caches each
// address's answer for usableAddressesCacheTTL for all its clients, and the round after the stale scan can
// be far longer than the one the coin arrived in (for example after the last used index jumped).
func TestStaleNegativeAnswerIsRechecked(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	clock := func() time.Time { return now }
	node := &usableAddressesNode{usable: map[string]bool{}, cacheTTL: usableAddressesCacheTTL, now: clock}
	s := newRescanTestServer(t, node, 1500, 75000)
	s.now = clock
	sync := func() {
		t.Helper()
		now = now.Add(2 * time.Second)
		mustSync(t, s.sync)
	}

	mustSync(t, s.collectRecentAddresses)
	sync() // Starts the round ending at 76000.
	s.nextRescanIndex = 74000
	// Its re-check of the batch at 3000 would come just as the cache below expires and find the coin by
	// itself; the case under test is a batch with no re-check pending.
	s.oldBatchRechecks = nil
	// The internal key chain jumps far ahead: the next round ends at 301000. This sync scans the new range
	// and the old batch at 74000, which has left the recent window.
	if err := s.keysFile.SetLastUsedInternalIndex(300000); err != nil {
		t.Fatalf("SetLastUsedInternalIndex: %+v", err)
	}
	sync()

	// Another wallet on the node asks about external 3500 while it is empty, then a coin reaches it.
	address := rescanTestAddress(external, 3500)
	node.askAsAnotherClient(address)
	node.pay(address)

	// The old batch at 75000, then the wrap to the round ending at 301000, whose first batch that is not
	// recent is 3000: scanned within the node's cache time, so the coin is not seen yet.
	sync()
	sync()
	if s.nextRescanIndex != 4000 || s.rescanRoundEnd != 301000 {
		t.Fatalf("expected the batch at 3000 scanned in the round ending at 301000, got nextRescanIndex %d, "+
			"rescanRoundEnd %d", s.nextRescanIndex, s.rescanRoundEnd)
	}
	if _, ok := s.addressSet[address]; ok {
		t.Fatalf("the fake node's cache did not hide the coin")
	}
	staleScan := now

	for now.Add(2*time.Second).Sub(staleScan) < oldBatchRecheckDelay {
		sync()
	}
	if _, ok := s.addressSet[address]; ok {
		t.Fatalf("the batch at 3000 was scanned again before the node's cache expired")
	}
	// The re-check is due on this sync, but the route is closed: it stays queued.
	client := s.backgroundRPCClient
	s.backgroundRPCClient = closedRPCClient(t)
	queued := len(s.oldBatchRechecks)
	sync()
	if len(s.oldBatchRechecks) != queued || s.oldBatchRechecks[0].start != 3000 {
		t.Fatalf("a skipped re-check changed the queue from %d to %d entries", queued, len(s.oldBatchRechecks))
	}
	s.backgroundRPCClient = client

	sync()
	if _, ok := s.addressSet[address]; !ok {
		t.Fatalf("the coin at %s was not found %s after the stale scan", address, now.Sub(staleScan))
	}
	t.Logf("found %s after the stale scan (re-check delay %s, one sync skipped on a closed route)",
		now.Sub(staleScan), oldBatchRecheckDelay)
}

// TestDiscoveryReplacesUnscannableLastUsedIndex pins this case: the keys file records an external
// last used index no address can have, the internal one is 0, and the far scan finds a coin at external
// 10500. The discovery must become the external key chain's last used index, so that a later coin at
// external 10550, behind the far scan, is still found.
func TestDiscoveryReplacesUnscannableLastUsedIndex(t *testing.T) {
	node := &usableAddressesNode{usable: map[string]bool{}}
	s := newRescanTestServer(t, node, 4294966296, 0)
	for start := uint32(9000); start <= 12000; start += numIndexesToQueryForRecentAddresses {
		addRescanTestBatch(s, start)
	}
	mustSync(t, s.collectRecentAddresses)
	mustSync(t, s.sync)

	// What the far scan does when it reaches 10500: it scans the range through collectAddresses, and the
	// node reports the coin. (The fixture has no keys to derive the far scan's own 100-index range from.)
	found := rescanTestAddress(external, 10500)
	node.pay(found)
	s.lock.Lock()
	scanned, err := s.collectAddresses(10000, 11000)
	s.nextSyncStartIndex = 10600
	s.lock.Unlock()
	if err != nil || !scanned {
		t.Fatalf("collectAddresses: %t, %+v", scanned, err)
	}
	if got := s.keysFile.LastUsedExternalIndex(); got != 10500 {
		t.Fatalf("the discovery left the recorded external last used index at %d", got)
	}

	later := rescanTestAddress(external, 10550)
	node.pay(later)
	mustSync(t, s.sync)
	if _, ok := s.addressSet[later]; !ok {
		t.Fatalf("the next sync did not find the coin at %s, behind the far scan", later)
	}
}

// baseSyncCost returns what a sync asked the node before the change, after the first sweep: the far scan,
// which the fixture's far scan sends empty, and every batch up to the last used index + 1000.
func baseSyncCost(s *server) (calls, addresses int) {
	batches := int((s.maxUsedIndex() + 2*numIndexesToQueryForRecentAddresses - 1) / numIndexesToQueryForRecentAddresses)
	return 1 + batches, batches * 2 * numIndexesToQueryForRecentAddresses
}

// TestNoSyncAsksMoreThanBefore pins that after the first sweep no sync asks the node about more than every
// sync did before the change, re-checks included: wallets with one, two and three old batches, whose
// cursor comes back to a batch before its re-check is due, and a large one.
func TestNoSyncAsksMoreThanBefore(t *testing.T) {
	for _, wallet := range []struct{ external, internal uint32 }{{4500, 20}, {5980, 20}, {6980, 20}, {1500, 75000}} {
		now := time.Unix(1_800_000_000, 0)
		node := &usableAddressesNode{usable: map[string]bool{}}
		s := newRescanTestServer(t, node, wallet.external, wallet.internal)
		s.now = func() time.Time { return now }
		mustSync(t, s.collectRecentAddresses)
		node.asked()
		baseCalls, baseAddresses := baseSyncCost(s)
		var calls, addresses int
		for range 60 {
			now = now.Add(2 * time.Second)
			mustSync(t, s.sync)
			calls, addresses = node.asked()
			if calls > baseCalls || addresses > baseAddresses {
				t.Fatalf("wallet %d / %d: a sync made %d calls with %d addresses, more than the %d with %d before",
					wallet.external, wallet.internal, calls, addresses, baseCalls, baseAddresses)
			}
		}
		t.Logf("wallet %d / %d: before %d calls, %d addresses per sync; now %d calls, %d addresses",
			wallet.external, wallet.internal, baseCalls, baseAddresses, calls, addresses)
	}
}

// TestForcedSyncsLeaveNoRecheckBacklog pins that a burst of forced syncs - every broadcast forces one - does
// not delay re-checks: after every sync no due re-check is left, so each runs on the first sync at or after
// its time, and the queue is back to its usual length once the burst is over.
func TestForcedSyncsLeaveNoRecheckBacklog(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	node := &usableAddressesNode{usable: map[string]bool{}}
	s := newRescanTestServer(t, node, 1500, 75000)
	s.now = func() time.Time { return now }
	mustSync(t, s.collectRecentAddresses)
	node.asked()
	baseCalls, _ := baseSyncCost(s)

	syncAfter := func(interval time.Duration) {
		t.Helper()
		now = now.Add(interval)
		mustSync(t, s.sync)
		if calls, _ := node.asked(); calls > baseCalls {
			t.Fatalf("a sync made %d calls, more than the %d before", calls, baseCalls)
		}
		for _, recheck := range s.oldBatchRechecks {
			if !now.Before(recheck.notBefore) {
				t.Fatalf("a re-check due %s ago was left for a later sync", now.Sub(recheck.notBefore))
			}
		}
	}
	for range 30 {
		syncAfter(2 * time.Second)
	}
	for range 60 { // One broadcast per second for a minute.
		syncAfter(time.Second)
	}
	for range 60 {
		syncAfter(2 * time.Second)
	}
	if queued := len(s.oldBatchRechecks); queued > 17 {
		t.Fatalf("%d re-checks queued after the burst; the 2 s tick queues at most 17", queued)
	}
}

// TestRecheckOfARecentBatchIsDropped pins that a re-check of a batch collectRecentAddresses scans on every
// sync anyway is dropped rather than asked about a second time on the same sync.
func TestRecheckOfARecentBatchIsDropped(t *testing.T) {
	node := &usableAddressesNode{usable: map[string]bool{}}
	s := newRescanTestServer(t, node, 1500, 75000)
	mustSync(t, s.collectRecentAddresses)
	node.asked()
	s.oldBatchRechecks = []oldBatchRecheck{{start: 74000}} // Recent: around internal index 75000; due long ago.
	mustSync(t, s.sync)
	if calls, addresses := node.asked(); calls != 7 || addresses != 12000 {
		t.Fatalf("a sync made %d calls with %d addresses, expected 7 with 12000", calls, addresses)
	}
	if len(s.oldBatchRechecks) != 1 || s.oldBatchRechecks[0].start == 74000 {
		t.Fatalf("the re-check of the recent batch was not dropped: %+v", s.oldBatchRechecks)
	}
}
