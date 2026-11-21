package server

import (
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/daemon/pb"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/keys"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/libhtnwallet"
)

func TestIssuedReceiveAddressesAppearInBalanceAndUTXOs(t *testing.T) {
	for _, tc := range []struct {
		name     string
		multisig bool
	}{
		{name: "single-sig"},
		{name: "multisig", multisig: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node := newRecentTestNode()
			s := newRescanTestServer(t, node, 0, 0)
			s.rpcClient = s.backgroundRPCClient.(walletRPCClient)
			setupReceiveTestKeys(t, s, tc.multisig)

			// Put the real addresses (all encodings) alongside the fixture's stand-ins.
			addresses, err := s.addressesToQuery(1, 4)
			if err != nil {
				t.Fatal(err)
			}
			for address, path := range addresses {
				s.recentAddressBatches[0][address] = path
			}
			collectRecentAt(t, s, node, -32*time.Second)
			collectRecentAt(t, s, node, 0)
			s.firstSyncDone.Store(true)

			want := make(map[string]string)
			for _, addressType := range []pb.AddressType{
				pb.AddressType_ADDRESS_TYPE_P2PKH,
				pb.AddressType_ADDRESS_TYPE_MLDSA44,
				pb.AddressType_ADDRESS_TYPE_MLDSA44_P2SH,
			} {
				reply, err := s.NewAddress(t.Context(), &pb.NewAddressRequest{AddressType: addressType})
				if err != nil {
					t.Fatal(err)
				}
				for _, address := range []string{reply.Address, reply.P2PkAddress, reply.P2PkhAddress, reply.P2ShAddress} {
					if address == "" {
						continue
					}
					path, ok := addresses[address]
					if !ok {
						t.Fatalf("issued address %s is missing from the scan fixture", address)
					}
					want[address] = s.walletAddressLabel(path)
					node.pay(address)
				}
			}
			wantCount := 5
			if tc.multisig {
				wantCount = 3
			}
			if len(want) != wantCount {
				t.Fatalf("NewAddress returned %d unique addresses, want %d", len(want), wantCount)
			}

			node.clearCache()
			setRecentTestTime(s, node, 6*time.Second)
			mustTick(t, s)

			balance, err := s.GetBalance(t.Context(), &pb.GetBalanceRequest{})
			if err != nil {
				t.Fatal(err)
			}
			if len(balance.AddressBalances) != len(want) || balance.Available != uint64(len(want)) {
				t.Errorf("balance shows %d addresses and %d available, want %d of each",
					len(balance.AddressBalances), balance.Available, len(want))
			}
			for _, entry := range balance.AddressBalances {
				if _, ok := want[entry.Address]; !ok || entry.Available != 1 {
					t.Errorf("unexpected balance entry %v", entry)
				}
			}

			utxos, err := s.GetUTXOs(t.Context(), &pb.GetUTXOsRequest{})
			if err != nil {
				t.Fatal(err)
			}
			if len(utxos.Utxos) != len(want) {
				t.Errorf("got %d UTXOs, want %d for the issued addresses", len(utxos.Utxos), len(want))
			}
			for _, coin := range utxos.Utxos {
				if path, ok := want[coin.Address]; !ok || path != coin.DerivationPath || coin.UtxoEntry.Amount != 1 {
					t.Errorf("unexpected coin or path %v", coin)
				}
			}
		})
	}
}

func setupReceiveTestKeys(t *testing.T, s *server, multisig bool) {
	t.Helper()
	s.keysFile.MLDSA44Cosigners = make(map[string]*keys.MLDSA44KeyPool)
	cosigners := 1
	if multisig {
		cosigners = 2
		s.keysFile.CosignerIndex = 1
		s.keysFile.MinimumSignatures = 2
	}
	for range cosigners {
		mnemonic, err := libhtnwallet.CreateMnemonic()
		if err != nil {
			t.Fatal(err)
		}
		publicKey, err := libhtnwallet.MasterPublicKeyFromMnemonic(s.params, mnemonic, multisig)
		if err != nil {
			t.Fatal(err)
		}
		pool, err := keys.NewMLDSA44KeyPool(mnemonic, 4, multisig)
		if err != nil {
			t.Fatal(err)
		}
		s.keysFile.ExtendedPublicKeys = append(s.keysFile.ExtendedPublicKeys, publicKey)
		if multisig {
			s.keysFile.MLDSA44Cosigners[publicKey] = pool
		} else {
			s.keysFile.MLDSA44 = pool
		}
	}
}
