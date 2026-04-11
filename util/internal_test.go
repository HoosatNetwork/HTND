// Copyright (c) 2013-2017 The btcsuite developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

/*
This test file is part of the util package rather than than the
util_test package so it can bridge access to the internals to properly test
cases which are either not possible or can't reliably be tested via the public
interface. The functions are only exported while the tests are being run.
*/

package util

import (
	"github.com/Hoosat-Oy/HTND/util/bech32"
	"golang.org/x/crypto/blake2b"
)

// TstAppDataDir makes the internal appDir function available to the test
// package.
func TstAppDataDir(goos, appName string, roaming bool) string {
	return appDir(goos, appName, roaming)
}

func TstAddressPubKey(prefix Bech32Prefix, hash [PublicKeySize]byte) *AddressPublicKey {
	return &AddressPublicKey{
		prefix:    prefix,
		publicKey: hash,
	}
}

func TstAddressPubKeyECDSA(prefix Bech32Prefix, hash [PublicKeySizeECDSA]byte) *AddressPublicKeyECDSA {
	return &AddressPublicKeyECDSA{
		prefix:    prefix,
		publicKey: hash,
	}
}

func TstAddressPubKeyHash(prefix Bech32Prefix, hash [blake2b.Size256]byte) *AddressPublicKeyHash {
	return &AddressPublicKeyHash{
		prefix: prefix,
		hash:   hash,
	}
}

func TstAddressPubKeyHashECDSA(prefix Bech32Prefix, hash [blake2b.Size256]byte) *AddressPublicKeyHashECDSA {
	return &AddressPublicKeyHashECDSA{
		prefix: prefix,
		hash:   hash,
	}
}

// TstAddressScriptHash makes an AddressScriptHash, setting the
// unexported fields with the parameters hash and netID.
func TstAddressScriptHash(prefix Bech32Prefix, hash [blake2b.Size256]byte) *AddressScriptHash {
	return &AddressScriptHash{
		prefix: prefix,
		hash:   hash,
	}
}

// TstAddressSAddr returns the expected script address bytes for
// P2PK hoosat addresses.
func TstAddressSAddrP2PK(addr string) []byte {
	_, decoded, _, _ := bech32.Decode(addr)
	return decoded[:PublicKeySize]
}

// TstAddressSAddr returns the expected script address bytes for
// ECDSA P2PK hoosat addresses.
func TstAddressSAddrP2PKECDSA(addr string) []byte {
	_, decoded, _, _ := bech32.Decode(addr)
	return decoded[:PublicKeySizeECDSA]
}

// TstAddressSAddrP2PKH returns the expected script address bytes for
// P2PKH hoosat addresses.
func TstAddressSAddrP2PKH(addr string) []byte {
	_, decoded, _, _ := bech32.Decode(addr)
	return decoded[:blake2b.Size256]
}

// TstAddressSAddrP2PKHECDSA returns the expected script address bytes for
// P2PKH ECDSA hoosat addresses.
func TstAddressSAddrP2PKHECDSA(addr string) []byte {
	_, decoded, _, _ := bech32.Decode(addr)
	return decoded[:blake2b.Size256]
}

// TstAddressSAddrP2SH returns the expected script address bytes for
// P2SH hoosat addresses.
func TstAddressSAddrP2SH(addr string) []byte {
	_, decoded, _, _ := bech32.Decode(addr)
	return decoded[:blake2b.Size256]
}
