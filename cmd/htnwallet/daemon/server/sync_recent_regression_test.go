package server

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/daemon/pb"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/libhtnwallet"
)

var recentTestEpoch = time.Unix(1_800_000_000, 0)

func newRecentTestNode() *usableAddressesNode {
	node := &usableAddressesNode{usable: map[string]bool{}, cacheTTL: 30 * time.Second}
	node.setCheckTime(recentTestEpoch)
	return node
}

// The model advances the node's per-address cache clock independently of the wallet's request clock.
func setRecentTestTime(s *server, node *usableAddressesNode, elapsed time.Duration) {
	now := recentTestEpoch.Add(elapsed)
	s.now = func() time.Time { return now }
	node.setCheckTime(now)
}

func collectRecentAt(t *testing.T, s *server, node *usableAddressesNode, elapsed time.Duration) {
	t.Helper()
	setRecentTestTime(s, node, elapsed)
	mustCollectRecent(t, s)
}

func TestRecentPollAllowsForLateNodeCheck(t *testing.T) {
	node := newRecentTestNode()
	s := newRescanTestServer(t, node, 0, 0)
	address := rescanTestAddress(external, 0)
	collectRecentAt(t, s, node, -32*time.Second)
	node.asked()

	// The wallet starts at t=0. The target is checked at t=3 and the wallet
	// receives the answer at t=3.5, before the t=4 tick.
	node.setCheckTime(recentTestEpoch.Add(3 * time.Second))
	s.now = func() time.Time {
		node.mu.Lock()
		defer node.mu.Unlock()
		if node.calls == 0 {
			return recentTestEpoch
		}
		return recentTestEpoch.Add(3500 * time.Millisecond)
	}
	mustCollectRecent(t, s)
	node.pay(address)
	collectRecentAt(t, s, node, 32*time.Second)
	collectRecentAt(t, s, node, 34*time.Second)
	if s.addressSet[address] == nil {
		t.Fatal("the late negative check delayed discovery past the first tick after its cache entry expired")
	}
}

func TestRecentSweepRetriesWarmNodeCache(t *testing.T) {
	node := newRecentTestNode()
	s := newRescanTestServer(t, node, 0, 0)
	address := rescanTestAddress(external, 0)

	node.setCheckTime(recentTestEpoch.Add(-29900 * time.Millisecond))
	node.askAsAnotherClient(address)
	node.pay(address)
	collectRecentAt(t, s, node, 0)
	if s.addressSet[address] != nil {
		t.Fatal("the initial scan should receive the shared cached negative")
	}
	collectRecentAt(t, s, node, 2*time.Second)
	if s.addressSet[address] == nil {
		t.Fatal("the first tick after startup did not find the coin after the shared cache entry expired")
	}
}

func TestForcedSyncPollsRecentBatchesOffTick(t *testing.T) {
	for _, tc := range []struct {
		name  string
		when  time.Duration
		evict bool
	}{
		{name: "expired", when: 31 * time.Second},
		{name: "evicted", when: 5 * time.Second, evict: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node := newRecentTestNode()
			s := newRescanTestServer(t, node, 0, 0)
			collectRecentAt(t, s, node, -32*time.Second)
			collectRecentAt(t, s, node, 0)
			address := rescanTestAddress(external, 0)
			node.pay(address)
			if tc.evict {
				node.clearCache()
			}
			setRecentTestTime(s, node, tc.when)
			s.forceSyncChan = newForceSyncChan()
			s.forceSync()
			select {
			case <-s.forceSyncChan:
				mustSync(t, func() error { return s.sync(true) })
			default:
				t.Fatal("forceSync did not request a sync")
			}
			if s.addressSet[address] == nil {
				t.Fatalf("the forced sync at %s skipped the recent address", tc.when)
			}
		})
	}
}

