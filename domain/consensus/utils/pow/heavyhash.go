package pow

import (
	// "github.com/chewxy/math"

	"fmt"
	"math"

	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/hashes"
	"github.com/chewxy/math32"
)

const COMPLEX_OUTPUT_CLAMP = 1000000000
const PRODUCT_VALUE_SCALE_MULTIPLIER = 0.1
const COMPLEX_TRANSFORM_MULTIPLIER = 0.000001
const eps float64 = 1e-9

// type matrix [64][64]uint16
type matrix [64][64]uint16
type floatMatrix [64][64]float64

// func generateMatrix(hash *externalapi.DomainHash) *matrix {
// 	var mat matrix
// 	generator := newxoShiRo256PlusPlus(hash)
// 	for {
// 		for i := range mat {
// 			for j := 0; j < 64; j += 16 {
// 				val := generator.Uint64()
// 				for shift := 0; shift < 16; shift++ {
// 					mat[i][j+shift] = uint16(val >> (4 * shift) & 0x0F)
// 				}
// 			}
// 		}
// 		if mat.computeRank() == 64 {
// 			return &mat
// 		}
// 	}
// }

func GenerateMatrix(hash *externalapi.DomainHash) *matrix {
	var mat matrix
	generator := newxoShiRo256PlusPlus(hash)

	for {
		for i := range mat {
			for j := 0; j < 64; j += 16 {
				val := generator.Uint64()
				mat[i][j] = uint16(val & 0x0F)
				mat[i][j+1] = uint16((val >> 4) & 0x0F)
				mat[i][j+2] = uint16((val >> 8) & 0x0F)
				mat[i][j+3] = uint16((val >> 12) & 0x0F)
				mat[i][j+4] = uint16((val >> 16) & 0x0F)
				mat[i][j+5] = uint16((val >> 20) & 0x0F)
				mat[i][j+6] = uint16((val >> 24) & 0x0F)
				mat[i][j+7] = uint16((val >> 28) & 0x0F)
				mat[i][j+8] = uint16((val >> 32) & 0x0F)
				mat[i][j+9] = uint16((val >> 36) & 0x0F)
				mat[i][j+10] = uint16((val >> 40) & 0x0F)
				mat[i][j+11] = uint16((val >> 44) & 0x0F)
				mat[i][j+12] = uint16((val >> 48) & 0x0F)
				mat[i][j+13] = uint16((val >> 52) & 0x0F)
				mat[i][j+14] = uint16((val >> 56) & 0x0F)
				mat[i][j+15] = uint16((val >> 60) & 0x0F)
			}
		}
		rank := mat.computeRank()
		if rank == 64 {
			return &mat
		}
	}
}

func GenerateHoohashMatrix(hash *externalapi.DomainHash) *matrix {
	var mat matrix
	generator := newxoShiRo256PlusPlus(hash)

	for {
		for i := range mat {
			for j := 0; j < 64; j += 16 {
				val := generator.Uint64()
				mat[i][j] = uint16(val & 0x0F)
				mat[i][j+1] = uint16((val >> 4) & 0x0F)
				mat[i][j+2] = uint16((val >> 8) & 0x0F)
				mat[i][j+3] = uint16((val >> 12) & 0x0F)
				mat[i][j+4] = uint16((val >> 16) & 0x0F)
				mat[i][j+5] = uint16((val >> 20) & 0x0F)
				mat[i][j+6] = uint16((val >> 24) & 0x0F)
				mat[i][j+7] = uint16((val >> 28) & 0x0F)
				mat[i][j+8] = uint16((val >> 32) & 0x0F)
				mat[i][j+9] = uint16((val >> 36) & 0x0F)
				mat[i][j+10] = uint16((val >> 40) & 0x0F)
				mat[i][j+11] = uint16((val >> 44) & 0x0F)
				mat[i][j+12] = uint16((val >> 48) & 0x0F)
				mat[i][j+13] = uint16((val >> 52) & 0x0F)
				mat[i][j+14] = uint16((val >> 56) & 0x0F)
				mat[i][j+15] = uint16((val >> 60) & 0x0F)
			}
		}
		rank := mat.computeHoohashRank()
		if rank == 64 {
			return &mat
		}
	}
}

