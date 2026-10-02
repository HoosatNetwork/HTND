package server

import (
	"context"

	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/daemon/pb"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/libhtnwallet"
	"github.com/HoosatNetwork/HTND/v2/util"
	"github.com/pkg/errors"
)

func (s *server) changeAddress(useExisting bool, fromAddresses []*walletAddress) (util.Address, *walletAddress, error) {
	var walletAddr *walletAddress
	if len(fromAddresses) != 0 && useExisting {
		walletAddr = fromAddresses[0]
	} else {
		internalIndex := uint32(0)
		if !useExisting {
			err := s.keysFile.SetLastUsedInternalIndex(s.keysFile.LastUsedInternalIndex() + 1)
			if err != nil {
				return nil, nil, err
			}

			err = s.keysFile.Save()
			if err != nil {
				return nil, nil, err
			}

			internalIndex = s.keysFile.LastUsedInternalIndex()
		}

		walletAddr = &walletAddress{
			index:         internalIndex,
			cosignerIndex: s.keysFile.CosignerIndex,
			keyChain:      libhtnwallet.InternalKeychain,
		}
	}

	path := s.walletAddressPath(walletAddr)
	address, err := libhtnwallet.Address(s.params, s.keysFile.ExtendedPublicKeys, s.keysFile.MinimumSignatures, path, s.keysFile.ECDSA)
	if err != nil {
		return nil, nil, err
	}
	s.trackChangeAddress(address, walletAddr)
	return address, walletAddr, nil
}

// trackChangeAddress adds a change address to the addresses the wallet reads balances and UTXOs from,
// before any coin reaches it. The caller holds s.lock.
//
// The address scan only adds an address once the node reports it usable, which means it holds a coin,
// and the node caches that answer per address for 30 seconds - "not usable" answers included. The scan
// asks about every recent address every two seconds, so a fresh change address is cached as not usable
// when the change arrives. Once the payment was accepted, its input was gone while the change address
// was still outside the set: the balance dropped by the whole input for up to half a minute, and the
// change looked lost.
func (s *server) trackChangeAddress(address util.Address, walletAddr *walletAddress) {
	if s.addressSet == nil {
		s.addressSet = make(walletAddressSet)
	}
	s.addressSet[address.String()] = walletAddr
}

func (s *server) ShowAddresses(_ context.Context, request *pb.ShowAddressesRequest) (*pb.ShowAddressesResponse, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	if !s.isSynced() {
		return nil, errors.Errorf("wallet daemon is not synced yet, %s", s.formatSyncStateReport())
	}

	addresses := make([]string, 0)
	for i := uint32(1); i <= s.keysFile.LastUsedExternalIndex(); i++ {
		walletAddr := &walletAddress{
			index:         i,
			cosignerIndex: s.keysFile.CosignerIndex,
			keyChain:      libhtnwallet.ExternalKeychain,
		}
		if request.GetIncludeAll() && !s.isMultisig() {
			addressStrings, err := s.walletAddressStringsForScan(walletAddr)
			if err != nil {
				return nil, err
			}
			addresses = append(addresses, addressStrings...)
			continue
		}

		path := s.walletAddressPath(walletAddr)
		// Default to P2PK for single-sig; multisig always returns P2SH.
		var singleSigType libhtnwallet.SingleSigAddressType
		switch request.GetAddressType() {
		case pb.AddressType_ADDRESS_TYPE_P2PK:
			singleSigType = libhtnwallet.SingleSigAddressTypeP2PK
		case pb.AddressType_ADDRESS_TYPE_P2PKH:
			singleSigType = libhtnwallet.SingleSigAddressTypeP2PKH
		case pb.AddressType_ADDRESS_TYPE_P2SH:
			singleSigType = libhtnwallet.SingleSigAddressTypeP2SH
		default:
			singleSigType = libhtnwallet.SingleSigAddressTypeP2PK
		}

		address, err := libhtnwallet.AddressWithSingleSigAddressType(s.params, s.keysFile.ExtendedPublicKeys, s.keysFile.MinimumSignatures, path, s.keysFile.ECDSA, singleSigType)
		if err != nil {
			return nil, err
		}
		addresses = append(addresses, address.String())
	}

	return &pb.ShowAddressesResponse{Address: addresses}, nil
}

