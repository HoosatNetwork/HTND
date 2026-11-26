package server

import (
	"strings"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/libhtnwallet"
)

func TestMLDSA44ScanGapReportsDiscoveredChangeIndexZero(t *testing.T) {
	w := newMLWallet(t, 1, 1)
	w.s.keysFile.MLDSA44 = nil
	found := &walletAddress{index: 0, keyChain: libhtnwallet.InternalKeychain}
	address := w.address(t, found)
	// Local tracking occurs before a change output is confirmed.
	w.s.trackAddress(address.String(), found)
	w.s.warnOfMLDSA44ScanGap()
	if w.s.warnedOfMLDSA44ScanGap {
		t.Fatal("a tracked change address without a discovered coin triggered a notice")
	}
	if err := w.s.updateAddressesAndLastUsedIndexes(walletAddressSet{address.String(): found},
		&appmessage.GetUsableAddressesResponseMessage{Addresses: []string{address.String()}}); err != nil {
		t.Fatal(err)
	}
	if got := w.s.keysFile.LastUsedInternalIndex(); got != 0 {
		t.Fatalf("discovery changed the used index to %d", got)
	}
	gap, partial := w.s.mldsa44ScanGap()
	if !w.s.warnedOfMLDSA44ScanGap || partial || !strings.Contains(gap, "change ML-DSA-44 addresses from index 0") {
		t.Fatalf("want a coverage notice for discovered change index zero, got partial=%t, gap=%q", partial, gap)
	}
}
