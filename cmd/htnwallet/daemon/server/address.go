package server

import (
	"context"

	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/daemon/pb"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/libhtnwallet"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/txscript"
	"github.com/HoosatNetwork/HTND/v2/util"
	"github.com/pkg/errors"
)

// changeAddress derives a candidate without issuing it. When all inputs are ML-DSA-44, it
// selects the corresponding change address so that change stays under the same key type.
func (s *server) changeAddress(useExisting bool, fromAddresses []*walletAddress, mldsa44 bool) (util.Address, *walletAddress, error) {
	if s.keysFile.IsImported() {
		return s.importedWalletChangeAddress(useExisting, fromAddresses)
	}

	var walletAddr *walletAddress
	if len(fromAddresses) != 0 && useExisting {
		walletAddr = fromAddresses[0]
	} else {
		internalIndex := uint32(0)
		if !useExisting {
			var err error
			internalIndex, err = nextIndex(s.keysFile.LastUsedInternalIndex())
			if err != nil {
				return nil, nil, err
			}
		}
		walletAddr = &walletAddress{
			index:         internalIndex,
			cosignerIndex: s.walletAddressCosignerIndex(mldsa44),
			keyChain:      libhtnwallet.InternalKeychain,
			mldsa44:       mldsa44,
		}
	}

	if walletAddr.mldsa44 {
		address, err := s.mldsa44Address(walletAddr)
		return address, walletAddr, err
	}

	path := s.walletAddressPath(walletAddr)
	address, err := libhtnwallet.Address(s.params, s.keysFile.ExtendedPublicKeys, s.keysFile.MinimumSignatures, path, s.keysFile.ECDSA)
	return address, walletAddr, err
}

type changeOutput struct {
	address    util.Address
	walletAddr *walletAddress
	fresh      bool
}

// commitChange tracks the change output and saves any new derivation index. Call it with
// s.lock held before the transaction can leave the daemon.
func (s *server) commitChange(change *changeOutput) error {
	if change == nil {
		return nil
	}
	if change.fresh && change.walletAddr.imported == nil {
		if err := s.keysFile.ReserveInternalIndex(change.walletAddr.index); err != nil {
			return err
		}
	}
	s.trackAddress(change.address.String(), change.walletAddr)
	return nil
}

// nextIndex uses the same effective last used index as the scan. A counter outside the scannable
// range is treated as unknown. A key chain that has reached the last scannable index is exhausted.
func nextIndex(lastUsedIndex uint32) (uint32, error) {
	index := scannableLastUsedIndex(lastUsedIndex) + 1
	if index >= scanIndexLimit {
		return 0, errors.New("wallet address index range exhausted")
	}
	return index, nil
}

// trackAddress adds an issued address to balance and UTXO queries so these reads
// can see incoming coins without waiting for discovery. The caller holds s.lock.
func (s *server) trackAddress(address string, walletAddr *walletAddress) {
	if s.addressSet == nil {
		s.addressSet = make(walletAddressSet)
	}
	s.addressSet[address] = walletAddr
}

// mldsa44Address returns the ML-DSA-44 address at wAddr's index and key chain, from the key pool the
// wallet precomputed (see keys.MLDSA44KeyPool).
func (s *server) mldsa44Address(wAddr *walletAddress) (util.Address, error) {
	if s.isMultisig() {
		redeemScript, err := s.mldsa44MultiSigRedeemScript(wAddr)
		if err != nil {
			return nil, err
		}
		return libhtnwallet.MLDSA44MultiSigAddress(s.params, redeemScript)
	}
	publicKeyHash, ok := s.keysFile.MLDSA44.PublicKeyHash(wAddr.keyChain, wAddr.index)
	if !ok {
		return nil, errors.Errorf("the wallet has ML-DSA-44 keys for indexes below %d only, and index %d was requested; "+
			"stop the daemon and run `htnwallet %s --count <n>` to generate more",
			s.keysFile.MLDSA44.Size(), wAddr.index, generateMLDSA44KeysSubCmdName)
	}
	switch wAddr.mldsa44Form {
	case libhtnwallet.MLDSA44AddressFormP2PKH:
		return libhtnwallet.MLDSA44Address(s.params, publicKeyHash)
	case libhtnwallet.MLDSA44AddressFormP2SH:
		return libhtnwallet.MLDSA44ScriptHashAddress(s.params, publicKeyHash)
	default:
		return nil, errors.Errorf("unknown ML-DSA-44 address form %s", wAddr.mldsa44Form)
	}
}

