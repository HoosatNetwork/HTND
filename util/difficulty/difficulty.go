package difficulty

import (
	"math"
	"math/big"
	"sync"
	"time"
)

var (
	// bigOne is 1 represented as a big.Int. It is defined here to avoid
	// the overhead of creating it multiple times.
	bigOne = big.NewInt(1)

	// oneLsh256 is 1 shifted left 256 bits. It is defined here to avoid
	// the overhead of creating it multiple times.
	oneLsh256 = new(big.Int).Lsh(bigOne, 256)

	// intPool caches scratch big.Int objects to prevent heap leaks on hot paths.
	intPool = sync.Pool{
		New: func() any {
			return new(big.Int)
		},
	}
)

// getScratchInt acquires a clean big.Int from the pool.
func getScratchInt() *big.Int {
	bi := intPool.Get().(*big.Int)
	bi.SetInt64(0)
	return bi
}

// putScratchInt returns a big.Int to the pool.
func putScratchInt(bi *big.Int) {
	intPool.Put(bi)
}

// CompactToBig converts a compact representation of a whole number N to an
// unsigned 32-bit number.
func CompactToBig(compact uint32) *big.Int {
	destination := big.NewInt(0)
	CompactToBigWithDestination(compact, destination)
	return destination
}

// CompactToBigWithDestination is a version of CompactToBig that
// takes a destination parameter. This is useful for saving memory,
// as then the destination big.Int can be reused.
func CompactToBigWithDestination(compact uint32, destination *big.Int) {
	mantissa := compact & 0x007fffff
	isNegative := compact&0x00800000 != 0
	exponent := uint(compact >> 24)

	if exponent <= 3 {
		mantissa >>= 8 * (3 - exponent)
		destination.SetInt64(int64(mantissa))
	} else {
		destination.SetInt64(int64(mantissa))
		destination.Lsh(destination, 8*(exponent-3))
	}

	if isNegative {
		destination.Neg(destination)
	}
}

// BigToCompact converts a whole number N to a compact representation using
// an unsigned 32-bit number.
func BigToCompact(n *big.Int) uint32 {
	if n.Sign() == 0 {
		return 0
	}

	var mantissa uint32
	// Calculate the base-256 exponent using zero-allocation BitLen math instead of len(n.Bytes())
	exponent := uint((n.BitLen() + 7) / 8)

	if exponent <= 3 {
		bits := n.Bits()
		if len(bits) > 0 {
			word := uint64(bits[0])
			if word <= math.MaxUint32 {
				mantissa = uint32(word)
			} else {
				mantissa = math.MaxUint32
			}
		} else {
			mantissa = 0
		}
		mantissa <<= 8 * (3 - exponent)
	} else {
		// Borrow a cached big.Int instance instead of instantiating new(big.Int)
		tn := getScratchInt()
		tn.Set(n)
		tn.Rsh(tn, 8*(exponent-3))
		bits := tn.Bits()
		if len(bits) > 0 {
			word := uint64(bits[0])
			if word <= math.MaxUint32 {
				mantissa = uint32(word)
			} else {
				mantissa = math.MaxUint32
			}
		} else {
			mantissa = 0
		}
		putScratchInt(tn)
	}

	if mantissa&0x00800000 != 0 {
		mantissa >>= 8
		exponent++
	}

	exponentUint64 := uint64(exponent)
	exponentUint32 := uint32(math.MaxUint32)
	if exponentUint64 <= math.MaxUint32 {
		exponentUint32 = uint32(exponentUint64)
	}
	compact := (exponentUint32 << 24) | mantissa
	if n.Sign() < 0 {
		compact |= 0x00800000
	}
	return compact
}

// CalcWork calculates a work value from difficulty bits.
func CalcWork(bits uint32) *big.Int {
	difficultyNum := getScratchInt()
	CompactToBigWithDestination(bits, difficultyNum)

	if difficultyNum.Sign() <= 0 {
		putScratchInt(difficultyNum)
		return big.NewInt(0)
	}

	denominator := getScratchInt()
	denominator.Add(difficultyNum, bigOne)

	result := big.NewInt(0)
	result.Div(oneLsh256, denominator)

	putScratchInt(difficultyNum)
	putScratchInt(denominator)
	return result
}

func getHashrate(target *big.Int, targetTimePerBlock time.Duration) *big.Int {
	tmp := getScratchInt()
	divisor := getScratchInt()

	divisor.Set(target)
	divisor.Mul(divisor, tmp.SetInt64(targetTimePerBlock.Milliseconds()))
	divisor.Div(divisor, tmp.SetInt64(int64(time.Second/time.Millisecond)))

	result := big.NewInt(0)
	result.Div(oneLsh256, divisor)

	putScratchInt(tmp)
	putScratchInt(divisor)
	return result
}

// GetHashrateString returns the expected hashrate of the network on a certain difficulty target.
func GetHashrateString(target *big.Int, targetTimePerBlock time.Duration) string {
	hashrate := getHashrate(target, targetTimePerBlock)
	in := hashrate.Text(10)

	var postfix string
	switch {
	case len(in) <= 3:
		return in + " H/s"
	case len(in) <= 6:
		postfix = " KH/s"
	case len(in) <= 9:
		postfix = " MH/s"
	case len(in) <= 12:
		postfix = " GH/s"
	case len(in) <= 15:
		postfix = " TH/s"
	case len(in) <= 18:
		postfix = " PH/s"
	case len(in) <= 21:
		postfix = " EH/s"
	default:
		return in + " H/s"
	}
	highPrecision := len(in) - ((len(in)-1)/3)*3
	return in[:highPrecision] + "." + in[highPrecision:highPrecision+2] + postfix
}
