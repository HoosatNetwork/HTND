package server

import (
	"testing"

	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/keys"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/libhtnwallet"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
)

func newScanTestServer(tb testing.TB) *server {
	params := &dagconfig.TestnetParams
	mnemonic, err := libhtnwallet.CreateMnemonic()
	if err != nil {
		tb.Fatalf("CreateMnemonic: %+v", err)
	}
	extendedPublicKey, err := libhtnwallet.MasterPublicKeyFromMnemonic(params, mnemonic, false)
	if err != nil {
		tb.Fatalf("MasterPublicKeyFromMnemonic: %+v", err)
	}
	return &server{
		params:     params,
		keysFile:   &keys.File{ExtendedPublicKeys: []string{extendedPublicKey}, MinimumSignatures: 1},
		addressSet: make(walletAddressSet),
	}
}

// BenchmarkRecentAddressBatch measures one batch of the recent-address scan, which every sync repeats
// every two seconds for each batch up to the last used index.
func BenchmarkRecentAddressBatch(b *testing.B) {
	s := newScanTestServer(b)
	for b.Loop() {
		if _, err := s.addressesToQuery(0, numIndexesToQueryForRecentAddresses); err != nil {
			b.Fatalf("addressesToQuery: %+v", err)
		}
	}
}

// TestRecentAddressBatchesAreCached pins that the recent-address scan derives each batch once, and that the
// cached batch is the one a fresh derivation gives.
func TestRecentAddressBatchesAreCached(t *testing.T) {
	s := newScanTestServer(t)
	first, err := s.addressesToQueryCached(numIndexesToQueryForRecentAddresses, 2*numIndexesToQueryForRecentAddresses)
	if err != nil {
		t.Fatalf("addressesToQueryCached: %+v", err)
	}
	second, err := s.addressesToQueryCached(numIndexesToQueryForRecentAddresses, 2*numIndexesToQueryForRecentAddresses)
	if err != nil {
		t.Fatalf("addressesToQueryCached: %+v", err)
	}
	if len(first) == 0 || len(second) != len(first) {
		t.Fatalf("the cached batch has %d addresses and the first scan had %d", len(second), len(first))
	}
	for address, walletAddr := range first {
		if second[address] != walletAddr {
			t.Fatalf("the second scan derived %s again instead of reusing the cached batch", address)
		}
	}

	fresh, err := s.addressesToQuery(numIndexesToQueryForRecentAddresses, 2*numIndexesToQueryForRecentAddresses)
	if err != nil {
		t.Fatalf("addressesToQuery: %+v", err)
	}
	if len(fresh) != len(first) {
		t.Fatalf("a fresh derivation has %d addresses and the cached batch %d", len(fresh), len(first))
	}
	for address, walletAddr := range fresh {
		if cached, ok := first[address]; !ok || *cached != *walletAddr {
			t.Fatalf("the cached batch differs from a fresh derivation at %s", address)
		}
	}

	if _, err := s.addressesToQueryCached(1000, 1000+numIndexesToQueryForFarAddresses); err != nil {
		t.Fatalf("addressesToQueryCached: %+v", err)
	}
	if len(s.recentAddressBatches) != 1 {
		t.Fatalf("a far-scan range was cached; %d batches are cached", len(s.recentAddressBatches))
	}
}
