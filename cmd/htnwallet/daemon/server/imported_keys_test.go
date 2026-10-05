package server

import (
	"context"
	"encoding/hex"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/daemon/pb"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/keys"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/libhtnwallet"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/utxo"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
)

// newImportedWalletServer returns a synced server for a keys file holding the given imported wallet.
func newImportedWalletServer(t *testing.T, importType string, secret string, paths []string) *server {
	t.Helper()
	params := &dagconfig.TestnetParams
	keysFile, err := keys.NewImportedFile(params, importType, secret, paths, "password")
	if err != nil {
		t.Fatalf("NewImportedFile: %+v", err)
	}
	if err := keysFile.SetPath(params, filepath.Join(t.TempDir(), "keys.json"), true); err != nil {
		t.Fatalf("SetPath: %+v", err)
	}

	s := &server{params: params, keysFile: keysFile, addressSet: make(walletAddressSet)}
	if err := s.trackImportedKeys(); err != nil {
		t.Fatalf("trackImportedKeys: %+v", err)
	}
	s.nextSyncStartIndex = numIndexesToQueryForRecentAddresses
	s.firstSyncDone.Store(true)
	return s
}

func newPrivateKeyWalletServer(t *testing.T) (*server, string) {
	t.Helper()
	privateKey, _, err := libhtnwallet.CreateKeyPair(false)
	if err != nil {
		t.Fatalf("CreateKeyPair: %+v", err)
	}
	s := newImportedWalletServer(t, libhtnwallet.ImportedKeyTypePrivateKey, hex.EncodeToString(privateKey), nil)
	return s, importedKeyP2PK(t, s, s.keysFile.ImportedKeys()[0])
}

// newWebWalletServer returns a server for a web wallet of two receive and two change addresses.
func newWebWalletServer(t *testing.T) *server {
	t.Helper()
	mnemonic, err := libhtnwallet.CreateMnemonic()
	if err != nil {
		t.Fatalf("CreateMnemonic: %+v", err)
	}
	paths := []string{
		libhtnwallet.WebWalletPath(libhtnwallet.ExternalKeychain, 0), libhtnwallet.WebWalletPath(libhtnwallet.ExternalKeychain, 1),
		libhtnwallet.WebWalletPath(libhtnwallet.InternalKeychain, 0), libhtnwallet.WebWalletPath(libhtnwallet.InternalKeychain, 1),
	}
	return newImportedWalletServer(t, libhtnwallet.ImportedKeyTypeHTNWebWallet, mnemonic, paths)
}

func importedKeyP2PK(t *testing.T, s *server, importedKey *keys.ImportedKey) string {
	t.Helper()
	address, err := libhtnwallet.ImportedKeyAddress(s.params, importedKey.ExtendedPublicKey, libhtnwallet.SingleSigAddressTypeP2PK)
	if err != nil {
		t.Fatalf("ImportedKeyAddress: %+v", err)
	}
	return address.String()
}

// TestImportedWalletAddressesAreScannedAndTracked pins how the daemon finds an imported wallet's coins: a
// lone private key is tracked from the start, a web wallet key once the scan of the first batch reports
// it usable, and neither touches the htnwallet last used indexes.
func TestImportedWalletAddressesAreScannedAndTracked(t *testing.T) {
	s, rawKeyAddress := newPrivateKeyWalletServer(t)
	if _, ok := s.addressSet[rawKeyAddress]; !ok {
		t.Fatalf("the private key's address %s is not tracked from the start", rawKeyAddress)
	}

	s = newWebWalletServer(t)
	webWalletAddress := importedKeyP2PK(t, s, s.keysFile.ImportedKeys()[0])
	if len(s.addressSet) != 0 {
		t.Fatalf("unused web wallet addresses are tracked before the scan found them: %v", s.addressSet.strings())
	}
	// 4 keys in 3 forms each.
	if len(s.importedAddresses) != 12 {
		t.Fatalf("got %d imported addresses, want 12", len(s.importedAddresses))
	}

	firstBatch, err := s.addressesToQuery(0, numIndexesToQueryForRecentAddresses)
	if err != nil {
		t.Fatalf("addressesToQuery: %+v", err)
	}
	if len(firstBatch) != len(s.importedAddresses) {
		t.Fatalf("the first batch of an imported wallet scans %d addresses, want its %d", len(firstBatch), len(s.importedAddresses))
	}
	laterBatch, err := s.addressesToQuery(numIndexesToQueryForRecentAddresses, 2*numIndexesToQueryForRecentAddresses)
	if err != nil {
		t.Fatalf("addressesToQuery: %+v", err)
	}
	if len(laterBatch) != 0 {
		t.Fatalf("a later batch of an imported wallet scans %d addresses", len(laterBatch))
	}

	err = s.updateAddressesAndLastUsedIndexes(firstBatch, &appmessage.GetUsableAddressesResponseMessage{
		Addresses: []string{webWalletAddress},
	})
	if err != nil {
		t.Fatalf("updateAddressesAndLastUsedIndexes: %+v", err)
	}
	if tracked, ok := s.addressSet[webWalletAddress]; !ok || tracked.imported == nil {
		t.Fatalf("a usable web wallet address is not tracked as an imported key")
	}
	if s.keysFile.LastUsedExternalIndex() != 0 || s.keysFile.LastUsedInternalIndex() != 0 {
		t.Fatalf("an imported key moved the last used indexes to %d/%d",
			s.keysFile.LastUsedExternalIndex(), s.keysFile.LastUsedInternalIndex())
	}
}

