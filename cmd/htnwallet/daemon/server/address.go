package server

import (
	"context"
	"fmt"

	"github.com/Hoosat-Oy/HTND/cmd/htnwallet/daemon/pb"
	"github.com/Hoosat-Oy/HTND/cmd/htnwallet/libhtnwallet"
	"github.com/Hoosat-Oy/HTND/util"
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
	return address, walletAddr, nil
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
		if request.GetIncludeBoth() && !s.isMultisig() {
			addressStrings, err := s.walletAddressStringsForScan(walletAddr)
			if err != nil {
				return nil, err
			}
			addresses = append(addresses, addressStrings...)
			continue
		}

		path := s.walletAddressPath(walletAddr)
		// Default to P2PK for single-sig; multisig always returns P2SH.
		singleSigType := libhtnwallet.SingleSigAddressTypeP2PK
		switch request.GetAddressType() {
		case pb.AddressType_ADDRESS_TYPE_P2PK:
			singleSigType = libhtnwallet.SingleSigAddressTypeP2PK
		case pb.AddressType_ADDRESS_TYPE_P2PKH:
			singleSigType = libhtnwallet.SingleSigAddressTypeP2PKH
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

	var primary string
	switch request.GetAddressType() {
	case pb.AddressType_ADDRESS_TYPE_P2PK:
		primary = addrP2PK.String()
	case pb.AddressType_ADDRESS_TYPE_P2PKH:
		primary = addrP2PKH.String()
	default:
		primary = addrP2PK.String()
	}

	return &pb.NewAddressResponse{
		Address:      primary,
		P2PkAddress:  addrP2PK.String(),
		P2PkhAddress: addrP2PKH.String(),
	}, nil
}

func (s *server) walletAddressString(wAddr *walletAddress) (string, error) {
	path := s.walletAddressPath(wAddr)
	addr, err := libhtnwallet.Address(s.params, s.keysFile.ExtendedPublicKeys, s.keysFile.MinimumSignatures, path, s.keysFile.ECDSA)
	if err != nil {
		return "", err
	}

	return addr.String(), nil
}

// walletAddressStringsForScan returns all address encodings that should be queried
// for a given wallet derivation path.
//
// For single-sig wallets, this includes both legacy P2PK and modern P2PKH encodings
// so that upgrading the wallet does not "lose" old funds.
func (s *server) walletAddressStringsForScan(wAddr *walletAddress) ([]string, error) {
	path := s.walletAddressPath(wAddr)

	if s.isMultisig() {
		addr, err := libhtnwallet.Address(s.params, s.keysFile.ExtendedPublicKeys, s.keysFile.MinimumSignatures, path, s.keysFile.ECDSA)
		if err != nil {
			return nil, err
		}
		return []string{addr.String()}, nil
	}

	addrP2PK, err := libhtnwallet.AddressWithSingleSigAddressType(
		s.params,
		s.keysFile.ExtendedPublicKeys,
		s.keysFile.MinimumSignatures,
		path,
		s.keysFile.ECDSA,
		libhtnwallet.SingleSigAddressTypeP2PK,
	)
	if err != nil {
		return nil, err
	}

	addrP2PKH, err := libhtnwallet.AddressWithSingleSigAddressType(
		s.params,
		s.keysFile.ExtendedPublicKeys,
		s.keysFile.MinimumSignatures,
		path,
		s.keysFile.ECDSA,
		libhtnwallet.SingleSigAddressTypeP2PKH,
	)
	if err != nil {
		return nil, err
	}

	return []string{addrP2PK.String(), addrP2PKH.String()}, nil
}

func (s *server) walletAddressPath(wAddr *walletAddress) string {
	if s.isMultisig() {
		return fmt.Sprintf("m/%d/%d/%d", wAddr.cosignerIndex, wAddr.keyChain, wAddr.index)
	}
	return fmt.Sprintf("m/%d/%d", wAddr.keyChain, wAddr.index)
}

func (s *server) isMultisig() bool {
	return len(s.keysFile.ExtendedPublicKeys) > 1
}
