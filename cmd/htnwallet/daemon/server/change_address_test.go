package server

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/keys"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/libhtnwallet"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
)

// TestChangeAddressIsTrackedBeforeItHoldsACoin pins that a new change address is read for balances and
// UTXOs from the moment it is handed out. It used to join the wallet's address set only once the address
// scan found a coin on it, and the node caches "no coin here" per address for 30 seconds - so after a
// payment was accepted, its input was gone and the change was invisible for up to half a minute.
func TestChangeAddressIsTrackedBeforeItHoldsACoin(t *testing.T) {
	params := &dagconfig.TestnetParams
	mnemonic, err := libhtnwallet.CreateMnemonic()
	if err != nil {
		t.Fatalf("CreateMnemonic: %+v", err)
	}
	extendedPublicKey, err := libhtnwallet.MasterPublicKeyFromMnemonic(params, mnemonic, false)
	if err != nil {
		t.Fatalf("MasterPublicKeyFromMnemonic: %+v", err)
	}
	keysFile := &keys.File{ExtendedPublicKeys: []string{extendedPublicKey}, MinimumSignatures: 1}
	if err := keysFile.SetPath(params, filepath.Join(t.TempDir(), "keys.json"), true); err != nil {
		t.Fatalf("SetPath: %+v", err)
	}
	s := &server{params: params, keysFile: keysFile, addressSet: make(walletAddressSet)}

	address, walletAddr, err := s.changeAddress(false, nil)
	if err != nil {
		t.Fatalf("changeAddress: %+v", err)
	}
	if walletAddr.keyChain != libhtnwallet.InternalKeychain || walletAddr.index != 1 {
		t.Fatalf("got change address %+v, want internal index 1", walletAddr)
	}
	tracked, ok := s.addressSet[address.String()]
	if !ok {
		t.Fatalf("change address %s is not in the address set", address)
	}
	if *tracked != *walletAddr {
		t.Fatalf("change address %s is tracked as %+v, want %+v", address, tracked, walletAddr)
	}
	if !slices.Contains(s.addressSet.strings(), address.String()) {
		t.Fatalf("change address %s is not among the addresses balances and UTXOs are read from", address)
	}

	// The scan must later find it under the same string, or the address would be tracked twice.
	scanned, err := s.walletAddressStringsForScan(walletAddr)
	if err != nil {
		t.Fatalf("walletAddressStringsForScan: %+v", err)
	}
	if !slices.Contains(scanned, address.String()) {
		t.Fatalf("the scan derives %v for the change address, not %s", scanned, address)
	}
}
