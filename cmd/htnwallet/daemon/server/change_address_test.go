package server

import (
	"context"
	"math"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/daemon/pb"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/keys"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/libhtnwallet"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
	"github.com/HoosatNetwork/HTND/v2/util"
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

	address, walletAddr, err := s.deriveChangeAddress(false, false)
	if err != nil {
		t.Fatalf("changeAddress: %+v", err)
	}
	if walletAddr.keyChain != libhtnwallet.InternalKeychain || walletAddr.index != 1 {
		t.Fatalf("got change address %+v, want internal index 1", walletAddr)
	}
	if err := s.commitChange(&changeOutput{address: address, walletAddr: walletAddr, fresh: true}); err != nil {
		t.Fatalf("commitChange: %+v", err)
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

	_, walletAddr, err := s.deriveChangeAddress(true, true)
	if err != nil {
		t.Fatalf("an existing ML-DSA-44 change address was refused: %+v", err)
	}
	if walletAddr.index != 0 || walletAddr.keyChain != libhtnwallet.InternalKeychain || !walletAddr.mldsa44 {
		t.Fatalf("an existing ML-DSA-44 change address is %+v, want internal index 0", walletAddr)
	}

	_, _, err = s.deriveChangeAddress(false, true)
	if err == nil {
		t.Fatalf("a fresh ML-DSA-44 change address past the key pool was handed out")
	}
	if got := keysFile.LastUsedInternalIndex(); got != poolSize-1 {
		t.Fatalf("a refused change address moved the internal index to %d, want %d", got, poolSize-1)
	}
}

// issueTestChangeAddress derives and commits change as the send handler does.
func issueTestChangeAddress(s *server, mldsa44 bool) (util.Address, *walletAddress, error) {
	address, walletAddr, err := s.deriveChangeAddress(false, mldsa44)
	if err != nil {
		return nil, nil, err
	}
	if err := s.commitChange(&changeOutput{address: address, walletAddr: walletAddr, fresh: true}); err != nil {
		return nil, nil, err
	}
	return address, walletAddr, nil
}

// With no positive discovery, the next address on a chain with an unscannable counter is at index 1.
func TestNewAddressesWithImpossibleLastUsedIndex(t *testing.T) {
	params := &dagconfig.TestnetParams
	mnemonic, err := libhtnwallet.CreateMnemonic()
	if err != nil {
		t.Fatalf("CreateMnemonic: %+v", err)
	}
	extendedPublicKey, err := libhtnwallet.MasterPublicKeyFromMnemonic(params, mnemonic, false)
	if err != nil {
		t.Fatalf("MasterPublicKeyFromMnemonic: %+v", err)
	}
	// 4294967295 wrapped to index 0 when 1 was added to it.
	for _, recorded := range []uint32{1 << 31, 4294966296, math.MaxUint32} {
		keysFile := &keys.File{ExtendedPublicKeys: []string{extendedPublicKey}, MinimumSignatures: 1}
		if err := keysFile.SetPath(params, filepath.Join(t.TempDir(), "keys.json"), true); err != nil {
			t.Fatalf("SetPath: %+v", err)
		}
		if err := keysFile.SetLastUsedExternalIndex(recorded); err != nil {
			t.Fatalf("SetLastUsedExternalIndex: %+v", err)
		}
		if err := keysFile.SetLastUsedInternalIndex(recorded); err != nil {
			t.Fatalf("SetLastUsedInternalIndex: %+v", err)
		}
		s := &server{params: params, keysFile: keysFile, addressSet: make(walletAddressSet),
			nextSyncStartIndex: numIndexesToQueryForRecentAddresses}
		s.firstSyncDone.Store(true)

		_, walletAddr, err := issueTestChangeAddress(s, false)
		if err != nil {
			t.Fatalf("recorded %d: changeAddress: %+v", recorded, err)
		}
		if walletAddr.keyChain != libhtnwallet.InternalKeychain || walletAddr.index != 1 || keysFile.LastUsedInternalIndex() != 1 {
			t.Fatalf("recorded %d: got change address %+v and recorded internal index %d, want index 1",
				recorded, walletAddr, keysFile.LastUsedInternalIndex())
		}
		requireSavedAddressIndexes(t, s, recorded, 1)

		response, err := s.NewAddress(context.Background(), &pb.NewAddressRequest{})
		if err != nil {
			t.Fatalf("recorded %d: NewAddress: %+v", recorded, err)
		}
		want, err := libhtnwallet.AddressWithSingleSigAddressType(params, keysFile.ExtendedPublicKeys, 1,
			libhtnwallet.WalletAddressPath(false, 0, libhtnwallet.ExternalKeychain, 1), false, libhtnwallet.SingleSigAddressTypeP2PK)
		if err != nil {
			t.Fatalf("AddressWithSingleSigAddressType: %+v", err)
		}
		if response.Address != want.String() || keysFile.LastUsedExternalIndex() != 1 {
			t.Fatalf("recorded %d: got receive address %s and recorded external index %d, want %s at index 1",
				recorded, response.Address, keysFile.LastUsedExternalIndex(), want)
		}
		requireSavedAddressIndexes(t, s, 1, 1)
	}
}

func requireSavedAddressIndexes(t *testing.T, s *server, externalIndex, internalIndex uint32) {
	t.Helper()
	saved, err := keys.ReadKeysFile(s.params, s.keysFile.Path())
	if err != nil {
		t.Fatalf("ReadKeysFile: %+v", err)
	}
	if gotExternal, gotInternal := saved.LastUsedExternalIndex(), saved.LastUsedInternalIndex(); gotExternal != externalIndex || gotInternal != internalIndex {
		t.Errorf("saved external and internal indexes are %d and %d, want %d and %d",
			gotExternal, gotInternal, externalIndex, internalIndex)
	}
}

// TestImportedWalletChangeAddressIgnoresLastUsedIndexes pins that an imported web wallet's change address
// does not depend on the htnwallet last used indexes, impossible ones included, and leaves them as they are.
func TestImportedWalletChangeAddressIgnoresLastUsedIndexes(t *testing.T) {
	s := newWebWalletServer(t)
	if err := s.keysFile.SetLastUsedInternalIndex(4294966296); err != nil {
		t.Fatalf("SetLastUsedInternalIndex: %+v", err)
	}
	address, walletAddr, err := issueTestChangeAddress(s, false)
	if err != nil {
		t.Fatalf("changeAddress: %+v", err)
	}
	if walletAddr.imported == nil || walletAddr.imported.Path != libhtnwallet.WebWalletPath(libhtnwallet.InternalKeychain, 0) {
		t.Fatalf("got change address %s at %+v, want the web wallet's first change key", address, walletAddr)
	}
	if got := s.keysFile.LastUsedInternalIndex(); got != 4294966296 {
		t.Fatalf("an imported wallet's change address changed the recorded internal index to %d", got)
	}
}

type dagInfoRPCClient struct {
	walletRPCClient
	score uint64
}

func (c *dagInfoRPCClient) Reconnect() error { return nil }

func (c *dagInfoRPCClient) GetBlockDAGInfo() (*appmessage.GetBlockDAGInfoResponseMessage, error) {
	return &appmessage.GetBlockDAGInfoResponseMessage{VirtualDAAScore: c.score}, nil
}

// Both ML-DSA-44 allocators recover to index 1 with a three-key pool and an unscannable counter.
func TestMLDSA44AddressesWithImpossibleLastUsedIndex(t *testing.T) {
	s := newMLWallet(t, 1, 1).s
	const recorded = 4294966296
	if err := s.keysFile.SetLastUsedExternalIndex(recorded); err != nil {
		t.Fatalf("SetLastUsedExternalIndex: %+v", err)
	}
	if err := s.keysFile.SetLastUsedInternalIndex(recorded); err != nil {
		t.Fatalf("SetLastUsedInternalIndex: %+v", err)
	}

	_, walletAddr, err := issueTestChangeAddress(s, true)
	if err != nil {
		t.Fatalf("changeAddress: %+v", err)
	}
	if !walletAddr.mldsa44 || walletAddr.index != 1 || s.keysFile.LastUsedInternalIndex() != 1 {
		t.Fatalf("got change address %+v and recorded internal index %d, want ML-DSA-44 index 1",
			walletAddr, s.keysFile.LastUsedInternalIndex())
	}
	requireSavedAddressIndexes(t, s, recorded, 1)

	s.nextSyncStartIndex = numIndexesToQueryForRecentAddresses
	s.firstSyncDone.Store(true)
	activationVersion := s.params.HardForkGates.MLDSA44SignaturesBlockVersion
	activationScore := s.params.POWScores[activationVersion-2]
	node := &dagInfoRPCClient{score: activationScore - 1}
	s.rpcClient = node
	request := &pb.NewAddressRequest{AddressType: pb.AddressType_ADDRESS_TYPE_MLDSA44}
	response, err := s.NewAddress(context.Background(), request)
	if response != nil || err == nil || !strings.Contains(err.Error(), "ML-DSA-44 is not active") {
		t.Errorf("NewAddress before activation returned %v, %v, want an activation error", response, err)
	}
	requireSavedAddressIndexes(t, s, recorded, 1)

	node.score = activationScore
	response, err = s.NewAddress(context.Background(), request)
	if err != nil {
		t.Fatalf("NewAddress at activation: %+v", err)
	}
	want, err := s.mldsa44Address(&walletAddress{index: 1, keyChain: libhtnwallet.ExternalKeychain, mldsa44: true,
		mldsa44Form: libhtnwallet.MLDSA44AddressFormP2PKH})
	if err != nil {
		t.Fatalf("mldsa44Address: %+v", err)
	}
	if response.Address != want.String() || s.keysFile.LastUsedExternalIndex() != 1 {
		t.Fatalf("got receive address %s and recorded external index %d, want %s at index 1",
			response.Address, s.keysFile.LastUsedExternalIndex(), want)
	}
	requireSavedAddressIndexes(t, s, 1, 1)
}

func TestScannableLastUsedIndexWithMaximumCounter(t *testing.T) {
	if got := scannableLastUsedIndex(math.MaxUint32); got != 0 {
		t.Fatalf("scannableLastUsedIndex(MaxUint32) = %d, want 0", got)
	}
}

func TestShowAddressesWithUnscannableExternalIndex(t *testing.T) {
	// Keep all inputs small so a missing guard fails without making listing iterate billions of times.
	previousLimit := scanIndexLimit
	scanIndexLimit = 2
	t.Cleanup(func() { scanIndexLimit = previousLimit })

	for _, request := range []struct {
		name        string
		addressType pb.AddressType
	}{
		{name: "default"},
		{name: "ML-DSA-44 only", addressType: pb.AddressType_ADDRESS_TYPE_MLDSA44},
	} {
		t.Run(request.name, func(t *testing.T) {
			s := newMLWallet(t, 1, 1).s
			s.nextSyncStartIndex = scanIndexLimit
			s.addressListingTimeout = maxAddressListingRPCTimeout
			s.rpcClient = &dagInfoRPCClient{score: math.MaxUint64}
			s.firstSyncDone.Store(true)
			for _, tc := range []struct {
				recorded uint32
				want     int
			}{{scanIndexLimit - 1, 1}, {scanIndexLimit, 0}, {scanIndexLimit + 1, 0}} {
				if err := s.keysFile.SetLastUsedExternalIndex(tc.recorded); err != nil {
					t.Fatalf("SetLastUsedExternalIndex: %+v", err)
				}
				response, err := s.ShowAddresses(context.Background(), &pb.ShowAddressesRequest{AddressType: request.addressType})
				if err != nil {
					t.Fatalf("ShowAddresses at recorded index %d: %+v", tc.recorded, err)
				}
				if got := len(response.Address); got != tc.want {
					t.Fatalf("ShowAddresses at recorded index %d returned %d addresses, want %d", tc.recorded, got, tc.want)
				}
				requireSavedAddressIndexes(t, s, tc.recorded, 0)
			}
		})
	}
}

func TestAddressIndexExhaustionDoesNotAdvanceCounters(t *testing.T) {
	const lastIndex = 1<<31 - 1
	const wantError = "wallet address index range exhausted"
	for _, tc := range []struct {
		name    string
		mldsa44 bool
	}{{"secp256k1", false}, {"ML-DSA-44", true}} {
		t.Run(tc.name, func(t *testing.T) {
			s := newMLWallet(t, 1, 1).s
			s.nextSyncStartIndex = lastIndex + 1
			s.firstSyncDone.Store(true)
			if err := s.keysFile.SetLastUsedInternalIndex(lastIndex); err != nil {
				t.Fatalf("SetLastUsedInternalIndex: %+v", err)
			}
			if err := s.keysFile.SetLastUsedExternalIndex(lastIndex); err != nil {
				t.Fatalf("SetLastUsedExternalIndex: %+v", err)
			}
			request := &pb.NewAddressRequest{}
			if tc.mldsa44 {
				request.AddressType = pb.AddressType_ADDRESS_TYPE_MLDSA44
				s.rpcClient = &dagInfoRPCClient{score: math.MaxUint64}
			}
			for attempt := 1; attempt <= 2; attempt++ {
				_, _, err := issueTestChangeAddress(s, tc.mldsa44)
				if err == nil || err.Error() != wantError {
					t.Errorf("attempt %d: changeAddress returned %v, want %q", attempt, err, wantError)
				}
				_, err = s.NewAddress(context.Background(), request)
				if err == nil || err.Error() != wantError {
					t.Errorf("attempt %d: receive allocation returned %v, want %q", attempt, err, wantError)
				}
				if gotExternal, gotInternal := s.keysFile.LastUsedExternalIndex(), s.keysFile.LastUsedInternalIndex(); gotExternal != lastIndex || gotInternal != lastIndex {
					t.Errorf("attempt %d: in-memory external and internal indexes are %d and %d, want %d",
						attempt, gotExternal, gotInternal, lastIndex)
				}
				requireSavedAddressIndexes(t, s, lastIndex, lastIndex)
			}
		})
	}
}

func TestAddressDerivationFailureDoesNotAdvanceCounters(t *testing.T) {
	s := newSchnorrWallet(t).s
	s.keysFile.ExtendedPublicKeys[0] = "invalid key"
	const recorded = 2
	if err := s.keysFile.SetLastUsedExternalIndex(recorded); err != nil {
		t.Fatalf("SetLastUsedExternalIndex: %+v", err)
	}
	if err := s.keysFile.SetLastUsedInternalIndex(recorded); err != nil {
		t.Fatalf("SetLastUsedInternalIndex: %+v", err)
	}
	s.nextSyncStartIndex = numIndexesToQueryForRecentAddresses
	s.firstSyncDone.Store(true)

	if _, _, err := issueTestChangeAddress(s, false); err == nil {
		t.Error("changeAddress succeeded with an invalid extended public key")
	}
	if _, err := s.NewAddress(context.Background(), &pb.NewAddressRequest{}); err == nil {
		t.Error("NewAddress succeeded with an invalid extended public key")
	}
	requireSavedAddressIndexes(t, s, recorded, recorded)
}

func TestAllocationUsesDiscoveredIndexOnEachChain(t *testing.T) {
	w := newSchnorrWallet(t)
	s := w.s
	const recorded = 1 << 31
	if err := s.keysFile.SetLastUsedExternalIndex(recorded); err != nil {
		t.Fatalf("SetLastUsedExternalIndex: %+v", err)
	}
	if err := s.keysFile.SetLastUsedInternalIndex(recorded); err != nil {
		t.Fatalf("SetLastUsedInternalIndex: %+v", err)
	}

	// The node reports a coin at external index 0 and another at a positive internal index.
	externalZero := &walletAddress{keyChain: external}
	internalFive := &walletAddress{index: 5, keyChain: internal}
	externalAddress := w.address(t, externalZero).String()
	internalAddress := w.address(t, internalFive).String()
	requested := walletAddressSet{externalAddress: externalZero, internalAddress: internalFive}
	usable := appmessage.NewGetUsableAddressesResponse([]string{externalAddress, internalAddress})
	if err := s.updateAddressesAndLastUsedIndexes(requested, usable); err != nil {
		t.Fatalf("updateAddressesAndLastUsedIndexes: %+v", err)
	}
	requireSavedAddressIndexes(t, s, recorded, 5)

	s.nextSyncStartIndex = numIndexesToQueryForRecentAddresses
	s.firstSyncDone.Store(true)
	response, err := s.NewAddress(context.Background(), &pb.NewAddressRequest{})
	if err != nil {
		t.Fatalf("NewAddress: %+v", err)
	}
	want := w.address(t, &walletAddress{index: 1, keyChain: external})
	if response.Address != want.String() {
		t.Errorf("NewAddress returned %s, want %s at external index 1", response.Address, want)
	}
	_, change, err := issueTestChangeAddress(s, false)
	if err != nil {
		t.Fatalf("changeAddress: %+v", err)
	}
	if change.index != 6 {
		t.Errorf("change uses index %d, want 6 after discovery", change.index)
	}
	requireSavedAddressIndexes(t, s, 1, 6)
}

func TestReceiveRecoveryUsesReportedDiscovery(t *testing.T) {
	const recorded = 1 << 31
	for _, tc := range []struct {
		name      string
		reportOld bool
		wantIndex uint32
	}{
		{name: "stale negative", wantIndex: 1},
		{name: "positive discovery", reportOld: true, wantIndex: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newSchnorrWallet(t)
			s := w.s
			if err := s.keysFile.SetLastUsedExternalIndex(recorded); err != nil {
				t.Fatalf("SetLastUsedExternalIndex: %+v", err)
			}

			// The old invoice at index 1 has been paid. The scan can still get a cached negative response.
			old := &walletAddress{index: 1, keyChain: external}
			oldAddress := w.address(t, old).String()
			var reported []string
			if tc.reportOld {
				reported = []string{oldAddress}
			}
			if err := s.updateAddressesAndLastUsedIndexes(walletAddressSet{oldAddress: old},
				appmessage.NewGetUsableAddressesResponse(reported)); err != nil {
				t.Fatalf("updateAddressesAndLastUsedIndexes: %+v", err)
			}
			if _, tracked := s.addressSet[oldAddress]; tracked != tc.reportOld {
				t.Errorf("old address tracked = %t, want %t", tracked, tc.reportOld)
			}

			s.nextSyncStartIndex = numIndexesToQueryForRecentAddresses
			s.firstSyncDone.Store(true)
			response, err := s.NewAddress(context.Background(), &pb.NewAddressRequest{})
			if err != nil {
				t.Fatalf("NewAddress: %+v", err)
			}
			want := w.address(t, &walletAddress{index: tc.wantIndex, keyChain: external})
			if response.Address != want.String() {
				t.Errorf("NewAddress returned %s, want %s at index %d", response.Address, want, tc.wantIndex)
			}
			requireSavedAddressIndexes(t, s, tc.wantIndex, 0)
		})
	}
}
