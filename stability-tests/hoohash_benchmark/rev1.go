package main

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math"
	"math/big"
	"math/bits"
	"time"

	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/hashes"
	"golang.org/x/crypto/blake2b"
	// Import other necessary packages
)

type xoShiRo256PlusPlus struct {
	s0 uint64
	s1 uint64
	s2 uint64
	s3 uint64
}

func newxoShiRo256PlusPlus(hash *externalapi.DomainHash) *xoShiRo256PlusPlus {
	hashArray := hash.ByteArray()
	return &xoShiRo256PlusPlus{
		s0: binary.LittleEndian.Uint64(hashArray[:8]),
		s1: binary.LittleEndian.Uint64(hashArray[8:16]),
		s2: binary.LittleEndian.Uint64(hashArray[16:24]),
		s3: binary.LittleEndian.Uint64(hashArray[24:32]),
	}
}

func (x *xoShiRo256PlusPlus) Uint64() uint64 {
	res := bits.RotateLeft64(x.s0+x.s3, 23) + x.s0
	t := x.s1 << 17
	x.s2 ^= x.s0
	x.s3 ^= x.s1
	x.s1 ^= x.s2
	x.s0 ^= x.s3

	x.s2 ^= t
	x.s3 = bits.RotateLeft64(x.s3, 45)
	return res
}

const eps float64 = 1e-9

// type matrix [64][64]uint16
type matrix [128][128]uint16

func MediumComplexNonLinear(x float64) float64 {
	return math.Exp(math.Sin(x) + math.Cos(x))
}

func IntermediateComplexNonLinear(x float64) float64 {
	if x == math.Pi/2 || x == 3*math.Pi/2 {
		return 0 // Avoid singularity
	}
	return math.Sin(x) * math.Cos(x) * math.Tan(x)
}

func HighComplexNonLinear(x float64) float64 {
	return math.Exp(x) * math.Log(x+1)
}

func ComplexNonLinear(x float64) float64 {
	transformFactor := math.Mod(x, 1.0)
	switch {
	case x < 1:
		switch {
		case transformFactor < 0.25:
			return MediumComplexNonLinear(x + (1 + transformFactor))
		case transformFactor < 0.5:
			return MediumComplexNonLinear(x - (1 + transformFactor))
		case transformFactor < 0.75:
			return MediumComplexNonLinear(x * (1 + transformFactor))
		default:
			return MediumComplexNonLinear(x / (1 + transformFactor))
		}
	case x < 10:
		switch {
		case transformFactor < 0.25:
			return IntermediateComplexNonLinear(x + (1 + transformFactor))
		case transformFactor < 0.5:
			return IntermediateComplexNonLinear(x - (1 + transformFactor))
		case transformFactor < 0.75:
			return IntermediateComplexNonLinear(x * (1 + transformFactor))
		default:
			return IntermediateComplexNonLinear(x / (1 + transformFactor))
		}
	default:
		switch {
		case transformFactor < 0.25:
			return HighComplexNonLinear(x + (1 + transformFactor))
		case transformFactor < 0.5:
			return HighComplexNonLinear(x - (1 + transformFactor))
		case transformFactor < 0.75:
			return HighComplexNonLinear(x * (1 + transformFactor))
		default:
			return HighComplexNonLinear(x / (1 + transformFactor))
		}
	}
}

func (mat *matrix) computeHoohashRank() int {
	var B [64][64]float64
	for i := range B {
		for j := range B[0] {
			// fmt.Printf("%v\n", mat[i][j])
			B[i][j] = float64(mat[i][j]) + ComplexNonLinear(float64(mat[i][j]))
		}
	}
	var rank int
	var rowSelected [64]bool
	for i := range 64 {
		var j int
		for j = 0; j < 64; j++ {
			if !rowSelected[j] && math.Abs(B[j][i]) > eps {
				break
			}
		}
		if j != 64 {
			rank++
			rowSelected[j] = true
			for p := i + 1; p < 64; p++ {
				B[j][p] /= B[j][i]
			}
			for k := range 64 {
				if k != j && math.Abs(B[k][i]) > eps {
					for p := i + 1; p < 64; p++ {
						B[k][p] -= B[j][p] * B[k][i]
					}
				}
			}
		}
	}
	return rank
}

