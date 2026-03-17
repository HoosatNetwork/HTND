package random

import (
	"errors"
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
	originalReadRandom := readRandom
	defer func() {
		readRandom = originalReadRandom
	}()

	expectedErr := errors.New("forced random read failure")
	readRandom = func(_ []byte) (int, error) {
		return 0, expectedErr
	}

	value, err := Uint64()
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected error %v, got %v", expectedErr, err)
	}
	if value != 0 {
		t.Fatalf("expected zero value on error, got %d", value)
	}
}
