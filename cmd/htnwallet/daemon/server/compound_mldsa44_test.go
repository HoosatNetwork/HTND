package server

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/keys"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/libhtnwallet"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/utils"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/utxo"
	"github.com/HoosatNetwork/HTND/v2/util"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// TestCompoundKeepsMLDSA44CoinsOutOfOtherAddresses pins which coins a compound may take: ML-DSA-44
// coins only when it pays an ML-DSA-44 address or the user named the addresses to compound from.
//
// A compound pays all it spends to one address, and it used to take the wallet's coins by amount
// whatever their kind. So a compound to a secp256k1 address - an auto-compound set up before the
// wallet received ML-DSA-44 coins, for example - moved those coins to a secp256k1 address.
func TestCompoundKeepsMLDSA44CoinsOutOfOtherAddresses(t *testing.T) {
	schnorr := &walletAddress{index: 1, keyChain: libhtnwallet.ExternalKeychain}
	ml := &walletAddress{index: 1, keyChain: libhtnwallet.ExternalKeychain, mldsa44: true}
	mlP2SH := &walletAddress{
		index: 2, keyChain: libhtnwallet.ExternalKeychain, mldsa44: true, mldsa44Form: libhtnwallet.MLDSA44AddressFormP2SH,
	}

	w := newMLWallet(t, 1, 1)
	w.fund(t,
		coinSpec{schnorr, 50_000_000}, coinSpec{schnorr, 1_000_000}, coinSpec{schnorr, 2_000_000},
		coinSpec{ml, 60_000_000}, coinSpec{ml, 1_500_000}, coinSpec{mlP2SH, 2_500_000},
	)
	w.s.indexCompoundP2SHAddresses()
	// The batch a running daemon has scanned: it is what knows an own address that holds no coin yet.
	if _, err := w.s.addressesToQueryCached(0, numIndexesToQueryForRecentAddresses); err != nil {
		t.Fatalf("addressesToQueryCached: %+v", err)
	}
	foreignP2SH, err := util.NewAddressScriptHash([]byte{0x51}, w.params.Prefix)
	if err != nil {
		t.Fatalf("NewAddressScriptHash: %+v", err)
	}

	// selected returns how many ML-DSA-44 and how many other coins a compound to toAddress takes.
	selected := func(t *testing.T, toAddress util.Address, fromAddresses []*walletAddress) (mldsa44, other, leftOut int) {
		t.Helper()
		coins, leftOut, err := w.s.selectUTXOsForCompoundingAt(feeTestVirtualDAAScore, fromAddresses, toAddress)
		if err != nil {
			t.Fatalf("selectUTXOsForCompoundingAt: %+v", err)
		}
		for _, coin := range coins {
			if libhtnwallet.IsMLDSA44Coin(coin.UTXOEntry.ScriptPublicKey().Script, coin.RedeemScript) {
				mldsa44++
			} else {
				other++
			}
		}
		return mldsa44, other, leftOut
	}

	for name, test := range map[string]struct {
		toAddress                util.Address
		fromAddresses            []*walletAddress
		wantMLDSA44, wantLeftOut int
	}{
		"to a secp256k1 address of the wallet":       {w.address(t, schnorr), nil, 0, 3},
		"to a pay-to-script-hash address of another": {foreignP2SH, nil, 0, 3},
		"to an ML-DSA-44 address":                    {w.address(t, ml), nil, 3, 0},
		"to an ML-DSA-44 P2SH address of the wallet that holds no coin yet": {
			w.address(t, &walletAddress{index: 0, keyChain: libhtnwallet.ExternalKeychain, mldsa44: true, mldsa44Form: libhtnwallet.MLDSA44AddressFormP2SH}),
			nil, 3, 0,
		},
		"to a secp256k1 address, from addresses the user named": {w.address(t, schnorr), []*walletAddress{ml, mlP2SH, schnorr}, 3, 0},
	} {
		t.Run(name, func(t *testing.T) {
			mldsa44, other, leftOut := selected(t, test.toAddress, test.fromAddresses)
			if mldsa44 != test.wantMLDSA44 || other != 3 || leftOut != test.wantLeftOut {
				t.Fatalf("the compound takes %d ML-DSA-44 and %d other coins and leaves %d out; want %d, 3 and %d",
					mldsa44, other, leftOut, test.wantMLDSA44, test.wantLeftOut)
			}
		})
	}

	// With only ML-DSA-44 coins there is nothing to compound into a secp256k1 address, and the error
	// path reports how many coins were passed over.
	w.fund(t, coinSpec{ml, 60_000_000}, coinSpec{ml, 1_500_000})
	_, leftOut, err := w.s.selectUTXOsForCompoundingAt(feeTestVirtualDAAScore, nil, w.address(t, schnorr))
	if err == nil || leftOut != 2 {
		t.Fatalf("got err %v and %d coins left out, want a refusal and 2", err, leftOut)
	}
}

