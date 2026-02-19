package random

import (
	"fmt"
	"testing"
)

// TestRandomUint64 exercises the randomness of the random number generator on
// the system by ensuring the probability of the generated numbers. If the RNG
// is evenly distributed as a proper cryptographic RNG should be, there really
// should only be 1 number < 2^56 in 2^8 tries for a 64-bit number. However,
// use a higher number of 5 to really ensure the test doesn't fail unless the
// RNG is just horrendous.
func TestRandomUint64(t *testing.T) {
	tries := 1 << 8              // 2^8
	watermark := uint64(1 << 56) // 2^56
	maxHits := 5

	numHits := 0
	for i := range tries {
		nonce, err := Uint64()
		if err != nil {
			t.Errorf("RandomUint64 iteration %d failed - err %v",
				i, err)
			return
		}
		if nonce < watermark {
			numHits++
		}
		if numHits > maxHits {
			str := fmt.Sprintf("The random number generator on this system is clearly "+
				"terrible since we got %d values less than %d in %d runs "+
				"when only %d was expected", numHits, watermark, tries, maxHits)
			t.Errorf("Random Uint64 iteration %d failed - %v %v", i,
				str, numHits)
			return
		}
	}
}

// TestRandomUint64Errors uses a fake reader to force error paths to be executed
// and checks the results accordingly.
func TestRandomUint64Errors(t *testing.T) {
	// On modern Go (notably on Windows), crypto/rand.Read failures can terminate
	// the process via an unrecoverable runtime throw (see https://go.dev/issue/66821),
	// so we can't safely induce the error path in a unit test.
	//
	// Uint64 still returns (uint64, error) for API compatibility, but the error
	// path isn't reliably testable without changing the production implementation.
	t.Skip("cannot safely force crypto/rand.Read failure in a unit test")
}