func (s *server) NewAddress(_ context.Context, request *pb.NewAddressRequest) (*pb.NewAddressResponse, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	if !s.isSynced() {
		return nil, errors.Errorf("wallet daemon is not synced yet, %s", s.formatSyncStateReport())
	}

	err := s.keysFile.SetLastUsedExternalIndex(s.keysFile.LastUsedExternalIndex() + 1)
	if err != nil {
		return nil, err
	}

	err = s.keysFile.Save()
	if err != nil {
		return nil, err
	}

	walletAddr := &walletAddress{
		index:         s.keysFile.LastUsedExternalIndex(),
		cosignerIndex: s.keysFile.CosignerIndex,
		keyChain:      libhtnwallet.ExternalKeychain,
	}
	path := s.walletAddressPath(walletAddr)
	if s.isMultisig() {
		address, err := libhtnwallet.Address(s.params, s.keysFile.ExtendedPublicKeys, s.keysFile.MinimumSignatures, path, s.keysFile.ECDSA)
		if err != nil {
			return nil, err
		}
		return &pb.NewAddressResponse{Address: address.String()}, nil
	}

	addrP2PK, err := libhtnwallet.AddressWithSingleSigAddressType(s.params, s.keysFile.ExtendedPublicKeys, s.keysFile.MinimumSignatures, path, s.keysFile.ECDSA, libhtnwallet.SingleSigAddressTypeP2PK)
	if err != nil {
		return nil, err
	}

	addrP2PKH, err := libhtnwallet.AddressWithSingleSigAddressType(s.params, s.keysFile.ExtendedPublicKeys, s.keysFile.MinimumSignatures, path, s.keysFile.ECDSA, libhtnwallet.SingleSigAddressTypeP2PKH)
	if err != nil {
		return nil, err
	}

	addrP2SH, err := libhtnwallet.AddressWithSingleSigAddressType(s.params, s.keysFile.ExtendedPublicKeys, s.keysFile.MinimumSignatures, path, s.keysFile.ECDSA, libhtnwallet.SingleSigAddressTypeP2SH)
	if err != nil {
		return nil, err
	}

	var primary string
	switch request.GetAddressType() {
	case pb.AddressType_ADDRESS_TYPE_P2PK:
		primary = addrP2PK.String()
	case pb.AddressType_ADDRESS_TYPE_P2PKH:
		primary = addrP2PKH.String()
	case pb.AddressType_ADDRESS_TYPE_P2SH:
		primary = addrP2SH.String()
	default:
		primary = addrP2PK.String()
	}

	return &pb.NewAddressResponse{
		Address:      primary,
		P2PkAddress:  addrP2PK.String(),
		P2PkhAddress: addrP2PKH.String(),
		P2ShAddress:  addrP2SH.String(),
	}, nil
}

// walletAddressStringsForScan returns all address encodings that should be queried
// for a given wallet derivation path.
//
// For single-sig wallets, this includes both legacy P2PK and modern P2PKH encodings
// so that upgrading the wallet does not "lose" old funds.
func (s *server) walletAddressStringsForScan(wAddr *walletAddress) ([]string, error) {
	addresses, err := libhtnwallet.WalletAddressesAtPath(s.params, s.keysFile.ExtendedPublicKeys,
		s.keysFile.MinimumSignatures, s.walletAddressPath(wAddr), s.keysFile.ECDSA)
	if err != nil {
		return nil, err
	}
	addressStrings := make([]string, len(addresses))
	for i, address := range addresses {
		addressStrings[i] = address.String()
	}
	return addressStrings, nil
}

func (s *server) walletAddressPath(wAddr *walletAddress) string {
	return libhtnwallet.WalletAddressPath(s.isMultisig(), wAddr.cosignerIndex, wAddr.keyChain, wAddr.index)
}

func (s *server) isMultisig() bool {
	return len(s.keysFile.ExtendedPublicKeys) > 1
}
