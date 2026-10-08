package libhtnwallet

import (
	"fmt"

	"github.com/cloudflare/circl/sign/mldsa/mldsa44"
	"github.com/pkg/errors"
	"github.com/tyler-smith/go-bip39"
	"golang.org/x/crypto/blake2b"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/txscript"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
	"github.com/HoosatNetwork/HTND/v2/util"
)

// MLDSA44SignatureWithPublicKeySize is the size of the PubKeySignaturePair.Signature this wallet
// stores for an ML-DSA-44 input: the signature with its sighash type byte, followed by the public
// key.
//
// A secp256k1 input's public key is re-derived from the pair's extended public key when the
// transaction is extracted. An ML-DSA-44 key cannot be derived from an extended public key at all,
// so the signer - the only party holding the mnemonic - appends it here instead.
const MLDSA44SignatureWithPublicKeySize = mldsa44.SignatureSize + 1 + mldsa44.PublicKeySize

// DefaultMLDSA44KeyPoolSize is how many ML-DSA-44 public key hashes a new wallet precomputes per key
// chain. See MLDSA44PublicKeyHashes for why they are precomputed at all.
const DefaultMLDSA44KeyPoolSize = 500

// mldsa44SeedDomain separates ML-DSA-44 key seeds from every other use of the BIP39 seed, and
// mldsa44MultiSigSeedDomain separates a mnemonic's multisig cosigner keys from its single-sig keys,
// so the same mnemonic used both ways never puts one ML-DSA-44 key behind two kinds of address.
const (
	mldsa44SeedDomain         = "HoosatMLDSA44KeySeed"
	mldsa44MultiSigSeedDomain = "HoosatMLDSA44MultiSigKeySeed"
)

func mldsa44Domain(multisig bool) string {
	if multisig {
		return mldsa44MultiSigSeedDomain
	}
	return mldsa44SeedDomain
}

// mldsa44KeyFromBIP39Seed derives the ML-DSA-44 key at derivationPath ("m/<keychain>/<index>", the
// same path the wallet uses for that address index) from a BIP39 seed.
//
// The derivation deliberately never passes through secp256k1. Deriving the ML-DSA seed from a BIP32
// private key would be simpler, but a BIP32 private key is recoverable by anyone who can break
// secp256k1 on its public counterpart - and the extended public key, and every P2PK address, publish
// that counterpart. A quantum attacker would then derive the ML-DSA key too, which is the one thing
// this key exists to prevent. The BIP39 seed is a PBKDF2 output of the mnemonic and is not
// recoverable from any public key, so a keyed BLAKE2b over it is hash-only end to end.
func mldsa44KeyFromBIP39Seed(bip39Seed []byte, derivationPath string, multisig bool) (*mldsa44.PublicKey, *mldsa44.PrivateKey, error) {
	hasher, err := blake2b.New256(bip39Seed)
	if err != nil {
		return nil, nil, errors.Wrap(err, "failed to key the ML-DSA-44 seed derivation")
	}
	_, _ = hasher.Write([]byte(mldsa44Domain(multisig)))
	_, _ = hasher.Write([]byte{0})
	_, _ = hasher.Write([]byte(derivationPath))

	var seed [mldsa44.SeedSize]byte
	copy(seed[:], hasher.Sum(nil))
	publicKey, privateKey := mldsa44.NewKeyFromSeed(&seed)
	return publicKey, privateKey, nil
}

func mldsa44BIP39Seed(mnemonic string) []byte {
	return bip39.NewSeed(mnemonic, "")
}

// MLDSA44DerivationPath returns the derivation path of the ML-DSA-44 key at index in keychain. It is
// the same path format the daemon uses for single-sig addresses.
func MLDSA44DerivationPath(keychain uint8, index uint32) string {
	return fmt.Sprintf("m/%d/%d", keychain, index)
}

// MLDSA44KeyFromMnemonic returns the ML-DSA-44 key pair at derivationPath for mnemonic. multisig
// selects the mnemonic's multisig cosigner keys rather than its single-sig keys.
func MLDSA44KeyFromMnemonic(mnemonic string, derivationPath string, multisig bool) (*mldsa44.PublicKey, *mldsa44.PrivateKey, error) {
	return mldsa44KeyFromBIP39Seed(mldsa44BIP39Seed(mnemonic), derivationPath, multisig)
}

// MLDSA44PublicKeyHashes returns the BLAKE2b-256 hashes of the ML-DSA-44 public keys at indexes
// [start, start+count) of keychain - exactly the 32 bytes an ML-DSA-44 P2PKH address encodes.
//
// These exist because the wallet daemon is watch-only: it creates and scans addresses from extended
// public keys, without the password. That works for secp256k1 because BIP32 derives child public
// keys publicly, but there is no public derivation for ML-DSA-44 - its public keys can only be
// computed from the mnemonic. So they are computed once, while the mnemonic is available, and only
// their hashes are stored for the daemon. The hashes are no more sensitive than the addresses.
//
// multisig selects the mnemonic's multisig cosigner keys, which is what a cosigner exports to the
// others (see MLDSA44MultiSigAddress).
func MLDSA44PublicKeyHashes(mnemonic string, keychain uint8, start, count uint32, multisig bool) ([][]byte, error) {
	bip39Seed := mldsa44BIP39Seed(mnemonic)
	hashes := make([][]byte, 0, count)
	for index := start; index < start+count; index++ {
		publicKey, _, err := mldsa44KeyFromBIP39Seed(bip39Seed, MLDSA44DerivationPath(keychain, index), multisig)
		if err != nil {
			return nil, err
		}
		hashes = append(hashes, util.HashBlake2b(publicKey.Bytes()))
	}
	return hashes, nil
}