func GenerateHoohashMatrixV110(hash *externalapi.DomainHash) *floatMatrix {
	var mat floatMatrix
	generator := newxoShiRo256PlusPlus(hash)
	const normalize float64 = 1000000

	for i := range 64 {
		for j := range 64 {
			val := generator.Uint64()
			lower4Bytes := uint32(val & 0xFFFFFFFF)
			mat[i][j] = float64(lower4Bytes) / float64(math.MaxUint32) * normalize
		}
	}
	return &mat
}

// Basic Non-linear Operations are fast but less computationally intensive.
// Intermediate Non-linear Operations increase complexity with additional trigonometric functions.
// Advanced Non-linear Operations involve more complex combinations of trigonometric, exponential, and logarithmic functions.
// Very Complex Non-linear Operations introduce even more layers of computation, involving multiple transcendental functions.
// Extremely Complex Non-linear Operations are the most computationally intensive, combining high-power terms, exponentials, and logarithms of absolute values.

func BasicComplexNonLinear(x float64) float64 {
	return math.Sin(x) + math.Cos(x)
}

func MediumComplexNonLinear(x float64) float64 {
	return math.Exp(math.Sin(x) + math.Cos(x))
}

func MediumComplexNonLinear32(x float32) float32 {
	return float32(math.Exp(float64(math32.Sin(x) + math32.Cos(x))))
}

func IntermediateComplexNonLinear(x float64) float64 {
	if x == math.Pi/2 || x == 3*math.Pi/2 {
		return 0 // Avoid singularity
	}
	return math.Sin(x) * math.Sin(x)
}

func IntermediateComplexNonLinear32(x float32) float32 {
	if x == math.Pi/2 || x == 3*math.Pi/2 {
		return 0 // Avoid singularity
	}
	sin := math.Sin(float64(x))
	cos := math.Cos(float64(x))
	ta := math32.Tan(x)
	fmt.Printf("%f %f %f %f\n", x, sin, cos, ta)
	return math32.Sin(x) * math32.Cos(x) * math32.Tan(x)
}

func HighComplexNonLinear(x float64) float64 {
	return math.Exp(x) * math.Log(x+1)
}

func HighComplexNonLinear110(x float64) float64 {
	return 1.0 / math.Sqrt(math.Abs(x)+1)
}

func ComplexNonLinear(x float64) float64 {
	transformFactor := math.Mod(x, 4.0) / 4.0
	if x < 1 {
		if transformFactor < 0.25 {
			return MediumComplexNonLinear(x + (1 + transformFactor))
		} else if transformFactor < 0.5 {
			return MediumComplexNonLinear(x - (1 + transformFactor))
		} else if transformFactor < 0.75 {
			return MediumComplexNonLinear(x * (1 + transformFactor))
		} else {
			return MediumComplexNonLinear(x / (1 + transformFactor))
		}
	} else if x < 10 {
		if transformFactor < 0.25 {
			return IntermediateComplexNonLinear(x + (1 + transformFactor))
		} else if transformFactor < 0.5 {
			return IntermediateComplexNonLinear(x - (1 + transformFactor))
		} else if transformFactor < 0.75 {
			return IntermediateComplexNonLinear(x * (1 + transformFactor))
		} else {
			return IntermediateComplexNonLinear(x / (1 + transformFactor))
		}
	} else {
		if transformFactor < 0.25 {
			return HighComplexNonLinear(x + (1 + transformFactor))
		} else if transformFactor < 0.5 {
			return HighComplexNonLinear(x - (1 + transformFactor))
		} else if transformFactor < 0.75 {
			return HighComplexNonLinear(x * (1 + transformFactor))
		} else {
			return HighComplexNonLinear(x / (1 + transformFactor))
		}
	}
}