func generateHoohashMatrix(hash *externalapi.DomainHash) *matrix {
	var mat matrix
	generator := newxoShiRo256PlusPlus(hash)

	for {
		for i := range mat {
			for j := 0; j < 64; j += 16 {
				val := generator.Uint64()
				v0 := val & 0x0F
				v1 := (val >> 4) & 0x0F
				v2 := (val >> 8) & 0x0F
				if v0 > uint64(^uint16(0)) || v1 > uint64(^uint16(0)) || v2 > uint64(^uint16(0)) {
					panic("value overflows uint16")
				}
				mat[i][j] = uint16(v0)
				mat[i][j+1] = uint16(v1)
				mat[i][j+2] = uint16(v2)
				v3 := (val >> 12) & 0x0F
				v4 := (val >> 16) & 0x0F
				v5 := (val >> 20) & 0x0F
				if v3 > 0xFFFF || v4 > 0xFFFF || v5 > 0xFFFF {
					panic("value overflows uint16")
				}
				mat[i][j+3] = uint16(v3)
				mat[i][j+4] = uint16(v4)
				mat[i][j+5] = uint16(v5)
				v6 := (val >> 24) & 0x0F
				v7 := (val >> 28) & 0x0F
				v8 := (val >> 32) & 0x0F
				if v6 > 0xFFFF || v7 > 0xFFFF || v8 > 0xFFFF {
					panic("value overflows uint16")
				}
				mat[i][j+6] = uint16(v6)
				mat[i][j+7] = uint16(v7)
				mat[i][j+8] = uint16(v8)
				v9 := (val >> 36) & 0x0F
				v10 := (val >> 40) & 0x0F
				v11 := (val >> 44) & 0x0F
				if v9 > 0xFFFF || v10 > 0xFFFF || v11 > 0xFFFF {
					panic("value overflows uint16")
				}
				mat[i][j+9] = uint16(v9)
				mat[i][j+10] = uint16(v10)
				mat[i][j+11] = uint16(v11)
				v12 := (val >> 48) & 0x0F
				v13 := (val >> 52) & 0x0F
				v14 := (val >> 56) & 0x0F
				if v12 > 0xFFFF || v13 > 0xFFFF || v14 > 0xFFFF {
					panic("value overflows uint16")
				}
				mat[i][j+12] = uint16(v12)
				mat[i][j+13] = uint16(v13)
				mat[i][j+14] = uint16(v14)
				v15 := (val >> 60) & 0x0F
				if v15 > 0xFFFF {
					panic("value overflows uint16")
				}
				mat[i][j+15] = uint16(v15)
			}
		}
		rank := mat.computeHoohashRank()
		if rank == 64 {
			return &mat
		}
	}
}

func (mat *matrix) HoohashMatrixMultiplication(hash *externalapi.DomainHash) []byte {
	hashBytes := hash.ByteArray()
	var vector [64]float64
	var product [64]float64
	for i := range 32 {
		vector[2*i] = float64(hashBytes[i] >> 4)
		vector[2*i+1] = float64(hashBytes[i] & 0x0F)
	}
	// Matrix-vector multiplication with floating point operations
	for i := range 64 {
		var sum float64
		for j := range 64 {
			sum += float64(mat[i][j]) * ComplexNonLinear(vector[j]) // Introduce non-linear operations
		}
		product[i] = sum
	}

	// Convert product back to uint16 and then to byte array
	var res [32]byte
	for i := range res {
		high := uint16(math.Mod(product[2*i], 16))
		low := uint16(math.Mod(product[2*i+1], 16))
		res[i] = hashBytes[i] ^ (byte(high<<4) | byte(low))
	}
	// Hash again
	writer := hashes.BlakeHeavyHashWriter()
	writer.InfallibleWrite(res[:])
	return res[:]
}

const tableSize = 1 << 20 // 64 KB table (reduced from 16 MB)
var lookupTable [tableSize]uint64

