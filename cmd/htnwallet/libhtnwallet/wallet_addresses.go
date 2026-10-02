package libhtnwallet

import (
	"fmt"
	"slices"

	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
	"github.com/HoosatNetwork/HTND/v2/util"
)

// WalletAddressPath is the derivation path of a wallet's address: m/<keyChain>/<index> for a single-sig
// wallet, and m/<cosignerIndex>/<keyChain>/<index> for a multisig one.
func WalletAddressPath(isMultisig bool, cosignerIndex uint32, keyChain uint8, index uint32) string {
	if isMultisig {
		return fmt.Sprintf("m/%d/%d/%d", cosignerIndex, keyChain, index)
	}
	return fmt.Sprintf("m/%d/%d", keyChain, index)
}

// WalletAddressesAtPath returns every address a wallet with these keys can receive coins on at path. A
// multisig wallet has one. A single-sig wallet has three - P2PK, P2PKH and P2SH-wrapped P2PKH - because
// the same key can be paid in any of those forms, and a scan that looked at only one would miss coins
// paid to the others.
func WalletAddressesAtPath(params *dagconfig.Params, extendedPublicKeys []string, minimumSignatures uint32,
	path string, ecdsa bool,
) ([]util.Address, error) {
	// The address functions sort the keys in place; the caller's slice is left as it was.
	extendedPublicKeys = slices.Clone(extendedPublicKeys)

	if len(extendedPublicKeys) > 1 {
		address, err := Address(params, extendedPublicKeys, minimumSignatures, path, ecdsa)
		if err != nil {
			return nil, err
		}
		return []util.Address{address}, nil
	}

	singleSigTypes := []SingleSigAddressType{SingleSigAddressTypeP2PK, SingleSigAddressTypeP2PKH, SingleSigAddressTypeP2SH}
	addresses := make([]util.Address, 0, len(singleSigTypes))
	for _, singleSigType := range singleSigTypes {
		address, err := AddressWithSingleSigAddressType(params, extendedPublicKeys, minimumSignatures, path, ecdsa,
			singleSigType)
		if err != nil {
			return nil, err
		}
		addresses = append(addresses, address)
	}
	return addresses, nil
}
