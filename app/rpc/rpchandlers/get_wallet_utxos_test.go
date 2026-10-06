package rpchandlers

import (
	"strings"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/libhtnwallet"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
	"github.com/HoosatNetwork/HTND/v2/util"
)

func newTestExtendedPublicKey(t *testing.T, params *dagconfig.Params) string {
	t.Helper()
	mnemonic, err := libhtnwallet.CreateMnemonic()
	if err != nil {
		t.Fatalf("CreateMnemonic: %+v", err)
	}
	extendedPublicKey, err := libhtnwallet.MasterPublicKeyFromMnemonic(params, mnemonic, false)
	if err != nil {
		t.Fatalf("MasterPublicKeyFromMnemonic: %+v", err)
	}
	return extendedPublicKey
}

// fundedWallet is a fake UTXO index: one coin on each funded address.
type fundedWallet map[string]uint64

func (w fundedWallet) fund(t *testing.T, params *dagconfig.Params, extendedPublicKeys []string, minimumSignatures uint32,
	path string, form int, amount uint64,
) string {
	t.Helper()
	addresses, err := libhtnwallet.WalletAddressesAtPath(params, extendedPublicKeys, minimumSignatures, path, false)
	if err != nil {
		t.Fatalf("WalletAddressesAtPath(%s): %+v", path, err)
	}
	address := addresses[form].String()
	w[address] = amount
	return address
}

func (w fundedWallet) lookup(address util.Address) ([]*appmessage.UTXOsByAddressesEntry, bool, error) {
	amount, ok := w[address.String()]
	if !ok {
		return nil, false, nil
	}
	return []*appmessage.UTXOsByAddressesEntry{{
		Address:   address.String(),
		Outpoint:  &appmessage.RPCOutpoint{TransactionID: strings.Repeat("0", 64)},
		UTXOEntry: &appmessage.RPCUTXOEntry{Amount: amount},
	}}, true, nil
}

func pathsByAddress(result *walletScanResult) map[string]string {
	paths := make(map[string]string, len(result.entries))
	for _, entry := range result.entries {
		paths[entry.Address] = entry.DerivationPath
	}
	return paths
}

// TestScanWalletUTXOsFindsEveryKeyChainAndAddressForm pins that the scan covers the receive and change
// key chains and all three single-sig address forms, and labels each coin with the path it is derived at.
func TestScanWalletUTXOsFindsEveryKeyChainAndAddressForm(t *testing.T) {
	params := &dagconfig.TestnetParams
	extendedPublicKeys := []string{newTestExtendedPublicKey(t, params)}
	wallet := fundedWallet{}
	want := map[string]string{
		wallet.fund(t, params, extendedPublicKeys, 1, "m/0/0", 0, 1): "m/0/0",
		wallet.fund(t, params, extendedPublicKeys, 1, "m/0/7", 1, 2): "m/0/7",
		wallet.fund(t, params, extendedPublicKeys, 1, "m/1/3", 2, 3): "m/1/3",
	}

	request := appmessage.NewGetWalletUTXOsRequestMessage(extendedPublicKeys, 0, false, 10, 0)
	result, err := scanWalletUTXOs(params, request, 0, wallet.lookup)
	if err != nil {
		t.Fatalf("scanWalletUTXOs: %+v", err)
	}
	got := pathsByAddress(result)
	if len(got) != len(want) {
		t.Fatalf("found %d coins, want %d: %v", len(got), len(want), got)
	}
	for address, path := range want {
		if got[address] != path {
			t.Fatalf("coin on %s reported at path %q, want %q", address, got[address], path)
		}
	}
	// The scan runs gapLimit indexes past the last used one.
	if result.scannedExternalIndexes != 7+1+10 || result.scannedInternalIndexes != 3+1+10 {
		t.Fatalf("scanned %d external and %d internal indexes, want 18 and 14",
			result.scannedExternalIndexes, result.scannedInternalIndexes)
	}
	if result.truncated {
		t.Fatalf("a complete scan reported itself truncated")
	}
}

