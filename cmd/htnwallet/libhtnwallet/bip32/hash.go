//lint:file-ignore SA1019 RIPEMD-160 is required here for legacy BIP32-compatible hash160 behavior.
//nolint:staticcheck // RIPEMD-160 is required here for legacy BIP32-compatible hash160 behavior.
package bip32

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/sha512"
	"hash"

	// WARNING: RIPEMD-160 is deprecated and considered weak. It is used here for legacy compatibility (e.g., Bitcoin address generation).
	// For new applications, prefer SHA-256 or SHA-3. See: https://github.com/golang/go/issues/44205
	"github.com/pkg/errors"
	//lint:ignore SA1019 RIPEMD-160 is required here for legacy BIP32-compatible hash160 behavior.
	// #nosec G507 -- RIPEMD-160 is required here for legacy BIP32/BTC-compatible hash160 behavior.
	"golang.org/x/crypto/ripemd160"
)

func newHMACWriter(key []byte) hmacWriter {
	return hmacWriter{
		Hash: hmac.New(sha512.New, key),
	}
}

type hmacWriter struct {
	hash.Hash
}

func (hw hmacWriter) InfallibleWrite(p []byte) {
	_, err := hw.Write(p)
	if err != nil {
		panic(errors.Wrap(err, "writing to hmac should never fail"))
	}
}

func calcChecksum(data []byte) []byte {
	return doubleSha256(data)[:checkSumLen]
}

func doubleSha256(data []byte) []byte {
	inner := sha256.Sum256(data)
	outer := sha256.Sum256(inner[:])
	return outer[:]
}

// validateChecksum validates that the last checkSumLen bytes of the
// given data are its valid checksum.
func validateChecksum(data []byte) error {
	checksum := data[len(data)-checkSumLen:]
	expectedChecksum := calcChecksum(data[:len(data)-checkSumLen])
	if !bytes.Equal(expectedChecksum, checksum) {
		return errors.Errorf("expected checksum %x but got %x", expectedChecksum, checksum)
	}

	return nil
}

// hash160 returns RIPEMD-160(SHA-256(data)).
// WARNING: RIPEMD-160 is deprecated and considered weak. Used only for legacy compatibility (e.g., Bitcoin address generation).
func hash160(data []byte) []byte {
	sha := sha256.Sum256(data)
	// #nosec G406 -- RIPEMD-160 is intentionally retained for legacy-compatible hash160 derivation.
	ripe := ripemd160.New()
	ripe.Write(sha[:])
	return ripe.Sum(nil)
}