// generateMLDSA44KeysSubCmdName is the CLI command that fills keys.MLDSA44KeyPool; the daemon names it
// in errors but cannot run it, because it needs the password.
const generateMLDSA44KeysSubCmdName = "generate-mldsa44-keys"

// mldsa44MultiSigRedeemScript returns the ML-DSA-44 multisig redeem script at wAddr's index and key
// chain, from every cosigner's key pool.
//
// Unlike the secp256k1 multisig addresses, these do not give each cosigner its own m/<cosigner>/...
// path space: that would need every cosigner's key pool for every other cosigner's space as well.
// The cost is that two cosigners' daemons can hand out the same address - address reuse, not a loss
// of funds.
func (s *server) mldsa44MultiSigRedeemScript(wAddr *walletAddress) ([]byte, error) {
	if !libhtnwallet.SupportsMLDSA44Multisig(len(s.keysFile.ExtendedPublicKeys), s.keysFile.MinimumSignatures) {
		return nil, errors.Errorf("ML-DSA-44 multisig supports up to %d-of-%d, and this wallet is %d-of-%d",
			txscript.MaxMLDSA44MultiSigSignatures, txscript.MaxMLDSA44MultiSigKeys,
			s.keysFile.MinimumSignatures, len(s.keysFile.ExtendedPublicKeys))
	}
	cosignerPublicKeyHashes, err := s.keysFile.MLDSA44MultiSigPublicKeyHashes(wAddr.keyChain, wAddr.index)
	if err != nil {
		return nil, errors.Wrapf(err, "stop the daemon, then import the missing keys with `htnwallet %s` "+
			"or extend the pools with `htnwallet %s`", importMLDSA44KeysSubCmdName, generateMLDSA44KeysSubCmdName)
	}
	return libhtnwallet.MLDSA44MultiSigRedeemScript(cosignerPublicKeyHashes, s.keysFile.MinimumSignatures)
}

const importMLDSA44KeysSubCmdName = "import-mldsa44-keys"

// walletAddressCosignerIndex returns the cosigner index of a new wallet address. ML-DSA-44 multisig
// addresses share one address space across cosigners (see mldsa44MultiSigRedeemScript), so they all
// use 0 - which is also what scanning assigns them, and walletAddress values are compared by value.
func (s *server) walletAddressCosignerIndex(mldsa44 bool) uint32 {
	if mldsa44 {
		return 0
	}
	return s.keysFile.CosignerIndex
}

// walletAddressRedeemScript returns the redeem script to carry in an unsigned transaction spending
// wAddr, or nil when the signers can rebuild it themselves - which is every case but ML-DSA-44 P2SH,
// single-sig or multisig.
func (s *server) walletAddressRedeemScript(wAddr *walletAddress) ([]byte, error) {
	if !wAddr.mldsa44 {
		return nil, nil
	}
	if s.isMultisig() {
		return s.mldsa44MultiSigRedeemScript(wAddr)
	}
	if wAddr.mldsa44Form != libhtnwallet.MLDSA44AddressFormP2SH {
		return nil, nil
	}
	publicKeyHash, ok := s.keysFile.MLDSA44.PublicKeyHash(wAddr.keyChain, wAddr.index)
	if !ok {
		return nil, errors.Errorf("the wallet has no ML-DSA-44 key at index %d", wAddr.index)
	}
	return libhtnwallet.MLDSA44SingleSigRedeemScript(publicKeyHash)
}

// ensureMLDSA44Active refuses to hand out ML-DSA-44 addresses before the network accepts ML-DSA-44
// spends: coins sent to one earlier would sit unspendable until the activation block version.
func (s *server) ensureMLDSA44Active() error {
	dagInfo, err := s.getBlockDAGInfo(context.Background())
	if err != nil {
		return err
	}
	return s.checkMLDSA44Activation(dagInfo.VirtualDAAScore)
}