// TestScanWalletUTXOsStopsAtTheGapLimit pins that a coin further than gapLimit unused addresses past the
// last used one is not reached, and is with a larger gapLimit.
func TestScanWalletUTXOsStopsAtTheGapLimit(t *testing.T) {
	params := &dagconfig.TestnetParams
	extendedPublicKeys := []string{newTestExtendedPublicKey(t, params)}
	wallet := fundedWallet{}
	farAddress := wallet.fund(t, params, extendedPublicKeys, 1, "m/0/25", 0, 1)

	result, err := scanWalletUTXOs(params, appmessage.NewGetWalletUTXOsRequestMessage(extendedPublicKeys, 0, false, 10, 0),
		0, wallet.lookup)
	if err != nil {
		t.Fatalf("scanWalletUTXOs: %+v", err)
	}
	if len(result.entries) != 0 {
		t.Fatalf("found %d coins beyond the gap limit", len(result.entries))
	}

	result, err = scanWalletUTXOs(params, appmessage.NewGetWalletUTXOsRequestMessage(extendedPublicKeys, 0, false, 30, 0),
		0, wallet.lookup)
	if err != nil {
		t.Fatalf("scanWalletUTXOs: %+v", err)
	}
	if got := pathsByAddress(result)[farAddress]; got != "m/0/25" {
		t.Fatalf("with a gap limit of 30 the coin at m/0/25 was reported at %q", got)
	}
}

// TestScanWalletUTXOsHonoursTheLimit pins that the scan stops at limit coins and says it did.
func TestScanWalletUTXOsHonoursTheLimit(t *testing.T) {
	params := &dagconfig.TestnetParams
	extendedPublicKeys := []string{newTestExtendedPublicKey(t, params)}
	wallet := fundedWallet{}
	for _, path := range []string{"m/0/0", "m/0/1", "m/0/2"} {
		wallet.fund(t, params, extendedPublicKeys, 1, path, 0, 1)
	}

	result, err := scanWalletUTXOs(params, appmessage.NewGetWalletUTXOsRequestMessage(extendedPublicKeys, 0, false, 5, 0),
		2, wallet.lookup)
	if err != nil {
		t.Fatalf("scanWalletUTXOs: %+v", err)
	}
	if len(result.entries) != 2 || !result.truncated {
		t.Fatalf("got %d coins, truncated=%t; want 2 and true", len(result.entries), result.truncated)
	}
}

// TestScanWalletUTXOsScansEveryCosignerOfAMultisigWallet pins that a multisig wallet is scanned at
// m/<cosigner>/<keyChain>/<index> for every cosigner.
func TestScanWalletUTXOsScansEveryCosignerOfAMultisigWallet(t *testing.T) {
	params := &dagconfig.TestnetParams
	extendedPublicKeys := []string{newTestExtendedPublicKey(t, params), newTestExtendedPublicKey(t, params)}
	wallet := fundedWallet{}
	address := wallet.fund(t, params, extendedPublicKeys, 2, "m/1/1/4", 0, 1)

	result, err := scanWalletUTXOs(params, appmessage.NewGetWalletUTXOsRequestMessage(extendedPublicKeys, 2, false, 10, 0),
		0, wallet.lookup)
	if err != nil {
		t.Fatalf("scanWalletUTXOs: %+v", err)
	}
	if got := pathsByAddress(result)[address]; got != "m/1/1/4" {
		t.Fatalf("the multisig coin at m/1/1/4 was reported at %q", got)
	}
}

// TestScanWalletUTXOsRefusesBadRequests pins the request checks, which come back as RPC errors.
func TestScanWalletUTXOsRefusesBadRequests(t *testing.T) {
	params := &dagconfig.TestnetParams
	extendedPublicKey := newTestExtendedPublicKey(t, params)
	for name, request := range map[string]*appmessage.GetWalletUTXOsRequestMessage{
		"no keys":            appmessage.NewGetWalletUTXOsRequestMessage(nil, 0, false, 0, 0),
		"too many signers":   appmessage.NewGetWalletUTXOsRequestMessage([]string{extendedPublicKey}, 2, false, 0, 0),
		"gap limit too high": appmessage.NewGetWalletUTXOsRequestMessage([]string{extendedPublicKey}, 0, false, maxWalletGapLimit+1, 0),
		"malformed key":      appmessage.NewGetWalletUTXOsRequestMessage([]string{"not-an-xpub"}, 0, false, 0, 0),
	} {
		_, err := scanWalletUTXOs(params, request, 0, fundedWallet{}.lookup)
		if _, ok := err.(*appmessage.RPCError); !ok {
			t.Errorf("%s: got %v, want an RPC error", name, err)
		}
	}
}
