package txscript

import (
	"sync"
	"testing"

	"github.com/cloudflare/circl/sign/mldsa/mldsa44"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/consensushashing"
)

// mldsa44CacheTestSignature signs a fixed message with the key derived from seedByte.
func mldsa44CacheTestSignature(t *testing.T, seedByte, messageByte byte) (*externalapi.DomainHash, []byte, []byte) {
	t.Helper()
	publicKey, privateKey := mldsa44TestKey(t, seedByte)
	var messageBytes [externalapi.DomainHashSize]byte
	for i := range messageBytes {
		messageBytes[i] = messageByte
	}
	message := externalapi.NewDomainHashFromByteArray(&messageBytes)
	signature := make([]byte, mldsa44.SignatureSize)
	if err := mldsa44.SignTo(privateKey, message.ByteSlice(), nil, false, signature); err != nil {
		t.Fatalf("SignTo: %v", err)
	}
	return message, publicKey.Bytes(), signature
}

func (c *MLDSA44Cache) sizes() (validSigs, publicKeys int) {
	c.lock.RLock()
	defer c.lock.RUnlock()
	return len(c.validSigs), len(c.publicKeys)
}

// TestMLDSA44CacheVerdictsMatchUncached pins that the cache never changes an answer: every triple
// gets the same verdict with a cache, without one, and again from the cache once it is warm.
func TestMLDSA44CacheVerdictsMatchUncached(t *testing.T) {
	message, publicKey, signature := mldsa44CacheTestSignature(t, 1, 0xaa)
	otherMessage, otherPublicKey, _ := mldsa44CacheTestSignature(t, 2, 0xbb)
	tampered := append([]byte(nil), signature...)
	tampered[10] ^= 0x01

	tests := []struct {
		name      string
		message   *externalapi.DomainHash
		publicKey []byte
		signature []byte
		wantValid bool
	}{
		{"valid", message, publicKey, signature, true},
		{"tampered signature", message, publicKey, tampered, false},
		{"other message", otherMessage, publicKey, signature, false},
		{"other public key", message, otherPublicKey, signature, false},
	}

	cache := NewMLDSA44Cache(100, 10)
	for round := 0; round < 2; round++ {
		for _, test := range tests {
			uncachedValid, uncachedParsed := (*MLDSA44Cache)(nil).verify(test.message, test.publicKey, test.signature)
			cachedValid, cachedParsed := cache.verify(test.message, test.publicKey, test.signature)
			if uncachedValid != test.wantValid || cachedValid != test.wantValid || !uncachedParsed || !cachedParsed {
				t.Fatalf("round %d, %s: uncached (valid %t, parsed %t), cached (valid %t, parsed %t), want valid %t",
					round, test.name, uncachedValid, uncachedParsed, cachedValid, cachedParsed, test.wantValid)
			}
		}
	}
}

// TestMLDSA44CacheRecordsOnlyVerifiedSignatures pins that a failed verification leaves no trace:
// neither the signature nor its public key is stored, so keys that never sign anything valid cannot
// evict the ones that do.
func TestMLDSA44CacheRecordsOnlyVerifiedSignatures(t *testing.T) {
	message, publicKey, signature := mldsa44CacheTestSignature(t, 1, 0xaa)
	tampered := append([]byte(nil), signature...)
	tampered[10] ^= 0x01

	cache := NewMLDSA44Cache(100, 10)
	if valid, _ := cache.verify(message, publicKey, tampered); valid {
		t.Fatalf("a tampered signature verified")
	}
	if validSigs, publicKeys := cache.sizes(); validSigs != 0 || publicKeys != 0 {
		t.Fatalf("after a failed verification the cache holds %d signatures and %d keys, want none",
			validSigs, publicKeys)
	}

	if valid, _ := cache.verify(message, publicKey, signature); !valid {
		t.Fatalf("a valid signature did not verify")
	}
	if validSigs, publicKeys := cache.sizes(); validSigs != 1 || publicKeys != 1 {
		t.Fatalf("after a successful verification the cache holds %d signatures and %d keys, want 1 and 1",
			validSigs, publicKeys)
	}

	// The cached key must still reject a bad signature: a key hit is not a signature hit.
	if valid, _ := cache.verify(message, publicKey, tampered); valid {
		t.Fatalf("a tampered signature verified once its public key was cached")
	}
}