func (s *server) checkMLDSA44Activation(virtualDAAScore uint64) error {
	if !libhtnwallet.MLDSA44Active(s.params, virtualDAAScore) {
		return errors.Errorf("ML-DSA-44 is not active on %s yet: it activates at block version %d, "+
			"and coins sent to an ML-DSA-44 address before then cannot be spent until it does",
			s.params.Name, s.params.HardForkGates.MLDSA44SignaturesBlockVersion)
	}
	return nil
}

func (s *server) ShowAddresses(ctx context.Context, request *pb.ShowAddressesRequest) (*pb.ShowAddressesResponse, error) {
	response, mldsa44, err := s.snapshotShownAddresses(request)
	if err != nil {
		return nil, err
	}
	if mldsa44 && len(response.Address) != 0 {
		ctx, cancel := context.WithTimeout(ctx, s.addressListingTimeout)
		defer cancel()
		dagInfo, err := s.getBlockDAGInfo(ctx)
		if err != nil {
			return nil, err
		}
		if err := s.checkMLDSA44Activation(dagInfo.VirtualDAAScore); err != nil {
			return nil, err
		}
	}
	return response, nil
}

func (s *server) snapshotShownAddresses(request *pb.ShowAddressesRequest) (*pb.ShowAddressesResponse, bool, error) {
	s.lock.RLock()
	defer s.lock.RUnlock()

	if !s.isSynced() {
		return nil, false, errors.Errorf("wallet daemon is not synced yet, %s", s.formatSyncStateReport())
	}

	if s.keysFile.IsImported() {
		response, err := s.showImportedWalletAddresses(request)
		return response, false, err
	}

	// includeAll lists the three secp256k1 forms of each of a single-sig wallet's addresses - P2PK,
	// P2PKH and P2SH, in that order - and overrides the address type. ML-DSA-44 addresses are not
	// among them: a caller that reads the answer three by three would take them for secp256k1
	// addresses of other indexes, and they are listed by asking for their address type.
	includeAll := request.GetIncludeAll() && !s.isMultisig()
	mldsa44Form, mldsa44Requested := mldsa44AddressForm(request.GetAddressType())
	mldsa44Requested = mldsa44Requested && !includeAll
	addresses := make([]string, 0)
	lastUsedExternalIndex := scannableLastUsedIndex(s.keysFile.LastUsedExternalIndex())
	for i := uint32(1); i <= lastUsedExternalIndex; i++ {
		walletAddr := &walletAddress{
			index:         i,
			cosignerIndex: s.keysFile.CosignerIndex,
			keyChain:      libhtnwallet.ExternalKeychain,
		}
		if includeAll {
			addressStrings, err := s.walletAddressStringsForScan(walletAddr)
			if err != nil {
				return nil, false, err
			}
			addresses = append(addresses, addressStrings...)
			continue
		}

		if mldsa44Requested {
			for _, mldsa44Addr := range s.mldsa44WalletAddressesForScan(walletAddr) {
				if mldsa44Addr.mldsa44Form != mldsa44Form && !s.isMultisig() {
					continue
				}
				address, err := s.mldsa44Address(mldsa44Addr)
				if err != nil {
					return nil, false, err
				}
				addresses = append(addresses, address.String())
			}
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
			return nil, false, err
		}
		addresses = append(addresses, address.String())
	}

	return &pb.ShowAddressesResponse{Address: addresses}, mldsa44Requested, nil
}

func (s *server) NewAddress(_ context.Context, request *pb.NewAddressRequest) (*pb.NewAddressResponse, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	if !s.isSynced() {
		return nil, errors.Errorf("wallet daemon is not synced yet, %s", s.formatSyncStateReport())
	}

	if form, ok := mldsa44AddressForm(request.GetAddressType()); ok {
		if s.keysFile.IsImported() {
			return nil, errors.New("an imported wallet holds no ML-DSA-44 keys")
		}
		if err := s.ensureMLDSA44Active(); err != nil {
			return nil, err
		}
		return s.newMLDSA44Address(form)
	}

	if s.keysFile.IsImported() {
		return s.importedWalletNewAddress(request)
	}

	index, err := nextIndex(s.keysFile.LastUsedExternalIndex())
	if err != nil {
		return nil, err
	}

	walletAddr := &walletAddress{
		index:         index,
		cosignerIndex: s.keysFile.CosignerIndex,
		keyChain:      libhtnwallet.ExternalKeychain,
	}
	response, err := s.deriveSecp256k1AddressResponse(s.walletAddressPath(walletAddr), request.GetAddressType())
	if err != nil {
		return nil, err
	}
	if err := s.keysFile.SetLastUsedExternalIndex(index); err != nil {
		return nil, err
	}
	if s.isMultisig() {
		s.trackAddress(response.Address, walletAddr)
	} else {
		s.trackAddress(response.P2PkAddress, walletAddr)
		s.trackAddress(response.P2PkhAddress, walletAddr)
		s.trackAddress(response.P2ShAddress, walletAddr)
	}
	return response, nil
}