// TestImportedWalletCoinsAreSpentByTheirKey pins that an imported wallet's coin is put in a transaction as
// spent by its imported key, not by a key derived at a path.
func TestImportedWalletCoinsAreSpentByTheirKey(t *testing.T) {
	s := newWebWalletServer(t)
	walletAddr := s.importedAddresses[importedKeyP2PK(t, s, s.keysFile.ImportedKeys()[0])]

	coin := &walletUTXO{
		Outpoint:  &externalapi.DomainOutpoint{Index: 1},
		UTXOEntry: utxo.NewUTXOEntry(5, &externalapi.ScriptPublicKey{}, false, 0),
		address:   walletAddr,
	}
	libhtnwalletUTXO, err := s.libhtnwalletUTXO(coin.Outpoint, coin.UTXOEntry, coin.address)
	if err != nil {
		t.Fatalf("libhtnwalletUTXO: %v", err)
	}
	if libhtnwalletUTXO.ImportedExtendedPublicKey != walletAddr.imported.ExtendedPublicKey ||
		libhtnwalletUTXO.DerivationPath != libhtnwallet.ImportedKeyDerivationPath {
		t.Fatalf("an imported key's coin became %+v", libhtnwalletUTXO)
	}
	if s.walletAddressLabel(walletAddr) != "imported web wallet "+libhtnwallet.WebWalletPath(libhtnwallet.ExternalKeychain, 0) {
		t.Fatalf("unexpected label %q", s.walletAddressLabel(walletAddr))
	}
}

// TestImportedWalletHandsOutItsOwnAddresses pins where new and change addresses of an imported wallet
// come from: a web wallet's next unused key of the right key chain, a lone private key's only address.
func TestImportedWalletHandsOutItsOwnAddresses(t *testing.T) {
	s := newWebWalletServer(t)
	importedKeys := s.keysFile.ImportedKeys()
	receive0, receive1 := importedKeyP2PK(t, s, importedKeys[0]), importedKeyP2PK(t, s, importedKeys[1])
	change0, change1 := importedKeyP2PK(t, s, importedKeys[2]), importedKeyP2PK(t, s, importedKeys[3])

	// receive0 already held coins.
	s.addressSet[receive0] = s.importedAddresses[receive0]

	response, err := s.NewAddress(context.Background(), &pb.NewAddressRequest{})
	if err != nil {
		t.Fatalf("NewAddress: %+v", err)
	}
	if response.Address != receive1 || response.P2PkAddress != receive1 || response.P2PkhAddress == "" || response.P2ShAddress == "" {
		t.Fatalf("NewAddress returned %+v, want the unused receive address %s", response, receive1)
	}
	_, err = s.NewAddress(context.Background(), &pb.NewAddressRequest{})
	if err == nil || !strings.Contains(err.Error(), "--num-addresses") {
		t.Fatalf("NewAddress with every receive address in use: got %v", err)
	}

	for _, want := range []string{change0, change1, change0} {
		address, _, err := s.changeAddress(false, nil, false)
		if err != nil {
			t.Fatalf("changeAddress: %+v", err)
		}
		if address.String() != want {
			t.Fatalf("got change address %s, want %s", address, want)
		}
	}

	fromAddress := s.addressSet[receive1]
	address, _, err := s.changeAddress(true, []*walletAddress{fromAddress}, false)
	if err != nil {
		t.Fatalf("changeAddress: %+v", err)
	}
	if address.String() != receive1 {
		t.Fatalf("change with --use-existing-change-address went to %s, not the from address %s", address, receive1)
	}

	s, rawKeyAddress := newPrivateKeyWalletServer(t)
	for range 2 {
		response, err := s.NewAddress(context.Background(), &pb.NewAddressRequest{})
		if err != nil {
			t.Fatalf("NewAddress: %+v", err)
		}
		if response.Address != rawKeyAddress {
			t.Fatalf("a private key wallet handed out %s, not its address %s", response.Address, rawKeyAddress)
		}
		address, _, err := s.changeAddress(false, nil, false)
		if err != nil {
			t.Fatalf("changeAddress: %+v", err)
		}
		if address.String() != rawKeyAddress {
			t.Fatalf("a private key wallet's change went to %s, not its address %s", address, rawKeyAddress)
		}
	}
}

// TestShowAddressesOfAnImportedWallet pins that show-addresses lists the imported wallet's tracked keys.
func TestShowAddressesOfAnImportedWallet(t *testing.T) {
	s, rawKeyAddress := newPrivateKeyWalletServer(t)
	response, err := s.ShowAddresses(context.Background(), &pb.ShowAddressesRequest{})
	if err != nil {
		t.Fatalf("ShowAddresses: %+v", err)
	}
	if !slices.Equal(response.Address, []string{rawKeyAddress}) {
		t.Fatalf("got %v, want the private key's %s", response.Address, rawKeyAddress)
	}
	response, err = s.ShowAddresses(context.Background(), &pb.ShowAddressesRequest{IncludeAll: true})
	if err != nil {
		t.Fatalf("ShowAddresses: %+v", err)
	}
	if len(response.Address) != 3 || response.Address[0] != rawKeyAddress {
		t.Fatalf("got %v, want the private key's three address forms", response.Address)
	}

	s = newWebWalletServer(t)
	importedKeys := s.keysFile.ImportedKeys()
	usedAddress := importedKeyP2PK(t, s, importedKeys[1])
	s.addressSet[usedAddress] = s.importedAddresses[usedAddress]
	response, err = s.ShowAddresses(context.Background(), &pb.ShowAddressesRequest{})
	if err != nil {
		t.Fatalf("ShowAddresses: %+v", err)
	}
	if !slices.Equal(response.Address, []string{usedAddress}) {
		t.Fatalf("got %v, want only the used web wallet address %s", response.Address, usedAddress)
	}
}
