package rpchandlers

import (
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/app/rpc/rpccontext"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/config"
	"github.com/HoosatNetwork/HTND/v2/util"
)

func usabilityCacheTestContext() *rpccontext.Context {
	return &rpccontext.Context{Config: &config.Config{Flags: &config.Flags{
		NetworkFlags: config.NetworkFlags{ActiveNetParams: &dagconfig.MainnetParams},
	}}}
}

func seedUsableAddressesCache(address string, usable bool, checkedAt time.Time) {
	usableAddressesCacheMutex.Lock()
	usableAddressesCache[address] = usableAddressCacheEntry{usable: usable, checkedAt: checkedAt}
	usableAddressesCacheMutex.Unlock()
}

func forgetUsableAddress(address string) {
	usableAddressesCacheMutex.Lock()
	delete(usableAddressesCache, address)
	usableAddressesCacheMutex.Unlock()
}

func cacheTestAddress(t testing.TB) string {
	address, err := util.NewAddressPublicKeyHash(make([]byte, 32), util.Bech32PrefixHoosat)
	if err != nil {
		t.Fatal(err)
	}
	return address.EncodeAddress()
}

// A fresh cache entry answers without decoding the address or building its script. The context has no
// UTXO index, so a miss would panic rather than return.
func TestGetUsabilityOfAddressHitDoesNotDecode(t *testing.T) {
	address := cacheTestAddress(t)
	defer forgetUsableAddress(address)
	ctx := usabilityCacheTestContext()

	for _, want := range []bool{true, false} {
		seedUsableAddressesCache(address, want, time.Now())
		got, err := getUsabilityOfAddress(ctx, address)
		if err != nil || got != want {
			t.Fatalf("got (%t, %v), want (%t, nil)", got, err, want)
		}
	}

	allocs := testing.AllocsPerRun(100, func() {
		if _, err := getUsabilityOfAddress(ctx, address); err != nil {
			t.Fatal(err)
		}
	})
	if allocs != 0 {
		t.Fatalf("cache hit allocated %v times, want 0", allocs)
	}
}

// An address that does not decode is never cached, so it reports the decode error on every call.
func TestGetUsabilityOfAddressInvalidAlwaysErrors(t *testing.T) {
	const invalid = "not-an-address"
	ctx := usabilityCacheTestContext()
	for i := 0; i < 3; i++ {
		if _, err := getUsabilityOfAddress(ctx, invalid); err == nil {
			t.Fatalf("call %d: expected a decode error", i)
		}
	}
	usableAddressesCacheMutex.Lock()
	_, cached := usableAddressesCache[invalid]
	usableAddressesCacheMutex.Unlock()
	if cached {
		t.Fatal("an invalid address was cached")
	}
}

func BenchmarkGetUsabilityOfAddressCacheHit(b *testing.B) {
	address := cacheTestAddress(b)
	seedUsableAddressesCache(address, true, time.Now().Add(time.Hour))
	defer forgetUsableAddress(address)
	ctx := usabilityCacheTestContext()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := getUsabilityOfAddress(ctx, address); err != nil {
			b.Fatal(err)
		}
	}
}
