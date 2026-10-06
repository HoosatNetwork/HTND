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

	address, walletAddr, err := s.changeAddress(false, nil, false)
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

// TestMLDSA44ChangeAddressWithExhaustedKeyPool pins that a wallet whose internal index has reached the
// end of its ML-DSA-44 key pool can still send with an existing change address: that reuses internal
// index 0 and must not be refused for lacking the key after the last used one. A fresh change address
// is still refused, and the refusal leaves the internal index where it was.
func TestMLDSA44ChangeAddressWithExhaustedKeyPool(t *testing.T) {
	params := &dagconfig.MainnetParams
	mnemonic, err := libhtnwallet.CreateMnemonic()
	if err != nil {
		t.Fatalf("CreateMnemonic: %+v", err)
	}
	extendedPublicKey, err := libhtnwallet.MasterPublicKeyFromMnemonic(params, mnemonic, false)
	if err != nil {
		t.Fatalf("MasterPublicKeyFromMnemonic: %+v", err)
	}
	const poolSize = 3
	pool, err := keys.NewMLDSA44KeyPool(mnemonic, poolSize, false)
	if err != nil {
		t.Fatalf("NewMLDSA44KeyPool: %+v", err)
	}
	keysFile := &keys.File{ExtendedPublicKeys: []string{extendedPublicKey}, MinimumSignatures: 1, MLDSA44: pool}
	err = keysFile.SetPath(params, filepath.Join(t.TempDir(), "keys.json"), true)
	if err != nil {
		t.Fatalf("SetPath: %+v", err)
	}
	err = keysFile.SetLastUsedInternalIndex(poolSize - 1)
	if err != nil {
		t.Fatalf("SetLastUsedInternalIndex: %+v", err)
	}
	s := &server{params: params, keysFile: keysFile}

	_, walletAddr, err := s.changeAddress(true, nil, true)
	if err != nil {
		t.Fatalf("an existing ML-DSA-44 change address was refused: %+v", err)
	}
	if walletAddr.index != 0 || walletAddr.keyChain != libhtnwallet.InternalKeychain || !walletAddr.mldsa44 {
		t.Fatalf("an existing ML-DSA-44 change address is %+v, want internal index 0", walletAddr)
	}

	_, _, err = s.changeAddress(false, nil, true)
	if err == nil {
		t.Fatalf("a fresh ML-DSA-44 change address past the key pool was handed out")
	}
	if got := keysFile.LastUsedInternalIndex(); got != poolSize-1 {
		t.Fatalf("a refused change address moved the internal index to %d, want %d", got, poolSize-1)
	}
}
