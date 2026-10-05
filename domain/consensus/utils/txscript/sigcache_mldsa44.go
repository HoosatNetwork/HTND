package txscript

import (
	"sync"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/cloudflare/circl/sign/mldsa/mldsa44"
	"golang.org/x/crypto/blake2b"
)

// MLDSA44Cache is SigCache for OP_CHECKSIGMLDSA44, plus a cache of parsed public keys.
//
// A transaction's signatures are verified more than once: at mempool admission, when its block is
// validated against its own past UTXO set, and again when a chain block merges it. SigCache makes
// the repeats free for Schnorr and ECDSA; without this, every repeat of an ML-DSA-44 check paid for
// the whole verification. Parsing a public key is most of that cost - it expands the key's matrix
// from its seed and allocates about 20 KB - so keys are cached too, which makes the first check of
// a signature cheaper whenever its key has been seen before.
//
// Neither cache can change a verdict. A signature entry is keyed by a hash committing to the exact
// signature hash, public key and signature that verified, and only verified triples are recorded,
// so a hit is a fact this node already established. A parsed key is keyed by a hash of its full
// encoding, and is only stored once a signature under it has verified, so keys that never sign
// anything valid cannot push out the ones that do. Eviction is random, as in SigCache.
type MLDSA44Cache struct {
	lock          sync.RWMutex
	validSigs     map[[blake2b.Size256]byte]struct{}
	maxValidSigs  uint
	publicKeys    map[[blake2b.Size256]byte]*mldsa44.PublicKey
	maxPublicKeys uint
}

// NewMLDSA44Cache creates an MLDSA44Cache holding at most maxValidSigs verified signatures and
// maxPublicKeys parsed public keys. A parsed key takes about 20 KB, so maxPublicKeys should stay
// small; a verified signature takes well under 100 bytes.
func NewMLDSA44Cache(maxValidSigs, maxPublicKeys uint) *MLDSA44Cache {
	return &MLDSA44Cache{
		validSigs:     make(map[[blake2b.Size256]byte]struct{}, maxValidSigs),
		maxValidSigs:  maxValidSigs,
		publicKeys:    make(map[[blake2b.Size256]byte]*mldsa44.PublicKey, maxPublicKeys),
		maxPublicKeys: maxPublicKeys,
	}
}

// verify reports whether sigBytes is a valid ML-DSA-44 signature by pkBytes over sigHash, with an
// empty context string. parsed is false when pkBytes is not a valid public key encoding, which the
// opcode treats differently from a signature that does not verify.
//
// The caller has already checked that pkBytes is mldsa44.PublicKeySize long and sigBytes is
// mldsa44.SignatureSize long, so the three fields hashed for the signature key have fixed lengths
// and their concatenation is unambiguous.
//
// It is safe for concurrent use, and a nil cache verifies without caching.
func (c *MLDSA44Cache) verify(sigHash *externalapi.DomainHash, pkBytes, sigBytes []byte) (valid, parsed bool) {
	if c == nil {
		var publicKey mldsa44.PublicKey
		if err := publicKey.UnmarshalBinary(pkBytes); err != nil {
			return false, false
		}
		return mldsa44.Verify(&publicKey, sigHash.ByteSlice(), nil, sigBytes), true
	}

	sigKey := mldsa44SigCacheKey(sigHash, pkBytes, sigBytes)
	publicKeyKey := blake2b.Sum256(pkBytes)

	c.lock.RLock()
	_, isKnownValid := c.validSigs[sigKey]
	publicKey := c.publicKeys[publicKeyKey]
	c.lock.RUnlock()
	if isKnownValid {
		return true, true
	}

	publicKeyIsCached := publicKey != nil
	if !publicKeyIsCached {
		publicKey = new(mldsa44.PublicKey)
		if err := publicKey.UnmarshalBinary(pkBytes); err != nil {
			return false, false
		}
	}

	// Verify only reads the key, so a cached key is shared between concurrent verifications.
	if !mldsa44.Verify(publicKey, sigHash.ByteSlice(), nil, sigBytes) {
		return false, true
	}

	c.lock.Lock()
	defer c.lock.Unlock()
	addWithRandomEviction(c.validSigs, c.maxValidSigs, sigKey, struct{}{})
	if !publicKeyIsCached {
		addWithRandomEviction(c.publicKeys, c.maxPublicKeys, publicKeyKey, publicKey)
	}
	return true, true
}

// mldsa44SigCacheKey commits to everything an ML-DSA-44 verification depends on: the message (the
// signature hash), the public key and the signature. The context string is always empty.
func mldsa44SigCacheKey(sigHash *externalapi.DomainHash, pkBytes, sigBytes []byte) [blake2b.Size256]byte {
	hasher, err := blake2b.New256(nil)
	if err != nil {
		// Only a key longer than 64 bytes fails, and this hasher has no key.
		panic(err)
	}
	hasher.Write(sigHash.ByteSlice())
	hasher.Write(pkBytes)
	hasher.Write(sigBytes)
	var key [blake2b.Size256]byte
	hasher.Sum(key[:0])
	return key
}

// addWithRandomEviction adds key to entries, first evicting an arbitrary entry when entries is
// full - the same policy SigCache uses, relying on Go's randomized map iteration. The caller holds
// the write lock.
func addWithRandomEviction[V any](entries map[[blake2b.Size256]byte]V, maxEntries uint,
	key [blake2b.Size256]byte, value V,
) {
	if maxEntries == 0 {
		return
	}
	if _, exists := entries[key]; !exists && uint(len(entries)) >= maxEntries {
		for evicted := range entries {
			delete(entries, evicted)
			break
		}
	}
	entries[key] = value
}
