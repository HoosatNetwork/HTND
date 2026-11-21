package server

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/daemon/pb"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/keys"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/libhtnwallet"
)

func TestMLDSA44ScanGapKeepsCosignerIdentityAfterAddressIssuance(t *testing.T) {
	w := newMLWallet(t, 3, 2)
	file := w.s.keysFile
	allPools := file.MLDSA44Cosigners
	xpubs := slices.Clone(file.ExtendedPublicKeys)
	slices.Sort(xpubs)
	peer, local := xpubs[0], xpubs[2]
	localIndex, err := libhtnwallet.MinimumCosignerIndex([]string{local}, xpubs)
	if err != nil {
		t.Fatal(err)
	}
	// The daemon only needs the number of mnemonics and the persisted cosigner index.
	file.EncryptedMnemonics = []*keys.EncryptedMnemonic{{}}
	file.CosignerIndex = localIndex
	w.s.firstSyncDone.Store(true)
	w.s.nextSyncStartIndex = 10

	for _, scenario := range []struct {
		name, poolKey string
		partial       bool
	}{
		{"peer pool only", peer, true},
		{"local pool only", local, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			createdOrder := []string{local, peer, xpubs[1]}
			file.ExtendedPublicKeys = slices.Clone(createdOrder)
			file.MLDSA44Cosigners = map[string]*keys.MLDSA44KeyPool{scenario.poolKey: allPools[scenario.poolKey]}
			w.s.keysFile = file
			if gap, partial := w.s.mldsa44ScanGap(); gap == "" || partial != scenario.partial {
				t.Errorf("before address issuance: partial=%t, gap=%q", partial, gap)
			}
			for range 2 {
				if _, err := w.s.NewAddress(context.Background(), &pb.NewAddressRequest{}); err != nil {
					t.Fatal(err)
				}
			}
			reloaded, err := keys.ReadKeysFile(w.params, file.Path())
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(reloaded.ExtendedPublicKeys, createdOrder) {
				t.Error("address issuance changed the persisted cosigner order")
			}
			reloaded.ExtendedPublicKeys = slices.Clone(createdOrder)
			if _, err := libhtnwallet.CreateUnsignedTransaction(reloaded.ExtendedPublicKeys, reloaded.MinimumSignatures, nil, nil, nil); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(reloaded.ExtendedPublicKeys, createdOrder) {
				t.Error("unsigned transaction construction changed the cosigner order")
			}
			// Older daemons could already have persisted the sorted order.
			reloaded.ExtendedPublicKeys = slices.Clone(xpubs)
			w.s.keysFile = reloaded
			if gap, partial := w.s.mldsa44ScanGap(); gap == "" || partial != scenario.partial {
				t.Errorf("on a previously reordered wallet: partial=%t, gap=%q", partial, gap)
			}
		})
	}
}

func TestMLDSA44ScanGapUsesNeutralNoticeForAmbiguousLocalCosigners(t *testing.T) {
	w := newMLWallet(t, 3, 2)
	file := w.s.keysFile
	file.EncryptedMnemonics = []*keys.EncryptedMnemonic{{}, {}}
	slices.Sort(file.ExtendedPublicKeys)
	// In a reordered wallet, index 0 is local and either remaining key could be the second local key.
	possibleLocal := file.ExtendedPublicKeys[2]
	file.MLDSA44Cosigners = map[string]*keys.MLDSA44KeyPool{possibleLocal: file.MLDSA44Cosigners[possibleLocal]}
	gap, partial := w.s.mldsa44ScanGap()
	if partial || !strings.Contains(gap, "2 of the wallet's 3 cosigners are missing") ||
		!strings.Contains(gap, "If a local pool is missing") {
		t.Fatalf("want neutral coverage and conditional local recovery advice, got partial=%t, gap=%q", partial, gap)
	}
}