func ComplexNonLinear110(x float64) float64 {
	transformFactorOne := (x*COMPLEX_TRANSFORM_MULTIPLIER)/8.0 - math.Floor((x*COMPLEX_TRANSFORM_MULTIPLIER)/8.0)
	transformFactorTwo := (x*COMPLEX_TRANSFORM_MULTIPLIER)/4.0 - math.Floor((x*COMPLEX_TRANSFORM_MULTIPLIER)/4.0)
	if transformFactorOne < 0.33 {
		if transformFactorTwo < 0.25 {
			return MediumComplexNonLinear(x + (1 + transformFactorTwo))
		} else if transformFactorTwo < 0.5 {
			return MediumComplexNonLinear(x - (1 + transformFactorTwo))
		} else if transformFactorTwo < 0.75 {
			return MediumComplexNonLinear(x * (1 + transformFactorTwo))
		} else {
			return MediumComplexNonLinear(x / (1 + transformFactorTwo))
		}
	} else if transformFactorOne < 0.66 {
		if transformFactorTwo < 0.25 {
			return IntermediateComplexNonLinear(x + (1 + transformFactorTwo))
		} else if transformFactorTwo < 0.5 {
			return IntermediateComplexNonLinear(x - (1 + transformFactorTwo))
		} else if transformFactorTwo < 0.75 {
			return IntermediateComplexNonLinear(x * (1 + transformFactorTwo))
		} else {
			return IntermediateComplexNonLinear(x / (1 + transformFactorTwo))
		}
	} else {
		if transformFactorTwo < 0.25 {
			return HighComplexNonLinear110(x + (1 + transformFactorTwo))
		} else if transformFactorTwo < 0.5 {
			return HighComplexNonLinear110(x - (1 + transformFactorTwo))
		} else if transformFactorTwo < 0.75 {
			return HighComplexNonLinear110(x * (1 + transformFactorTwo))
		} else {
			return HighComplexNonLinear110(x / (1 + transformFactorTwo))
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

func (mat *matrix) computeRank() int {
	var B [64][64]float64
	for i := range B {
		for j := range B[0] {
			B[i][j] = float64(mat[i][j])
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

func (mat *matrix) HoohashMatrixMultiplicationV1(hash *externalapi.DomainHash) *externalapi.DomainHash {
	hashBytes := hash.ByteArray()
	var vector [64]float64
	var product [64]float64

	// Populate the vector with floating-point values
	for i := range 32 {
		vector[2*i] = float64(hashBytes[i] >> 4)
		vector[2*i+1] = float64(hashBytes[i] & 0x0F)
	}

	// Matrix-vector multiplication with floating point operations
	for i := range 64 {
		for j := range 64 {
			// Transform Matrix values with complex non linear equations and sum into product.
			forComplex := float64(mat[i][j]) * vector[j]
			for forComplex > 16 {
				forComplex = forComplex * 0.1
			}
			product[i] += ComplexNonLinear(forComplex)
		}
	}

	// Convert product back to uint16 and then to byte array
	var res [32]byte
	for i := range res {
		high := uint32(product[2*i] * 0.00000001)
		low := uint32(product[2*i+1] * 0.00000001)
		// Combine high and low into a single byte
		combined := (high ^ low) & 0xFF
		res[i] = hashBytes[i] ^ byte(combined)
	}
	// Hash again
	writer := hashes.Blake3HashWriter()
	writer.InfallibleWrite(res[:])
	return writer.Finalize()
}

func (mat *matrix) HoohashMatrixMultiplicationV101(hash *externalapi.DomainHash) *externalapi.DomainHash {
	hashBytes := hash.ByteArray()
	var vector [64]float64
	var product [64]float64

	// Populate the vector with floating-point values
	for i := range 32 {
		vector[2*i] = float64(hashBytes[i] >> 4)
		vector[2*i+1] = float64(hashBytes[i] & 0x0F)
	}

	// Matrix-vector multiplication with floating point operations
	for i := range 64 {
		for j := range 64 {
			// Transform Matrix values with complex non linear equations and sum into product.
			forComplex := float64(mat[i][j]) * vector[j]
			for forComplex > 14 {
				forComplex = forComplex * 0.1
			}
			product[i] += ComplexNonLinear(forComplex)
		}
	}

	// Convert product back to uint16 and then to byte array
	var res [32]byte
	for i := range res {
		high := uint32(product[2*i] * 0.00000001)
		low := uint32(product[2*i+1] * 0.00000001)
		// Combine high and low into a single byte
		combined := (high ^ low) & 0xFF
		res[i] = hashBytes[i] ^ byte(combined)
	}
	// Hash again
	writer := hashes.Blake3HashWriter()
	writer.InfallibleWrite(res[:])
	return writer.Finalize()
}

func ForComplex(forComplex float64) float64 {
	var complex float64
	rounds := 1
	complex = ComplexNonLinear110(forComplex)
	for math.IsNaN(float64(complex)) || math.IsInf(float64(complex), 0) {
		forComplex *= 0.1
		if forComplex <= 0.0000000000001 {
			return 0 * float64(rounds)
		}
		rounds++
		complex = ComplexNonLinear110(forComplex)
	}
	// fmt.Printf("%d\n", rounds)
	return complex * float64(rounds)
}

func TransformFactor(x float64) float64 {
	const granularity = 1024.0 // Increase for finer granularity
	return x/granularity - math.Floor(x/granularity)
}

func (mat *floatMatrix) HoohashMatrixMultiplicationV110(hash *externalapi.DomainHash, Nonce uint64) *externalapi.DomainHash {
	hashBytes := hash.ByteArray()
	H := hash.Uint32Array()
	hashMod := float64(H[0] ^ H[1] ^ H[2] ^ H[3] ^ H[4] ^ H[5] ^ H[6] ^ H[7])
	nonceMod := float64(Nonce & 0xFF)
	divider := 0.0001
	multiplier := float64(1234)
	var vector [64]byte
	var product [64]float64
	sw := float64(0.0)

	// Populate the vector with floating-point values from the hash bytes
	for i := range 32 {
		vector[2*i] = hashBytes[i] >> 4     // Upper 4 bits
		vector[2*i+1] = hashBytes[i] & 0x0F // Lower 4 bits
	}

	// Perform the matrix-vector multiplication with nonlinear adjustments
	for i := range 64 {
		for j := range 64 {
			if sw <= 0.02 {
				input := (mat[i][j]*hashMod*float64(vector[j]) + nonceMod)
				output := ForComplex(input) * float64(vector[j]) * multiplier
				product[i] += output
			} else {
				product[i] += mat[i][j] * divider * float64(vector[j])
			}
			sw = TransformFactor(product[i])
		}
	}

	// Generate the result bytes

	var res [32]uint8
	var scaledValues [32]uint8
	for i := 0; i < 64; i += 2 {
		pval := uint64(product[i]) + uint64(product[i+1])
		scaledValues[i/2] = uint8(pval & 0xFF)
	}
	for i := range 32 {
		res[i] = hashBytes[i] ^ scaledValues[i]
	}
	writer := hashes.Blake3HashWriter()
	writer.InfallibleWrite(res[:32])
	return writer.Finalize()
}

func (mat *matrix) bHeavyHash(hash *externalapi.DomainHash) *externalapi.DomainHash {
	hashBytes := hash.ByteArray()
	var vector [64]uint16
	var product [64]uint16
	for i := range 32 {
		vector[2*i] = uint16(hashBytes[i] >> 4)
		vector[2*i+1] = uint16(hashBytes[i] & 0x0F)
	}
	// Matrix-vector multiplication, and convert to 4 bits.
	for i := range 64 {
		var sum uint16
		for j := range 64 {
			sum += mat[i][j] * vector[j]
		}
		product[i] = sum >> 10
	}

	// Concatenate 4 LSBs back to 8 bit xor with sum1
	var res [32]byte
	for i := range res {
		res[i] = hashBytes[i] ^ (byte(product[2*i]<<4) | byte(product[2*i+1]))
	}
	// Hash again
	writer := hashes.BlakeHeavyHashWriter()
	writer.InfallibleWrite(res[:])
	return writer.Finalize()
}