func TestCompoundRecognizesUnscannedOwnedMLDSA44P2SH(t *testing.T) {
	for _, tt := range []struct {
		name      string
		cosigners int
		keychain  uint8
	}{
		{"single-sig external", 1, libhtnwallet.ExternalKeychain},
		{"multisig internal", 2, libhtnwallet.InternalKeychain},
	} {
		t.Run(tt.name, func(t *testing.T) {
			w := newMLWallet(t, tt.cosigners, uint32(tt.cosigners))
			const index = 1000
			for i, mnemonic := range w.mnemonics {
				pool, err := keys.NewMLDSA44KeyPool(mnemonic, index+1, tt.cosigners > 1)
				if err != nil {
					t.Fatal(err)
				}
				if tt.cosigners == 1 {
					w.s.keysFile.MLDSA44 = pool
				} else {
					w.s.keysFile.MLDSA44Cosigners[w.s.keysFile.ExtendedPublicKeys[i]] = pool
				}
			}
			w.s.indexCompoundP2SHAddresses()
			if _, err := w.s.addressesToQueryCached(0, numIndexesToQueryForRecentAddresses); err != nil {
				t.Fatal(err)
			}
			destination := w.address(t, &walletAddress{
				index: index, keyChain: tt.keychain, mldsa44: true, mldsa44Form: libhtnwallet.MLDSA44AddressFormP2SH,
			})
			if _, tracked := w.s.addressSet[destination.String()]; tracked {
				t.Fatal("fixture's destination should be unfunded")
			}
			for _, batch := range w.s.recentAddressBatches {
				if _, cached := batch[destination.String()]; cached {
					t.Fatal("fixture's destination should be unscanned")
				}
			}
			input := &walletAddress{index: 1, keyChain: libhtnwallet.ExternalKeychain, mldsa44: true}
			w.fund(t, coinSpec{input, 12_000_000}, coinSpec{input, 3_000_000})
			selected, skipped, err := w.s.selectUTXOsForCompoundingAt(feeTestVirtualDAAScore, nil, destination)
			if err != nil || len(selected) != 2 || skipped != 0 {
				t.Fatalf("unscanned ML-DSA-44 destination selected %d, skipped %d with %v", len(selected), skipped, err)
			}
		})
	}
}

func TestCompoundSkipCountExcludesUnavailableCoins(t *testing.T) {
	w := newMLWallet(t, 1, 1)
	schnorr := &walletAddress{index: 1, keyChain: libhtnwallet.ExternalKeychain}
	ml := &walletAddress{index: 1, keyChain: libhtnwallet.ExternalKeychain, mldsa44: true}
	w.fund(t, coinSpec{schnorr, 60_000_000}, coinSpec{schnorr, 2_000_000},
		coinSpec{ml, 50_000_000}, coinSpec{ml, 30_000_000}, coinSpec{ml, 10_000_000}, coinSpec{ml, 1_000_000})
	now := time.Now()
	w.s.startTimeOfLastCompletedRefresh = now
	w.s.usedOutpoints = map[externalapi.DomainOutpoint]time.Time{
		*w.s.utxosSortedByAmount[2].Outpoint: now,
		*w.s.utxosSortedByAmount[3].Outpoint: now.Add(-2 * time.Hour),
	}
	young := w.s.utxosSortedByAmount[4]
	young.UTXOEntry = utxo.NewUTXOEntry(young.UTXOEntry.Amount(), young.UTXOEntry.ScriptPublicKey(), false, feeTestVirtualDAAScore-1)
	w.s.coinbaseMaturity = feeTestVirtualDAAScore
	immature := w.s.utxosSortedByAmount[5]
	immature.UTXOEntry = utxo.NewUTXOEntry(immature.UTXOEntry.Amount(), immature.UTXOEntry.ScriptPublicKey(), true, 1)

	coins, skipped, err := w.s.selectUTXOsForCompoundingAt(feeTestVirtualDAAScore, nil, w.address(t, schnorr))
	if err != nil || len(coins) != 2 || skipped != 1 {
		t.Fatalf("selected %d, skipped %d with %v, want two classical coins and one otherwise eligible ML-DSA-44 coin", len(coins), skipped, err)
	}
}

