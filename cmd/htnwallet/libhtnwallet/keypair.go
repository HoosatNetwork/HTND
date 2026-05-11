package libhtnwallet

import (
	"encoding/binary"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/Hoosat-Oy/HTND/cmd/htnwallet/libhtnwallet/bip32"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/txscript"
	"github.com/Hoosat-Oy/HTND/domain/dagconfig"
	"github.com/Hoosat-Oy/HTND/util"
	"github.com/kaspanet/go-secp256k1"
	"github.com/pkg/errors"
)

func checkedUint32ToInt(value uint32) (int, error) {
	if strconv.IntSize == 32 && value > math.MaxInt32 {
		return 0, errors.Errorf("value %d exceeds int", value)
	}
	return int(value), nil
}

func checkedIntToUint32(value int) (uint32, error) {
	if value < 0 {
		return 0, errors.Errorf("value %d cannot be negative", value)
	}
	valueUint64, err := strconv.ParseUint(strconv.Itoa(value), 10, 32)
	if err != nil {
		return 0, err
	}
	var valueBytes [8]byte
	binary.BigEndian.PutUint64(valueBytes[:], valueUint64)
	return binary.BigEndian.Uint32(valueBytes[4:]), nil
}

// CreateKeyPair generates a private-public key pair
func CreateKeyPair(ecdsa bool) ([]byte, []byte, error) {
	if ecdsa {
		return createKeyPairECDSA()
	}

	return createKeyPair()
}

func createKeyPair() ([]byte, []byte, error) {
	keyPair, err := secp256k1.GenerateSchnorrKeyPair()
	if err != nil {
		return nil, nil, errors.Wrap(err, "Failed to generate private key")
	}
	publicKey, err := keyPair.SchnorrPublicKey()
	if err != nil {
		return nil, nil, errors.Wrap(err, "Failed to generate public key")
	}
	publicKeySerialized, err := publicKey.Serialize()
	if err != nil {
		return nil, nil, errors.Wrap(err, "Failed to serialize public key")
	}

	return keyPair.SerializePrivateKey()[:], publicKeySerialized[:], nil
}

func createKeyPairECDSA() ([]byte, []byte, error) {
	keyPair, err := secp256k1.GenerateECDSAPrivateKey()
	if err != nil {
		return nil, nil, errors.Wrap(err, "Failed to generate private key")
	}
	publicKey, err := keyPair.ECDSAPublicKey()
	if err != nil {
		return nil, nil, errors.Wrap(err, "Failed to generate public key")
	}
	publicKeySerialized, err := publicKey.Serialize()
	if err != nil {
		return nil, nil, errors.Wrap(err, "Failed to serialize public key")
	}

	return keyPair.Serialize()[:], publicKeySerialized[:], nil
}

// PublicKeyFromPrivateKey returns the public key associated with a private key
func PublicKeyFromPrivateKey(privateKeyBytes []byte) ([]byte, error) {
	keyPair, err := secp256k1.DeserializeSchnorrPrivateKeyFromSlice(privateKeyBytes)
	if err != nil {
		return nil, errors.Wrap(err, "Failed to deserialize private key")
	}

	publicKey, err := keyPair.SchnorrPublicKey()
	if err != nil {
		return nil, errors.Wrap(err, "Failed to generate public key")
	}

	publicKeySerialized, err := publicKey.Serialize()
	if err != nil {
		return nil, errors.Wrap(err, "Failed to serialize public key")
	}

	return publicKeySerialized[:], nil
}

// Address returns the address associated with the given public keys and minimum signatures parameters.
func Address(params *dagconfig.Params, extendedPublicKeys []string, minimumSignatures uint32, path string, ecdsa bool) (util.Address, error) {
	sortPublicKeys(extendedPublicKeys)
	minimumSignaturesInt, err := checkedUint32ToInt(minimumSignatures)
	if err != nil {
		return nil, err
	}
	if len(extendedPublicKeys) < minimumSignaturesInt {
		return nil, errors.Errorf("The minimum amount of signatures (%d) is greater than the amount of "+
			"provided public keys (%d)", minimumSignatures, len(extendedPublicKeys))
	}

	if len(extendedPublicKeys) == 1 {
		return p2pkAddress(params, extendedPublicKeys[0], path, ecdsa)
	}

	redeemScript, err := multiSigRedeemScript(extendedPublicKeys, minimumSignatures, path, ecdsa)
	if err != nil {
		return nil, err
	}

	return util.NewAddressScriptHash(redeemScript, params.Prefix)
}

// SingleSigAddressType controls which address template is used for a single-sig wallet.
//
// Note: Multisig wallets always return P2SH addresses, regardless of this value.
type SingleSigAddressType uint8

const (
	SingleSigAddressTypeP2PK SingleSigAddressType = iota
	SingleSigAddressTypeP2PKH
	SingleSigAddressTypeP2SH
)