// MLDSA44AddressForm selects how a single-sig ML-DSA-44 key locks an output. The same key at the same
// derivation path is behind both, so a wallet holding the key can spend either.
//
// There is deliberately no pay-to-pubkey form: see util.AddressPublicKeyHashMLDSA44.
type MLDSA44AddressForm uint8

const (
	// MLDSA44AddressFormP2PKH is OP_DUP OP_BLAKE2B <hash> OP_EQUALVERIFY OP_CHECKSIGMLDSA44, the default.
	MLDSA44AddressFormP2PKH MLDSA44AddressForm = iota
	// MLDSA44AddressFormP2SH is pay-to-script-hash of the P2PKH script, as the wallet's Schnorr and
	// ECDSA single-sig P2SH addresses wrap theirs.
	MLDSA44AddressFormP2SH
)

func (form MLDSA44AddressForm) String() string {
	switch form {
	case MLDSA44AddressFormP2PKH:
		return "P2PKH"
	case MLDSA44AddressFormP2SH:
		return "P2SH"
	default:
		return fmt.Sprintf("MLDSA44AddressForm(%d)", uint8(form))
	}
}

// MLDSA44Address returns the ML-DSA-44 P2PKH address for a public key hash from MLDSA44PublicKeyHashes.
func MLDSA44Address(params *dagconfig.Params, publicKeyHash []byte) (util.Address, error) {
	return util.NewAddressPublicKeyHashMLDSA44FromHash(publicKeyHash, params.Prefix)
}

// MLDSA44SingleSigRedeemScript returns the redeem script of a single-sig ML-DSA-44 P2SH address: the
// P2PKH script of publicKeyHash.
func MLDSA44SingleSigRedeemScript(publicKeyHash []byte) ([]byte, error) {
	address, err := util.NewAddressPublicKeyHashMLDSA44FromHash(publicKeyHash, util.Bech32PrefixHoosat)
	if err != nil {
		return nil, err
	}
	scriptPublicKey, err := txscript.PayToAddrScript(address)
	if err != nil {
		return nil, err
	}
	return scriptPublicKey.Script, nil
}

// MLDSA44ScriptHashAddress returns the single-sig ML-DSA-44 P2SH address for a public key hash from
// MLDSA44PublicKeyHashes.
func MLDSA44ScriptHashAddress(params *dagconfig.Params, publicKeyHash []byte) (util.Address, error) {
	redeemScript, err := MLDSA44SingleSigRedeemScript(publicKeyHash)
	if err != nil {
		return nil, err
	}
	return util.NewAddressScriptHash(redeemScript, params.Prefix)
}

// isMLDSA44SingleSigRedeemScript reports whether redeemScript is the redeem script of a single-sig
// ML-DSA-44 P2SH address (see MLDSA44SingleSigRedeemScript).
func isMLDSA44SingleSigRedeemScript(redeemScript []byte) bool {
	return txscript.GetScriptClass(redeemScript) == txscript.PubKeyHashMLDSA44Ty
}

// MLDSA44MultiSigRedeemScript returns the redeem script of an ML-DSA-44 multisig address.
// cosignerPublicKeyHashes maps each cosigner's master extended public key to its ML-DSA-44 public
// key hash at the address's path; the keys enter the script in extended public key order, the same
// order CreateUnsignedTransaction gives the input's PubKeySignaturePairs, so a signer's pair index is
// its key's position in the script.
func MLDSA44MultiSigRedeemScript(cosignerPublicKeyHashes map[string][]byte, minimumSignatures uint32) ([]byte, error) {
	extendedPublicKeys := make([]string, 0, len(cosignerPublicKeyHashes))
	for extendedPublicKey := range cosignerPublicKeyHashes {
		extendedPublicKeys = append(extendedPublicKeys, extendedPublicKey)
	}
	sortPublicKeys(extendedPublicKeys)

	publicKeyHashes := make([][]byte, len(extendedPublicKeys))
	for i, extendedPublicKey := range extendedPublicKeys {
		publicKeyHashes[i] = cosignerPublicKeyHashes[extendedPublicKey]
	}
	minimumSignaturesInt, err := checkedUint32ToInt(minimumSignatures)
	if err != nil {
		return nil, err
	}
	return txscript.MultiSigMLDSA44RedeemScript(publicKeyHashes, minimumSignaturesInt)
}

// MLDSA44MultiSigAddress returns the pay-to-script-hash address of an ML-DSA-44 multisig redeem script.
func MLDSA44MultiSigAddress(params *dagconfig.Params, redeemScript []byte) (util.Address, error) {
	return util.NewAddressScriptHash(redeemScript, params.Prefix)
}

// MLDSA44Active reports whether ML-DSA-44 spends are consensus-valid at virtualDAAScore on the
// network described by params. Coins sent to an ML-DSA-44 address before then cannot be spent until
// the network reaches params.HardForkGates.MLDSA44SignaturesBlockVersion.
func MLDSA44Active(params *dagconfig.Params, virtualDAAScore uint64) bool {
	return params.MLDSA44SignaturesActive(constants.BlockVersionForDAAScore(params.POWScores, virtualDAAScore))
}