func TestMLDSA44CacheBoundsBothSides(t *testing.T) {
	cache := NewMLDSA44Cache(2, 1)
	for seed := byte(1); seed <= 4; seed++ {
		message, publicKey, signature := mldsa44CacheTestSignature(t, seed, seed)
		if valid, _ := cache.verify(message, publicKey, signature); !valid {
			t.Fatalf("signature %d did not verify", seed)
		}
		if validSigs, publicKeys := cache.sizes(); validSigs > 2 || publicKeys > 1 {
			t.Fatalf("after %d signatures the cache holds %d signatures and %d keys, want at most 2 and 1",
				seed, validSigs, publicKeys)
		}
	}

	disabled := NewMLDSA44Cache(0, 0)
	message, publicKey, signature := mldsa44CacheTestSignature(t, 1, 1)
	if valid, _ := disabled.verify(message, publicKey, signature); !valid {
		t.Fatalf("a zero-sized cache rejected a valid signature")
	}
	if validSigs, publicKeys := disabled.sizes(); validSigs != 0 || publicKeys != 0 {
		t.Fatalf("a zero-sized cache holds %d signatures and %d keys", validSigs, publicKeys)
	}
}

func TestMLDSA44CacheReportsUnparseablePublicKey(t *testing.T) {
	message, publicKey, signature := mldsa44CacheTestSignature(t, 1, 0xaa)
	for _, cache := range []*MLDSA44Cache{nil, NewMLDSA44Cache(10, 10)} {
		if valid, parsed := cache.verify(message, publicKey[:len(publicKey)-1], signature); valid || parsed {
			t.Fatalf("a truncated public key gave valid %t, parsed %t, want neither", valid, parsed)
		}
	}
}

// TestMLDSA44CacheConcurrentUse shares one cache - and so one parsed key - between goroutines
// verifying good and bad signatures at once. Run it with -race.
func TestMLDSA44CacheConcurrentUse(t *testing.T) {
	message, publicKey, signature := mldsa44CacheTestSignature(t, 1, 0xaa)
	tampered := append([]byte(nil), signature...)
	tampered[10] ^= 0x01

	cache := NewMLDSA44Cache(100, 10)
	var wg sync.WaitGroup
	errs := make(chan string, 64)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				if valid, _ := cache.verify(message, publicKey, signature); !valid {
					errs <- "a valid signature did not verify"
				}
				return
			}
			if valid, _ := cache.verify(message, publicKey, tampered); valid {
				errs <- "a tampered signature verified"
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

// TestMLDSA44SpendWithSharedCache runs real spends through the opcode with one cache shared between
// them, the way the transaction validator uses it: a valid spend passes twice, and a spend whose
// signature differs from the cached one still fails as it would without the cache.
func TestMLDSA44SpendWithSharedCache(t *testing.T) {
	publicKey, privateKey := mldsa44TestKey(t, 1)
	tx, scriptPubKey := newMLDSA44SpendTx(t, publicKey)
	sigScript, err := SignatureScriptMLDSA44(tx, 0, consensushashing.SigHashAll, privateKey,
		&consensushashing.SighashReusedValues{})
	if err != nil {
		t.Fatalf("SignatureScriptMLDSA44: %v", err)
	}
	tx.Inputs[0].SignatureScript = sigScript

	cache := NewMLDSA44Cache(100, 10)
	execute := func(tx *externalapi.DomainTransaction) error {
		vm, err := NewEngine(scriptPubKey, tx, 0, ScriptEnableMLDSA44, nil, nil, cache,
			&consensushashing.SighashReusedValues{})
		if err != nil {
			return err
		}
		return vm.Execute()
	}

	for attempt := 1; attempt <= 2; attempt++ {
		if err := execute(tx); err != nil {
			t.Fatalf("attempt %d: a correctly signed spend failed: %v", attempt, err)
		}
	}
	if validSigs, _ := cache.sizes(); validSigs != 1 {
		t.Fatalf("the cache holds %d signatures after two identical spends, want 1", validSigs)
	}

	// Same key, same signature, different transaction: the signature hash differs, so the cached
	// entry must not apply.
	changed := tx.Clone()
	changed.Outputs[0].Value++
	if err := execute(changed); !IsErrorCode(err, ErrNullFail) {
		t.Fatalf("a signature over a different transaction: want ErrNullFail, got %v", err)
	}
}