func TestCompoundDustErrorIncludesMLDSA44SkipReason(t *testing.T) {
	w := newMLWallet(t, 1, 1)
	schnorr := &walletAddress{index: 1, keyChain: libhtnwallet.ExternalKeychain}
	ml := &walletAddress{index: 1, keyChain: libhtnwallet.ExternalKeychain, mldsa44: true}
	w.fund(t, coinSpec{schnorr, 10_001}, coinSpec{schnorr, 10_001}, coinSpec{ml, 100_000_000})
	foreignP2SH, err := util.NewAddressScriptHash([]byte{0x51}, w.params.Prefix)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name        string
		destination util.Address
		reason      string
	}{
		{"classical", w.address(t, schnorr), "is a classical address"},
		{"foreign P2SH", foreignP2SH, "could not be verified as an ML-DSA-44 address"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			selected, skipped, err := w.s.selectUTXOsForCompoundingAt(feeTestVirtualDAAScore, nil, tt.destination)
			if err != nil || len(selected) != 2 || skipped != 1 {
				t.Fatalf("selected %d, skipped %d with %v, want two classical coins and one skip", len(selected), skipped, err)
			}
			txs, err := w.s.finishCompound(context.Background(), selected, tt.destination, skipped)
			if len(txs) != 0 || err == nil || !strings.Contains(err.Error(), "non-dust") ||
				!strings.Contains(err.Error(), "1 ML-DSA-44 coins were left out") || !strings.Contains(err.Error(), tt.reason) {
				t.Fatalf("got %d transactions and %v, want dust error with the ML-DSA-44 skip reason", len(txs), err)
			}
			if len(w.s.usedOutpoints) != 0 {
				t.Fatal("failed compound reserved inputs")
			}
		})
	}
}

func TestCompoundTracksUnscannedOwnedP2SH(t *testing.T) {
	w := newMLWallet(t, 1, 1)
	const index = 2000
	pool := w.s.keysFile.MLDSA44
	extendCompoundTestPool(pool, index+1)
	hashes, err := libhtnwallet.MLDSA44PublicKeyHashes(w.mnemonics[0], libhtnwallet.ExternalKeychain, index, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	pool.ExternalPublicKeyHashes[index] = hashes[0]
	w.s.indexCompoundP2SHAddresses()
	if err := w.s.keysFile.Save(); err != nil {
		t.Fatal(err)
	}
	path := walletAddress{index: index, keyChain: libhtnwallet.ExternalKeychain, mldsa44: true,
		mldsa44Form: libhtnwallet.MLDSA44AddressFormP2SH}
	destination := w.address(t, &path)
	w.s.nextSyncStartIndex = 2100
	w.s.usedOutpoints = make(map[externalapi.DomainOutpoint]time.Time)
	ml := &walletAddress{index: 1, keyChain: libhtnwallet.ExternalKeychain, mldsa44: true}
	w.fund(t, coinSpec{ml, 12_000_000}, coinSpec{ml, 3_000_000})

	selected, skipped, err := w.s.selectUTXOsForCompoundingAt(feeTestVirtualDAAScore, nil, destination)
	if err != nil || len(selected) != 2 || skipped != 0 {
		t.Fatalf("selected %d, skipped %d with %v", len(selected), skipped, err)
	}
	txs, err := w.s.finishCompound(context.Background(), selected, destination, skipped)
	wantOutputs(t, w.check(txs, err), 14_980_000)
	if tracked := w.s.addressSet[destination.String()]; tracked == nil || *tracked != path {
		t.Errorf("destination is absent or wrong in the balance and UTXO query set: %+v", tracked)
	}
	saved, err := keys.ReadKeysFile(w.params, w.s.keysFile.Path())
	if err != nil {
		t.Fatal(err)
	}
	restarted := &server{keysFile: saved, nextSyncStartIndex: w.s.nextSyncStartIndex}
	if saved.LastUsedExternalIndex() != index || saved.LastUsedInternalIndex() != 0 ||
		!restarted.shouldPollRecentBatch(index, false) {
		t.Fatalf("the saved external/internal indexes %d/%d do not keep the destination in the recent scan",
			saved.LastUsedExternalIndex(), saved.LastUsedInternalIndex())
	}
}

func TestCompoundRepeatedUnknownP2SHDoesNotWalkKeyPools(t *testing.T) {
	w := newMLWallet(t, 1, 1)
	extendCompoundTestPool(w.s.keysFile.MLDSA44, 3000)
	w.s.indexCompoundP2SHAddresses()
	destination, err := util.NewAddressScriptHash([]byte{0x51}, w.params.Prefix)
	if err != nil {
		t.Fatal(err)
	}
	schnorr := &walletAddress{index: 1, keyChain: libhtnwallet.ExternalKeychain}
	ml := &walletAddress{index: 1, keyChain: libhtnwallet.ExternalKeychain, mldsa44: true}
	classical := []coinSpec{{schnorr, 12_000_000}, {schnorr, 3_000_000}}
	for _, tc := range []struct {
		name    string
		coins   []coinSpec
		skipped int
	}{
		{"classical only", classical, 0},
		{"with an eligible ML-DSA-44 input", append(classical, coinSpec{ml, 3_000_000}), 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w.fund(t, tc.coins...)
			allocs := testing.AllocsPerRun(3, func() {
				selected, skipped, err := w.s.selectUTXOsForCompoundingAt(feeTestVirtualDAAScore, nil, destination)
				if err != nil || len(selected) != 2 || skipped != tc.skipped {
					t.Fatalf("selected %d, skipped %d with %v", len(selected), skipped, err)
				}
			})
			// A lookup need not allocate per derivation on either chain. Allow selection overhead.
			if allocs > 100 {
				t.Fatalf("each unknown P2SH lookup allocated %.0f times", allocs)
			}
		})
	}
}