func TestImportedNewAddressSkipsRecentlyFundedKey(t *testing.T) {
	node := newRecentTestNode()
	s := newWebWalletServer(t)
	s.backgroundRPCClient = usableAddressesNodeClient(t, node)
	receive := s.keysFile.ImportedKeys()
	p2pkh := func(index int) string {
		t.Helper()
		address, err := libhtnwallet.ImportedKeyAddress(s.params, receive[index].ExtendedPublicKey, libhtnwallet.SingleSigAddressTypeP2PKH)
		if err != nil {
			t.Fatal(err)
		}
		return address.String()
	}
	collectRecentAt(t, s, node, 0)
	node.clearCache()
	node.pay(p2pkh(0))
	setRecentTestTime(s, node, 6*time.Second)
	mustTick(t, s)

	request := &pb.NewAddressRequest{AddressType: pb.AddressType_ADDRESS_TYPE_P2PKH}
	next, err := s.NewAddress(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if next.Address != p2pkh(1) {
		t.Fatalf("NewAddress selected %s, want the second receive key %s", next.Address, p2pkh(1))
	}

	node.clearCache()
	node.pay(next.Address)
	setRecentTestTime(s, node, 8*time.Second)
	mustTick(t, s)
	if s.addressSet[next.Address] == nil {
		t.Fatal("the selected P2PKH alias was not discovered on the next tick")
	}
	shown, err := s.ShowAddresses(context.Background(), &pb.ShowAddressesRequest{AddressType: request.AddressType})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(shown.Address, p2pkh(0)) || !slices.Contains(shown.Address, next.Address) {
		t.Fatalf("ShowAddresses omitted an imported receive key: %v", shown.Address)
	}

	s.lock.Lock()
	change, _, err := issueTestChangeAddress(s, false)
	s.lock.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if s.addressSet[change.String()] == nil {
		t.Fatal("the imported change address was not tracked immediately")
	}
}

func addRecentTestPublicKey(t *testing.T, s *server) {
	t.Helper()
	mnemonic, err := libhtnwallet.CreateMnemonic()
	if err != nil {
		t.Fatal(err)
	}
	publicKey, err := libhtnwallet.MasterPublicKeyFromMnemonic(s.params, mnemonic, false)
	if err != nil {
		t.Fatal(err)
	}
	s.keysFile.ExtendedPublicKeys = []string{publicKey}
}

func recentTestDerivedAddress(t *testing.T, s *server, index uint32) (string, *walletAddress) {
	t.Helper()
	address := &walletAddress{keyChain: external, index: index}
	forms, err := s.walletAddressStringsForScan(address)
	if err != nil {
		t.Fatal(err)
	}
	return forms[0], address
}

func TestNewAddressWindowRetriesAfterFarOverlap(t *testing.T) {
	node := newRecentTestNode()
	s := newRescanTestServer(t, node, 1000, 20)
	addRecentTestPublicKey(t, s)
	address, walletAddr := recentTestDerivedAddress(t, s, 2050)
	s.recentAddressBatches[2000][address] = walletAddr
	collectRecentAt(t, s, node, 0)
	s.firstSyncDone.Store(true)

	// The real far walker scans [2000, 2100) and caches a negative for index 2050.
	setRecentTestTime(s, node, 2*time.Second)
	mustTick(t, s)
	node.asked()
	node.pay(address)
	if _, err := s.NewAddress(context.Background(), &pb.NewAddressRequest{}); err != nil {
		t.Fatal(err)
	}
	collectRecentAt(t, s, node, 28*time.Second)
	if calls, addresses := node.asked(); calls != 3 || addresses != 6001 {
		t.Fatalf("first recent sweep after NewAddress made %d calls with %d addresses, want 3 with 6001", calls, addresses)
	}
	collectRecentAt(t, s, node, 32*time.Second)
	if s.addressSet[address] != nil {
		t.Fatal("the far scan's negative should still be cached at age 30 seconds")
	}
	collectRecentAt(t, s, node, 34*time.Second)
	if s.addressSet[address] == nil {
		t.Fatal("recent polling missed the first tick after the overlapping far scan's entry expired")
	}
}

func TestAlignedFarSubsetKeepsRecentDeadline(t *testing.T) {
	node := newRecentTestNode()
	s := newRescanTestServer(t, node, 0, 0)
	addRecentTestPublicKey(t, s)
	collectRecentAt(t, s, node, -32*time.Second)
	collectRecentAt(t, s, node, 0)

	// An aligned far-sized request cannot stand in for the full batch.
	setRecentTestTime(s, node, 31*time.Second)
	s.lock.Lock()
	scanned, err := s.collectAddresses(0, 100)
	s.lock.Unlock()
	if err != nil || !scanned {
		t.Fatalf("far subset scanned %t, error %v", scanned, err)
	}
	address := rescanTestAddress(external, 500)
	node.pay(address)
	collectRecentAt(t, s, node, 32*time.Second)
	if s.addressSet[address] == nil {
		t.Fatal("the aligned far subset postponed the full recent poll")
	}
}

func TestRecentEntryIgnoresOldWalkerTimestamp(t *testing.T) {
	node := &usableAddressesNode{usable: map[string]bool{}}
	s := newRescanTestServer(t, node, 1999, 10000)
	collectRecentAt(t, s, node, 0)
	setRecentTestTime(s, node, 2*time.Second)
	mustTick(t, s) // The old walker scans batch 3000.
	if s.nextRescanIndex != 4000 {
		t.Fatalf("old cursor at %d, want 4000", s.nextRescanIndex)
	}
	if err := s.keysFile.SetLastUsedExternalIndex(2001); err != nil {
		t.Fatal(err)
	}
	address := rescanTestAddress(external, 3500)
	node.pay(address)
	collectRecentAt(t, s, node, 4*time.Second)
	if s.addressSet[address] == nil {
		t.Fatal("the previous old-walker scan suppressed the first recent scan")
	}
}

func TestDueOldRecheckSurvivesRecentCooldown(t *testing.T) {
	node := &usableAddressesNode{usable: map[string]bool{}}
	s := newRescanTestServer(t, node, 0, 0)
	collectRecentAt(t, s, node, -32*time.Second)
	collectRecentAt(t, s, node, 0)

	s.oldBatchRechecks = []oldBatchRecheck{{start: 0, notBefore: recentTestEpoch.Add(4 * time.Second)}}
	address := rescanTestAddress(external, 0)
	node.pay(address)
	setRecentTestTime(s, node, 4*time.Second)
	mustTick(t, s)
	if s.addressSet[address] == nil || len(s.oldBatchRechecks) != 0 {
		t.Fatal("the recent cooldown consumed a due old re-check without scanning")
	}
}

func TestRecentExitKeepsScheduledPoll(t *testing.T) {
	node := &usableAddressesNode{usable: map[string]bool{}}
	s := newRescanTestServer(t, node, 6500, 12000)
	collectRecentAt(t, s, node, -32*time.Second)
	collectRecentAt(t, s, node, 0)
	s.nextRescanIndex = 8000 // This round has already passed the batch at 5000.
	s.rescanRoundEnd = 13000
	if err := s.keysFile.SetLastUsedExternalIndex(8500); err != nil {
		t.Fatal(err)
	}
	address := rescanTestAddress(external, 5050)
	node.pay(address)
	setRecentTestTime(s, node, 2*time.Second)
	mustTick(t, s)
	setRecentTestTime(s, node, 32*time.Second)
	mustTick(t, s)
	if s.addressSet[address] == nil {
		t.Fatal("the batch that left the recent window lost its scheduled poll")
	}
}

func TestIncompleteFirstSweepRetriesSuccessfulPrefix(t *testing.T) {
	node := &usableAddressesNode{usable: map[string]bool{}}
	s := newRescanTestServer(t, node, 1000, 0)
	setRecentTestTime(s, node, 0)
	node.failScanOf(rescanTestAddress(external, 1000))
	if err := s.collectRecentAddresses(false); err == nil || !strings.Contains(err.Error(), "test scan failure") {
		t.Fatalf("first sweep returned %v, want the second batch's RPC failure", err)
	}
	address := rescanTestAddress(external, 0)
	node.pay(address)
	node.failScanOf("")
	collectRecentAt(t, s, node, 2*time.Second)
	if s.addressSet[address] == nil || s.nextSyncStartIndex != 2000 {
		t.Fatalf("retry did not scan the prefix and finish the sweep; frontier is %d", s.nextSyncStartIndex)
	}
}

func TestClippedRecentBatchKeepsPollCadence(t *testing.T) {
	limit := scanIndexLimit
	scanIndexLimit = 1002
	t.Cleanup(func() { scanIndexLimit = limit })

	node := &usableAddressesNode{usable: map[string]bool{}}
	s := newRescanTestServer(t, node, 1000, 0)
	addRecentTestPublicKey(t, s)
	address, _ := recentTestDerivedAddress(t, s, scanIndexLimit-1)
	collectRecentAt(t, s, node, 0)
	collectRecentAt(t, s, node, 32*time.Second)
	node.asked()
	node.pay(address)
	collectRecentAt(t, s, node, 64*time.Second-time.Millisecond)
	if calls, _ := node.asked(); calls != 0 {
		t.Errorf("made %d recent calls before the next 32-second poll", calls)
	}
	collectRecentAt(t, s, node, 64*time.Second)
	if calls, addresses := node.asked(); calls != 2 || addresses != 2012 {
		t.Errorf("made %d calls with %d addresses, want 2 calls with 2012 addresses (12 in the clipped batch)", calls, addresses)
	}
	if s.addressSet[address] == nil {
		t.Error("the highest valid index was not discovered")
	}
}