func (s *server) deriveSecp256k1AddressResponse(path string, addressType pb.AddressType) (*pb.NewAddressResponse, error) {
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
	switch addressType {
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

// mldsa44AddressForm returns the ML-DSA-44 address form an address type asks for, and false when it
// is not an ML-DSA-44 address type.
func mldsa44AddressForm(addressType pb.AddressType) (libhtnwallet.MLDSA44AddressForm, bool) {
	switch addressType {
	case pb.AddressType_ADDRESS_TYPE_MLDSA44:
		return libhtnwallet.MLDSA44AddressFormP2PKH, true
	case pb.AddressType_ADDRESS_TYPE_MLDSA44_P2SH:
		return libhtnwallet.MLDSA44AddressFormP2SH, true
	default:
		return 0, false
	}
}

// newMLDSA44Address hands out the ML-DSA-44 address of form at the next external index. The caller
// checks network activation first. In a multisig wallet both forms name its multisig P2SH.
func (s *server) newMLDSA44Address(form libhtnwallet.MLDSA44AddressForm) (*pb.NewAddressResponse, error) {
	index, err := nextIndex(s.keysFile.LastUsedExternalIndex())
	if err != nil {
		return nil, err
	}

	walletAddr := &walletAddress{
		index:         index,
		cosignerIndex: s.walletAddressCosignerIndex(true),
		keyChain:      libhtnwallet.ExternalKeychain,
		mldsa44:       true,
	}
	if !s.isMultisig() {
		walletAddr.mldsa44Form = form
	}
	// Resolve the address before consuming the index, so an exhausted key pool leaves the wallet as it was.
	address, err := s.mldsa44Address(walletAddr)
	if err != nil {
		return nil, err
	}

	if err := s.keysFile.SetLastUsedExternalIndex(index); err != nil {
		return nil, err
	}

	s.trackAddress(address.String(), walletAddr)
	return &pb.NewAddressResponse{Address: address.String()}, nil
}

// mldsa44WalletAddressesForScan returns the ML-DSA-44 counterparts of wAddr, or none when there is
// nothing to query: a multisig wallet's one multisig P2SH once every cosigner's keys are imported,
// and a single-sig wallet's P2PKH and P2SH for the indexes its key pool covers.
func (s *server) mldsa44WalletAddressesForScan(wAddr *walletAddress) []*walletAddress {
	mldsa44Addr := *wAddr
	mldsa44Addr.mldsa44 = true
	if s.isMultisig() {
		// One address space for all cosigners; see mldsa44MultiSigRedeemScript.
		mldsa44Addr.cosignerIndex = 0
		if _, err := s.mldsa44MultiSigRedeemScript(&mldsa44Addr); err != nil {
			return nil
		}
		return []*walletAddress{&mldsa44Addr}
	}
	if _, ok := s.keysFile.MLDSA44.PublicKeyHash(wAddr.keyChain, wAddr.index); !ok {
		return nil
	}
	forms := []libhtnwallet.MLDSA44AddressForm{libhtnwallet.MLDSA44AddressFormP2PKH, libhtnwallet.MLDSA44AddressFormP2SH}
	addresses := make([]*walletAddress, len(forms))
	for i, form := range forms {
		formAddr := mldsa44Addr
		formAddr.mldsa44Form = form
		addresses[i] = &formAddr
	}
	return addresses
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
	// ML-DSA-44 multisig addresses share one m/<keychain>/<index> space (see mldsa44MultiSigRedeemScript).
	return libhtnwallet.WalletAddressPath(s.isMultisig() && !wAddr.mldsa44, wAddr.cosignerIndex, wAddr.keyChain, wAddr.index)
}

func (s *server) isMultisig() bool {
	return len(s.keysFile.ExtendedPublicKeys) > 1
}
