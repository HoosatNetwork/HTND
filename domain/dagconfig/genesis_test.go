// Copyright (c) 2014-2016 The btcsuite developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package dagconfig

import (
	"encoding/binary"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus/utils/consensushashing"
)

// TestGenesisBlock tests the genesis block of the main network for validity by
// checking the encoded hash.
func TestGenesisBlock(t *testing.T) {
	// Check hash of the block against expected hash.
	hash := consensushashing.BlockHash(MainnetParams.GenesisBlock)
	if !MainnetParams.GenesisHash.Equal(hash) {
		t.Fatalf("TestGenesisBlock: Genesis block hash does "+
			"not appear valid - got %v, want %v", hash, MainnetParams.GenesisHash)
	}
}

// TestTestnetGenesisBlock tests the genesis block of the test network for
// validity by checking the hash.
func TestTestnetGenesisBlock(t *testing.T) {
	genesisBlock := TestnetParams.GenesisBlock

	calculatedBlockHash := consensushashing.BlockHash(genesisBlock)
	if !TestnetParams.GenesisHash.Equal(calculatedBlockHash) {
		hashBytes := calculatedBlockHash.ByteSlice()
		var formatted []string
		for _, b := range hashBytes {
			formatted = append(formatted, fmt.Sprintf("0x%02x", b))
		}
		formattedStr := strings.Join(formatted, ", ")

		t.Fatalf("TestGenesisBlock: Genesis block hash does not appear valid.\nGot:\n[]byte{%s}\nWant:\n%v",
			formattedStr, MainnetParams.GenesisHash)
	}
}

// TestSimnetGenesisBlock tests the genesis block of the simulation test network
// for validity by checking the hash.
func TestSimnetGenesisBlock(t *testing.T) {
	// Check hash of the block against expected hash.
	hash := consensushashing.BlockHash(SimnetParams.GenesisBlock)
	if !SimnetParams.GenesisHash.Equal(hash) {
		t.Fatalf("TestSimnetGenesisBlock: Genesis block hash does "+
			"not appear valid - got %v, want %v", hash,
			SimnetParams.GenesisHash)
	}
}

// TestDevnetGenesisBlock tests the genesis block of the development network
// for validity by checking the encoded hash.
func TestDevnetGenesisBlock(t *testing.T) {
	// Check hash of the block against expected hash.
	hash := consensushashing.BlockHash(DevnetParams.GenesisBlock)
	if !DevnetParams.GenesisHash.Equal(hash) {
		t.Fatalf("TestDevnetGenesisBlock: Genesis block hash does "+
			"not appear valid - got %v, want %v", hash,
			DevnetParams.GenesisHash)
	}
}

// CompactToBig converts compact bits to target big.Int
func CompactToBig(compact uint32) *big.Int {
	size := compact >> 24
	mant := compact & 0x007fffff
	if compact&0x00800000 != 0 {
		// Negative targets are not allowed; make it positive
		mant &= 0x007fffff
	}
	target := big.NewInt(int64(mant))
	if size <= 3 {
		target.Rsh(target, uint(8*(3-size)))
	} else {
		target.Lsh(target, uint(8*(size-3)))
	}
	return target
}

// BigToCompact converts target big.Int to compact bits format
func BigToCompact(target *big.Int) uint32 {
	bytes := target.Bytes()
	size := len(bytes)

	var mant uint32
	if size <= 3 {
		// Cast to uint32 after shifting the value to prevent overflow
		var mantBytes [8]byte
		binary.BigEndian.PutUint64(mantBytes[:], new(big.Int).SetBytes(bytes).Uint64()>>(8*(3-size)))
		mant = binary.BigEndian.Uint32(mantBytes[4:])
	} else {
		// Only use the first 3 bytes to avoid overflow
		var mantBytes [8]byte
		binary.BigEndian.PutUint64(mantBytes[:], new(big.Int).SetBytes(bytes[:3]).Uint64())
		mant = binary.BigEndian.Uint32(mantBytes[4:])
	}

	if mant&0x00800000 != 0 {
		mant >>= 8
		size++
	}

	sizeUint64, err := strconv.ParseUint(strconv.Itoa(size), 10, 32)
	if err != nil {
		panic(err)
	}
	var compactSizeBytes [8]byte
	binary.BigEndian.PutUint64(compactSizeBytes[:], sizeUint64<<24)
	compactSize := binary.BigEndian.Uint32(compactSizeBytes[4:])
	return compactSize | (mant & 0x007fffff)
}

// DifficultyToBits computes compact bits for desired difficulty
func DifficultyToBits(baseBits uint32, difficulty int64) uint32 {
	baseTarget := CompactToBig(baseBits)
	newTarget := new(big.Int).Div(baseTarget, big.NewInt(difficulty))
	return BigToCompact(newTarget)
}

// Unit test for difficulty 100 from 0x207fffff
func TestDifficultyToBits(t *testing.T) {
	baseBits := uint32(0x207fffff) // Regtest maximum target
	wantBits := uint32(0x1f0346dc) // Expected for difficulty 10000

	gotBits := DifficultyToBits(baseBits, 10000)
	if gotBits != wantBits {
		t.Errorf("DifficultyToBits() = 0x%x, want 0x%x", gotBits, wantBits)
	}
}
