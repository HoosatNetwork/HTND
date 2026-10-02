package server

import (
	"strings"

	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/daemon/pb"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/keys"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/libhtnwallet"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
	"github.com/HoosatNetwork/HTND/v2/util"
	"github.com/pkg/errors"
)

// importedAddressSet returns every address of the keys file's imported keys, each in all the forms it can
// be paid in.
func importedAddressSet(params *dagconfig.Params, keysFile *keys.File) (walletAddressSet, error) {
	addresses := make(walletAddressSet)
	for _, importedKey := range keysFile.ImportedKeys() {
		walletAddr := &walletAddress{imported: importedKey}
		importedKeyAddresses, err := libhtnwallet.ImportedKeyAddresses(params, importedKey.ExtendedPublicKey)
		if err != nil {
			return nil, err
		}
		for _, address := range importedKeyAddresses {
			addresses[address.String()] = walletAddr
		}
	}
	return addresses, nil
}

// trackImportedKeys sets up the addresses of an imported wallet's keys; an htnwallet wallet has none.
//
// They are scanned with the batch starting at index 0 (addressesToQuery), which every sync rescans, so
// that, like an htnwallet wallet's addresses, they are tracked once they hold coins and coins reaching
// them later are found. A lone private key, such as a genkeypair key, is tracked from the start as well:
// it is the wallet's only address, so it is shown and read even while it is empty.
func (s *server) trackImportedKeys() error {
	importedAddresses, err := importedAddressSet(s.params, s.keysFile)
	if err != nil {
		return err
	}
	s.importedAddresses = importedAddresses

	for address, walletAddr := range importedAddresses {
		if walletAddr.imported.Type == libhtnwallet.ImportedKeyTypePrivateKey {
			s.addressSet[address] = walletAddr
		}
	}
	if len(importedAddresses) > 0 {
		log.Infof("Tracking %d imported keys", len(s.keysFile.ImportedKeys()))
	}
	return nil
}

// trackedImportedKeys returns the imported keys with an address in the tracked set, in keys file order.
// The caller holds s.lock.
func (s *server) trackedImportedKeys() []*keys.ImportedKey {
	tracked := make(map[*keys.ImportedKey]struct{})
	for _, walletAddr := range s.addressSet {
		if walletAddr.imported != nil {
			tracked[walletAddr.imported] = struct{}{}
		}
	}

	var importedKeys []*keys.ImportedKey
	for _, importedKey := range s.keysFile.ImportedKeys() {
		if _, ok := tracked[importedKey]; ok {
			importedKeys = append(importedKeys, importedKey)
		}
	}
	return importedKeys
}

// nextImportedWalletAddress returns the address an imported wallet hands out next in keyChain: for a web
// wallet, its first key in that key chain the daemon does not track yet, as the web wallet itself would;
// for a lone private key, that key, since it is the wallet's only one. The address is tracked from then
// on, so the next call moves on, and coins reaching it are seen at once. The caller holds s.lock.
//
// When every web wallet key in keyChain is in use, a receive address is refused - the wallet needs to be
// imported again with more addresses - while change goes back to the key chain's first key, rather than
// failing the send.
func (s *server) nextImportedWalletAddress(keyChain uint8) (util.Address, *walletAddress, error) {
	importedKeys := s.keysFile.ImportedKeys()
	tracked := make(map[*keys.ImportedKey]struct{})
	for _, importedKey := range s.trackedImportedKeys() {
		tracked[importedKey] = struct{}{}
	}

	var chosen *keys.ImportedKey
	if s.keysFile.Imported.Type == libhtnwallet.ImportedKeyTypePrivateKey {
		chosen = importedKeys[0]
	} else {
		keyChainPrefix := strings.TrimSuffix(libhtnwallet.WebWalletPath(keyChain, 0), "0'")
		var first *keys.ImportedKey
		for _, importedKey := range importedKeys {
			if !strings.HasPrefix(importedKey.Path, keyChainPrefix) {
				continue
			}
			if first == nil {
				first = importedKey
			}
			if _, ok := tracked[importedKey]; !ok {
				chosen = importedKey
				break
			}
		}
		if first == nil {
			return nil, nil, errors.Errorf("the imported wallet has no keys in key chain %d", keyChain)
		}
		if chosen == nil {
			if keyChain == libhtnwallet.ExternalKeychain {
				return nil, nil, errors.Errorf("all %d receive addresses of the imported web wallet are in use; "+
					"import it again with a larger --num-addresses", s.importedKeyCount(keyChainPrefix))
			}
			chosen = first
		}
	}

	address, err := libhtnwallet.ImportedKeyAddress(s.params, chosen.ExtendedPublicKey, libhtnwallet.SingleSigAddressTypeP2PK)
	if err != nil {
		return nil, nil, err
	}
	walletAddr, ok := s.importedAddresses[address.String()]
	if !ok {
		return nil, nil, errors.Errorf("imported address %s is not among the imported wallet's addresses", address)
	}
	s.trackChangeAddress(address, walletAddr)
	return address, walletAddr, nil
}