// AddressWithSingleSigAddressType is like Address, but allows choosing between P2PK and P2PKH
// for single-sig wallets.
func AddressWithSingleSigAddressType(
	params *dagconfig.Params,
	extendedPublicKeys []string,
	minimumSignatures uint32,
	path string,
	ecdsa bool,
	singleSigType SingleSigAddressType,
) (util.Address, error) {
	sortPublicKeys(extendedPublicKeys)
	minimumSignaturesInt, err := checkedUint32ToInt(minimumSignatures)
	if err != nil {
		return nil, err
	}
	if len(extendedPublicKeys) < minimumSignaturesInt {
		return nil, errors.Errorf("The minimum amount of signatures (%d) is greater than the amount of "+
			"provided public keys (%d)", minimumSignatures, len(extendedPublicKeys))
	}

	if len(extendedPublicKeys) == 1 {
		switch singleSigType {
		case SingleSigAddressTypeP2PK:
			return p2pkAddress(params, extendedPublicKeys[0], path, ecdsa)
		case SingleSigAddressTypeP2PKH:
			return p2pkhAddress(params, extendedPublicKeys[0], path, ecdsa)
		case SingleSigAddressTypeP2SH:
			return p2shP2PKHAddress(params, extendedPublicKeys[0], path, ecdsa)
		default:
			return nil, errors.Errorf("unknown singleSigType %d", singleSigType)
		}
	}

	redeemScript, err := multiSigRedeemScript(extendedPublicKeys, minimumSignatures, path, ecdsa)
	if err != nil {
		return nil, err
	}

	return util.NewAddressScriptHash(redeemScript, params.Prefix)
}

func p2pkAddress(params *dagconfig.Params, extendedPublicKey string, path string, ecdsa bool) (util.Address, error) {
	extendedKey, err := bip32.DeserializeExtendedKey(extendedPublicKey)
	if err != nil {
		return nil, err
	}

	derivedKey, err := extendedKey.DeriveFromPath(path)
	if err != nil {
		return nil, err
	}

	publicKey, err := derivedKey.PublicKey()
	if err != nil {
		return nil, err
	}

	if ecdsa {
		serializedECDSAPublicKey, err := publicKey.Serialize()
		if err != nil {
			return nil, err
		}
		return util.NewAddressPublicKeyECDSA(serializedECDSAPublicKey[:], params.Prefix)
	}

	schnorrPublicKey, err := publicKey.ToSchnorr()
	if err != nil {
		return nil, err
	}

	serializedSchnorrPublicKey, err := schnorrPublicKey.Serialize()
	if err != nil {
		return nil, err
	}

	return util.NewAddressPublicKey(serializedSchnorrPublicKey[:], params.Prefix)
}

func p2pkhAddress(params *dagconfig.Params, extendedPublicKey string, path string, ecdsa bool) (util.Address, error) {
	extendedKey, err := bip32.DeserializeExtendedKey(extendedPublicKey)
	if err != nil {
		return nil, err
	}

	derivedKey, err := extendedKey.DeriveFromPath(path)
	if err != nil {
		return nil, err
	}

	publicKey, err := derivedKey.PublicKey()
	if err != nil {
		return nil, err
	}

	if ecdsa {
		serializedECDSAPublicKey, err := publicKey.Serialize()
		if err != nil {
			return nil, err
		}
		return util.NewAddressPublicKeyHashECDSA(serializedECDSAPublicKey[:], params.Prefix)
	}

	schnorrPublicKey, err := publicKey.ToSchnorr()
	if err != nil {
		return nil, err
	}

	serializedSchnorrPublicKey, err := schnorrPublicKey.Serialize()
	if err != nil {
		return nil, err
	}

	return util.NewAddressPublicKeyHash(serializedSchnorrPublicKey[:], params.Prefix)
}

// p2shP2PKHAddress returns a P2SH address whose redeem script is a standard
// single-sig P2PKH script for the derived key.
func p2shP2PKHAddress(params *dagconfig.Params, extendedPublicKey string, path string, ecdsa bool) (util.Address, error) {
	addrP2PKH, err := p2pkhAddress(params, extendedPublicKey, path, ecdsa)
	if err != nil {
		return nil, err
	}

	redeemScriptPublicKey, err := txscript.PayToAddrScript(addrP2PKH)
	if err != nil {
		return nil, err
	}

	return util.NewAddressScriptHash(redeemScriptPublicKey.Script, params.Prefix)
}

func sortPublicKeys(extendedPublicKeys []string) {
	sort.Slice(extendedPublicKeys, func(i, j int) bool {
		return strings.Compare(extendedPublicKeys[i], extendedPublicKeys[j]) < 0
	})
}

func cosignerIndex(extendedPublicKey string, sortedExtendedPublicKeys []string) (uint32, error) {
	cosignerIndex := sort.SearchStrings(sortedExtendedPublicKeys, extendedPublicKey)
	if cosignerIndex == len(sortedExtendedPublicKeys) {
		return 0, errors.Errorf("couldn't find extended public key %s", extendedPublicKey)
	}
	if cosignerIndex < 0 {
		return 0, errors.Errorf("cosigner index %d cannot be negative", cosignerIndex)
	}
	cosignerIndexUint32, err := checkedIntToUint32(cosignerIndex)
	if err != nil {
		return 0, err
	}

	return cosignerIndexUint32, nil
}

// MinimumCosignerIndex returns the minimum index for the cosigner from the set of all extended public keys.
func MinimumCosignerIndex(cosignerExtendedPublicKeys, allExtendedPublicKeys []string) (uint32, error) {
	allExtendedPublicKeysCopy := make([]string, len(allExtendedPublicKeys))
	copy(allExtendedPublicKeysCopy, allExtendedPublicKeys)
	sortPublicKeys(allExtendedPublicKeysCopy)

	minIndex := uint32(math.MaxUint32)
	for _, extendedPublicKey := range cosignerExtendedPublicKeys {
		cosignerIndex, err := cosignerIndex(extendedPublicKey, allExtendedPublicKeysCopy)
		if err != nil {
			return 0, err
		}

		if cosignerIndex < minIndex {
			minIndex = cosignerIndex
		}
	}

	return minIndex, nil
}
