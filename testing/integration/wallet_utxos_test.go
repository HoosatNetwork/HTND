package integration

import (
	"testing"

	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/libhtnwallet"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
)

// TestGetWalletUTXOs mines to a receive address and a change address of an HD wallet - in different
// address forms - and requires GetWalletUTXOs, given only the wallet's extended public key, to return the
// coins of both with the paths they are derived at, and none of another miner's.
func TestGetWalletUTXOs(t *testing.T) {
	params := &dagconfig.SimnetParams
	mnemonic, err := libhtnwallet.CreateMnemonic()
	if err != nil {
		t.Fatalf("CreateMnemonic: %+v", err)
	}
	extendedPublicKey, err := libhtnwallet.MasterPublicKeyFromMnemonic(params, mnemonic, false)
	if err != nil {
		t.Fatalf("MasterPublicKeyFromMnemonic: %+v", err)
	}
	walletAddress := func(path string, form int) string {
		addresses, err := libhtnwallet.WalletAddressesAtPath(params, []string{extendedPublicKey}, 1, path, false)
		if err != nil {
			t.Fatalf("WalletAddressesAtPath(%s): %+v", path, err)
		}
		return addresses[form].String()
	}
	receiveAddress := walletAddress("m/0/3", 0) // P2PK
	changeAddress := walletAddress("m/1/1", 1)  // P2PKH
	wantPaths := map[string]string{receiveAddress: "m/0/3", changeAddress: "m/1/1"}

	htnd, teardown := setupHarness(t, &harnessParams{
		p2pAddress:              p2pAddress1,
		rpcAddress:              rpcAddress1,
		miningAddress:           miningAddress1,
		miningAddressPrivateKey: miningAddress1PrivateKey,
		utxoIndex:               true,
	})
	defer teardown()

	// A block's coinbase pays the miners of the blocks it merges, so each address is mined to and then
	// followed by a block that merges it.
	for _, address := range []string{miningAddress1, receiveAddress, changeAddress, miningAddress1} {
		htnd.miningAddress = address
		for range 2 {
			mineNextBlock(t, htnd)
		}
	}

	response, err := htnd.rpcClient.GetWalletUTXOs([]string{extendedPublicKey}, 0, false, 0, 0)
	if err != nil {
		t.Fatalf("GetWalletUTXOs: %+v", err)
	}
	found := map[string]int{}
	for _, entry := range response.Entries {
		wantPath, ok := wantPaths[entry.Address]
		if !ok {
			t.Fatalf("returned a coin of %s, which is not an address of the wallet", entry.Address)
		}
		if entry.DerivationPath != wantPath {
			t.Fatalf("coin of %s reported at path %q, want %q", entry.Address, entry.DerivationPath, wantPath)
		}
		if entry.UTXOEntry == nil || entry.UTXOEntry.Amount == 0 || entry.Outpoint == nil {
			t.Fatalf("coin of %s came back without its outpoint or entry: %+v", entry.Address, entry)
		}
		found[entry.Address]++
	}
	for address, path := range wantPaths {
		if found[address] == 0 {
			t.Fatalf("no coin returned for the wallet address at %s (%s); got %d entries", path, address,
				len(response.Entries))
		}
	}
	if response.ScannedExternalIndexes < 3+1 || response.ScannedInternalIndexes < 1+1 || response.Truncated {
		t.Fatalf("scanned %d external and %d internal indexes, truncated=%t", response.ScannedExternalIndexes,
			response.ScannedInternalIndexes, response.Truncated)
	}
}
