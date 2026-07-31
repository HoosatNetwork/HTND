// Copyright (c) 2017 The btcsuite developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package bech32

import (
	"strings"

	"github.com/pkg/errors"
)

const (
	charset        = "qpzry9x8gf2tvdw0s3jn54khce6mua7l"
	checksumLength = 8
)

// Inverse lookup table for O(1) lookups during decoding.
var charsetInverse [256]int8

func init() {
	for i := range len(charsetInverse) {
		charsetInverse[i] = -1
	}
	for i := range len(charset) {
		charsetInverse[charset[i]] = int8(i)
	}
}

type conversionType struct {
	fromBits uint8
	toBits   uint8
	pad      bool
}

var (
	fiveToEightBits = conversionType{fromBits: 5, toBits: 8, pad: false}
	eightToFiveBits = conversionType{fromBits: 8, toBits: 5, pad: true}
)

var generator = []int{0x98f2bc8e61, 0x79b76d99e2, 0xf33e5fb3c4, 0xae2eabe2a8, 0x1e4f43e470}

// Encode prepends the version byte, converts to uint5, and encodes to Bech32.
func Encode(prefix string, payload []byte, version byte) string {
	data := make([]byte, len(payload)+1)
	data[0] = version
	copy(data[1:], payload)

	converted := convertBits(data, eightToFiveBits)
	return encode(prefix, converted)
}

// Decode decodes a string that was encoded with Encode.
func Decode(encoded string) (string, []byte, byte, error) {
	prefix, decoded, err := decode(encoded)
	if err != nil {
		return "", nil, 0, err
	}

	converted := convertBits(decoded, fiveToEightBits)
	if len(converted) == 0 {
		return "", nil, 0, errors.New("empty payload after bit conversion")
	}
	version := converted[0]
	payload := converted[1:]

	return prefix, payload, version, nil
}

func decode(encoded string) (string, []byte, error) {
	if len(encoded) < checksumLength+2 {
		return "", nil, errors.Errorf("invalid bech32 string length %d", len(encoded))
	}

	// Validate characters and mixed casing in a single zero-allocation pass
	hasLower := false
	hasUpper := false
	for i := 0; i < len(encoded); i++ {
		c := encoded[i]
		if c < 33 || c > 126 {
			return "", nil, errors.Errorf("invalid character in string: '%c'", c)
		}
		if c >= 'a' && c <= 'z' {
			hasLower = true
		}
		if c >= 'A' && c <= 'Z' {
			hasUpper = true
		}
	}
	if hasLower && hasUpper {
		return "", nil, errors.Errorf("string not all lowercase or all uppercase")
	}

	colonIndex := -1
	for i := len(encoded) - 1; i >= 0; i-- {
		if encoded[i] == ':' {
			colonIndex = i
			break
		}
	}
	if colonIndex < 1 || colonIndex+checksumLength+1 > len(encoded) {
		return "", nil, errors.Errorf("invalid index of ':'")
	}

	prefix := encoded[:colonIndex]
	data := encoded[colonIndex+1:]

	decoded, err := decodeFromBase32(data)
	if err != nil {
		return "", nil, errors.Errorf("failed converting data to bytes: %s", err)
	}

	if !verifyChecksum(prefix, decoded) {
		return "", nil, errors.Errorf("checksum failed")
	}

	return prefix, decoded[:len(decoded)-checksumLength], nil
}

func encode(prefix string, data []byte) string {
	var checksum [checksumLength]byte
	calculateChecksumBuf(prefix, data, checksum[:])

	// Preallocate exact capacity to prevent dynamic re-allocations
	combined := make([]byte, len(data)+checksumLength)
	copy(combined, data)
	copy(combined[len(data):], checksum[:])

	base32String := encodeToBase32(combined)

	// Avoid string formatting/concatenation leaks by building the string directly
	var sb strings.Builder
	sb.Grow(len(prefix) + 1 + len(base32String))

	for i := 0; i < len(prefix); i++ {
		c := prefix[i]
		if c >= 'A' && c <= 'Z' {
			c += 32 // Convert uppercase ASCII to lowercase
		}
		sb.WriteByte(c)
	}
	sb.WriteByte(':')
	sb.WriteString(base32String)

	return sb.String()
}

func decodeFromBase32(base32String string) ([]byte, error) {
	decoded := make([]byte, len(base32String))
	for i := 0; i < len(base32String); i++ {
		c := base32String[i]
		// Handle implicit lowercase conversion cleanly on-the-fly
		if c >= 'A' && c <= 'Z' {
			c += 32
		}
		index := charsetInverse[c]
		if index < 0 {
			return nil, errors.Errorf("invalid character not part of charset: %c", base32String[i])
		}
		decoded[i] = byte(index)
	}
	return decoded, nil
}

func encodeToBase32(data []byte) string {
	result := make([]byte, len(data))
	for i, b := range data {
		if int(b) >= len(charset) {
			return ""
		}
		result[i] = charset[b]
	}
	return string(result)
}

func convertBits(data []byte, conversionType conversionType) []byte {
	totalBits := len(data) * int(conversionType.fromBits)
	allocSize := totalBits / int(conversionType.toBits)
	if conversionType.pad && (totalBits%int(conversionType.toBits) != 0) {
		allocSize++
	}

	regrouped := make([]byte, 0, allocSize)
	nextByte := byte(0)
	filledBits := uint8(0)

	for _, b := range data {
		b <<= 8 - conversionType.fromBits
		remainingFromBits := conversionType.fromBits
		for remainingFromBits > 0 {
			remainingToBits := conversionType.toBits - filledBits
			toExtract := min(remainingFromBits, remainingToBits)

			nextByte = (nextByte << toExtract) | (b >> (8 - toExtract))
			b <<= toExtract
			remainingFromBits -= toExtract
			filledBits += toExtract

			if filledBits == conversionType.toBits {
				regrouped = append(regrouped, nextByte)
				filledBits = 0
				nextByte = 0
			}
		}
	}

	if conversionType.pad && filledBits > 0 {
		nextByte <<= (conversionType.toBits - filledBits)
		regrouped = append(regrouped, nextByte)
	}

	return regrouped
}

// Fixed-size stack boundary implementation to fully prevent heap allocations
func calculateChecksumBuf(prefix string, payload []byte, out []byte) {
	checksum := 1

	for i := 0; i < len(prefix); i++ {
		checksum = polyModStep(checksum, int(prefix[i]&31))
	}
	checksum = polyModStep(checksum, 0)
	for _, b := range payload {
		checksum = polyModStep(checksum, int(b))
	}
	for range 8 {
		checksum = polyModStep(checksum, 0)
	}

	checksum ^= 1
	for i := range checksumLength {
		shift := 5 * (checksumLength - 1 - i)
		out[i] = byte((checksum >> uint(shift)) & 31)
	}
}

func verifyChecksum(prefix string, payload []byte) bool {
	checksum := 1
	for i := 0; i < len(prefix); i++ {
		checksum = polyModStep(checksum, int(prefix[i]&31))
	}
	checksum = polyModStep(checksum, 0)
	for _, b := range payload {
		checksum = polyModStep(checksum, int(b))
	}
	return checksum == 1
}

func polyModStep(checksum int, value int) int {
	topBits := checksum >> 35
	checksum = ((checksum & 0x07ffffffff) << 5) ^ value
	for i := range 5 {
		if ((topBits >> uint(i)) & 1) == 1 {
			checksum ^= generator[i]
		}
	}
	return checksum
}