func extendCompoundTestPool(pool *keys.MLDSA44KeyPool, size int) {
	for i := len(pool.ExternalPublicKeyHashes); i < size; i++ {
		external := sha256.Sum256([]byte(fmt.Sprintf("external %d", i)))
		internal := sha256.Sum256([]byte(fmt.Sprintf("internal %d", i)))
		pool.ExternalPublicKeyHashes = append(pool.ExternalPublicKeyHashes, external[:])
		pool.InternalPublicKeyHashes = append(pool.InternalPublicKeyHashes, internal[:])
	}
}

type compoundTrailerRecorder struct {
	grpc.ServerTransportStream
	trailer metadata.MD
}

func (s *compoundTrailerRecorder) SetTrailer(trailer metadata.MD) error {
	s.trailer = trailer
	return nil
}

func TestCompoundSuccessSetsSkipTrailer(t *testing.T) {
	w := newMLWallet(t, 1, 1)
	w.s.usedOutpoints = make(map[externalapi.DomainOutpoint]time.Time)
	schnorr := &walletAddress{index: 1, keyChain: libhtnwallet.ExternalKeychain}
	ml := &walletAddress{index: 1, keyChain: libhtnwallet.ExternalKeychain, mldsa44: true}
	w.fund(t, coinSpec{schnorr, 12_000_000}, coinSpec{schnorr, 3_000_000}, coinSpec{ml, 6_000_000})
	destination := w.address(t, schnorr)
	selected, skipped, err := w.s.selectUTXOsForCompoundingAt(feeTestVirtualDAAScore, nil, destination)
	if err != nil || len(selected) != 2 || skipped != 1 {
		t.Fatalf("selected %d, skipped %d with %v", len(selected), skipped, err)
	}
	stream := &compoundTrailerRecorder{}
	ctx := grpc.NewContextWithServerTransportStream(context.Background(), stream)
	txs, err := w.s.finishCompound(ctx, selected, destination, skipped)
	if err != nil || len(txs) != 1 {
		t.Fatalf("finished with %d transactions and %v", len(txs), err)
	}
	notices := stream.trailer.Get(utils.CompoundSkipTrailer)
	if len(notices) != 1 || !strings.Contains(notices[0], "1 ML-DSA-44 coins were left out") ||
		!strings.Contains(notices[0], destination.String()+" is a classical address") {
		t.Fatalf("the response trailer should include the skip count and reason, got %q", notices)
	}
}