// importedWalletChangeAddress is changeAddress for an imported wallet: the change goes back to the first
// address it is sent from when useExisting is set, as for an htnwallet wallet, and otherwise to the
// imported wallet's next change address. The caller holds s.lock.
func (s *server) importedWalletChangeAddress(useExisting bool, fromAddresses []*walletAddress) (
	util.Address, *walletAddress, error,
) {
	if len(fromAddresses) == 0 || !useExisting {
		return s.nextImportedWalletAddress(libhtnwallet.InternalKeychain)
	}

	walletAddr := fromAddresses[0]
	address, err := libhtnwallet.ImportedKeyAddress(s.params, walletAddr.imported.ExtendedPublicKey,
		libhtnwallet.SingleSigAddressTypeP2PK)
	if err != nil {
		return nil, nil, err
	}
	s.trackChangeAddress(address, walletAddr)
	return address, walletAddr, nil
}

// showImportedWalletAddresses is ShowAddresses for an imported wallet: the addresses of its keys the
// daemon tracks - a lone private key's always, a web wallet's once they held coins or were handed out.
// The caller holds s.lock.
func (s *server) showImportedWalletAddresses(request *pb.ShowAddressesRequest) (*pb.ShowAddressesResponse, error) {
	addressType := singleSigAddressType(request.GetAddressType())
	addresses := make([]string, 0)
	for _, importedKey := range s.trackedImportedKeys() {
		if request.GetIncludeAll() {
			importedKeyAddresses, err := libhtnwallet.ImportedKeyAddresses(s.params, importedKey.ExtendedPublicKey)
			if err != nil {
				return nil, err
			}
			for _, address := range importedKeyAddresses {
				addresses = append(addresses, address.String())
			}
			continue
		}

		address, err := libhtnwallet.ImportedKeyAddress(s.params, importedKey.ExtendedPublicKey, addressType)
		if err != nil {
			return nil, err
		}
		addresses = append(addresses, address.String())
	}
	return &pb.ShowAddressesResponse{Address: addresses}, nil
}

// importedWalletNewAddress is NewAddress for an imported wallet: its next receive address
// (nextImportedWalletAddress). The caller holds s.lock.
func (s *server) importedWalletNewAddress(request *pb.NewAddressRequest) (*pb.NewAddressResponse, error) {
	_, walletAddr, err := s.nextImportedWalletAddress(libhtnwallet.ExternalKeychain)
	if err != nil {
		return nil, err
	}

	response := &pb.NewAddressResponse{}
	for addressType, field := range map[libhtnwallet.SingleSigAddressType]*string{
		libhtnwallet.SingleSigAddressTypeP2PK:  &response.P2PkAddress,
		libhtnwallet.SingleSigAddressTypeP2PKH: &response.P2PkhAddress,
		libhtnwallet.SingleSigAddressTypeP2SH:  &response.P2ShAddress,
	} {
		address, err := libhtnwallet.ImportedKeyAddress(s.params, walletAddr.imported.ExtendedPublicKey, addressType)
		if err != nil {
			return nil, err
		}
		*field = address.String()
		if addressType == singleSigAddressType(request.GetAddressType()) {
			response.Address = address.String()
		}
	}
	return response, nil
}

// singleSigAddressType is the address type a request asks for, P2PK by default.
func singleSigAddressType(addressType pb.AddressType) libhtnwallet.SingleSigAddressType {
	switch addressType {
	case pb.AddressType_ADDRESS_TYPE_P2PKH:
		return libhtnwallet.SingleSigAddressTypeP2PKH
	case pb.AddressType_ADDRESS_TYPE_P2SH:
		return libhtnwallet.SingleSigAddressTypeP2SH
	}
	return libhtnwallet.SingleSigAddressTypeP2PK
}

func (s *server) importedKeyCount(pathPrefix string) int {
	count := 0
	for _, importedKey := range s.keysFile.ImportedKeys() {
		if strings.HasPrefix(importedKey.Path, pathPrefix) {
			count++
		}
	}
	return count
}

// walletAddressLabel describes where an address's key is, for display: its derivation path in the
// wallet, or where an imported key came from.
func (s *server) walletAddressLabel(wAddr *walletAddress) string {
	if wAddr.imported == nil {
		return s.walletAddressPath(wAddr)
	}
	switch wAddr.imported.Type {
	case libhtnwallet.ImportedKeyTypePrivateKey:
		return "imported private key"
	case libhtnwallet.ImportedKeyTypeHTNWebWallet:
		return "imported web wallet " + wAddr.imported.Path
	}
	return "imported " + wAddr.imported.Type + " " + wAddr.imported.Path
}
