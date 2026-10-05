package util

import (
	"bytes"
	"testing"

	"golang.org/x/crypto/blake2b"
)

// TestDecodeAddressMultiSigPKHPayloadLength pins that a multisig-PKH address must carry exactly a Blake2b-256 hash.
// The payload used to be copied into the fixed-size hash without a length check, so a short payload was zero-padded
// and a long one truncated: the address decoded successfully to a script hash that differs from the one it encodes,
// and anything paid to it was unspendable. Every other address type already rejects a wrong payload length.
func TestDecodeAddressMultiSigPKHPayloadLength(t *testing.T) {
	for _, length := range []int{0, 1, 20, blake2b.Size256 - 1, blake2b.Size256 + 1, 40} {
		payload := bytes.Repeat([]byte{0xab}, length)
		encoded := encodeAddress(Bech32PrefixHoosat, payload, multiSigPKHAddrID)
		if addr, err := DecodeAddress(encoded, Bech32PrefixHoosat); err == nil {
			t.Fatalf("DecodeAddress accepted a %d-byte multisig-PKH payload as %x", length, addr.ScriptAddress())
		}
	}

	var hash [blake2b.Size256]byte
	copy(hash[:], bytes.Repeat([]byte{0xcd}, blake2b.Size256))
	valid, err := NewAddressMultiSigPKH(&hash, Bech32PrefixHoosat)
	if err != nil {
		t.Fatalf("NewAddressMultiSigPKH: %s", err)
	}
	decoded, err := DecodeAddress(valid.EncodeAddress(), Bech32PrefixHoosat)
	if err != nil {
		t.Fatalf("DecodeAddress rejected a valid multisig-PKH address: %s", err)
	}
	if !bytes.Equal(decoded.ScriptAddress(), hash[:]) {
		t.Fatalf("round trip changed the script hash: got %x, want %x", decoded.ScriptAddress(), hash[:])
	}
}