func generateHoohashLookupTable() {
	// Initialize lookup table deterministically
	var seed [32]byte
	for i := range lookupTable {
		// Use SHA-256 to generate deterministic values
		if i < 0 {
			continue
		}
		if i > int(^uint32(0)) {
			panic("i overflows uint32")
		}
		binary.BigEndian.PutUint32(seed[:], uint32(i))
		hash := sha256.Sum256(seed[:])
		lookupTable[i] = binary.BigEndian.Uint64(hash[:8])
	}
}

func timeMemoryTradeoff(input uint64) uint64 {
	result := input
	for range 1000 { // Number of lookups
		index := result % tableSize
		result ^= lookupTable[index]
		result = (result << 1) | (result >> 63) // Rotate left by 1
	}
	return result
}

func memoryHardFunction(input []byte) []byte {
	const memorySize = 1 << 10 // 2^16 = 65536
	const iterations = 2

	memory := make([]uint64, memorySize)

	// Initialize memory
	for i := range memory {
		memory[i] = binary.LittleEndian.Uint64(input)
	}

	// Perform memory-hard computations
	for range iterations {
		for j := range memorySize {
			index1 := memory[j] % uint64(memorySize)
			index2 := (memory[j] >> 32) % uint64(memorySize)

			hash, _ := blake2b.New512(nil)
			_ = binary.Write(hash, binary.LittleEndian, memory[index1])
			_ = binary.Write(hash, binary.LittleEndian, memory[index2])

			memory[j] = binary.LittleEndian.Uint64(hash.Sum(nil))
		}
	}

	// Combine results
	result := make([]byte, 64)
	for i := range 8 {
		binary.LittleEndian.PutUint64(result[i*8:], memory[i])
	}
	return result
}

func verifiableDelayFunction(input []byte) []byte {
	const iterations = 1000 // Adjust based on desired delay

	// Create a prime field
	p, _ := new(big.Int).SetString("FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFEFFFFFC2F", 16)

	// Convert input to big.Int
	x := new(big.Int).SetBytes(input)

	// Perform repeated squaring
	for range iterations {
		x.Mul(x, x)
		x.Mod(x, p)
	}

	// Hash the result to get final output
	hash := sha256.Sum256(x.Bytes())
	return hash[:]
}

func BenchmarkHoohashRev1() *externalapi.DomainHash {
	input := []byte("BenchmarkMatrix_HeavyHash")
	firstPass := hashes.Blake3HashWriter()
	firstPass.InfallibleWrite(input)
	hash := firstPass.Finalize()
	matrix := generateHoohashMatrix(hash)
	multiplied := matrix.HoohashMatrixMultiplication(hash)
	secondPass := hashes.Blake3HashWriter()
	secondPass.InfallibleWrite(multiplied)
	hash = secondPass.Finalize()
	return hash
}

func BenchmarkHoohashRev2() *externalapi.DomainHash {
	input := []byte("BenchmarkMatrix_HeavyHash")
	firstPass := hashes.Blake3HashWriter()
	firstPass.InfallibleWrite(input)
	hash := firstPass.Finalize()
	memoryHardResult := memoryHardFunction(hash.ByteSlice())
	tradeoffResult := timeMemoryTradeoff(binary.BigEndian.Uint64(memoryHardResult))
	vdfResult := verifiableDelayFunction(memoryHardResult)
	combined := append([]byte{}, memoryHardResult...)
	combined = append(combined, vdfResult...)
	combined = append(combined, byte(tradeoffResult))
	matrix := generateHoohashMatrix(hash)
	multiplied := matrix.HoohashMatrixMultiplication(externalapi.NewDomainHashFromByteArray((*[32]byte)(combined)))
	secondPass := hashes.Blake3HashWriter()
	secondPass.InfallibleWrite(multiplied)
	hash = secondPass.Finalize()
	return hash
}

func main() {
	iterations := 0
	startTime := time.Now()
	generateHoohashLookupTable()
	for {
		// Here you can switch which algorithm to benchmark
		BenchmarkHoohashRev1()
		// BenchmarkHoohashRev2()
		iterations++

		if iterations%1000 == 0 {
			elapsed := time.Since(startTime)
			opsPerSecond := float64(iterations) / elapsed.Seconds()
			fmt.Printf("Iterations: %d, Time: %v, Ops/sec: %.2f\n", iterations, elapsed, opsPerSecond)
		}
	}
}
