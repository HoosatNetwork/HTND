package addressmanager

import (
	"math/rand"
	"testing"
)

// maxRandPoint is the largest value cryptoRandFloat32 returns.
const maxRandPoint = float32((1<<24)-1) / float32(1<<24)

// cumulativeShareFallsShort reports whether float32 rounding leaves the cumulative weight shares below randPoint,
// the condition under which the scan runs past the last entry.
func cumulativeShareFallsShort(weights []float32, randPoint float32) bool {
	sum := float32(0)
	for _, weight := range weights {
		sum += weight
	}
	scanPoint := float32(0)
	for _, weight := range weights {
		scanPoint += weight / sum
	}
	return scanPoint < randPoint
}

// TestWeightedRandNeverReturnsAPickedAddress pins that the weighted pick never returns an entry whose weight
// RandomAddresses has zeroed because it already picked that address. It did at a random point of exactly 0 with the
// first address picked, and whenever float32 rounding left the cumulative shares short of the random point with the
// last address picked, so connmanager was handed the same address twice and dialed it twice in one round.
func TestWeightedRandNeverReturnsAPickedAddress(t *testing.T) {
	weightsByShape := []float32{1000, 1500, 2000, 100, 50, 1, 500, 250}
	var shortfall []float32
	for seed := int64(1); seed <= 100 && shortfall == nil; seed++ {
		rng := rand.New(rand.NewSource(seed))
		weights := make([]float32, 1000, 1001)
		for i := range weights {
			weights[i] = weightsByShape[rng.Intn(len(weightsByShape))]
		}
		// The last address has already been picked; a zero weight changes neither the sum nor the shares.
		weights = append(weights, 0)
		if cumulativeShareFallsShort(weights, maxRandPoint) {
			shortfall = weights
		}
	}
	if shortfall == nil {
		t.Fatalf("setup: no weight table in 100 seeds whose cumulative shares fall short of the largest random point")
	}

	tests := []struct {
		name      string
		weights   []float32
		randPoint float32
	}{
		{"a random point of 0 with the first address already picked", []float32{0, 1000, 1000}, 0},
		{"rounding short of the random point with the last address already picked", shortfall, maxRandPoint},
		{"only the middle address left", []float32{0, 5, 0}, maxRandPoint},
	}
	for _, test := range tests {
		index := weightedRandAt(test.weights, test.randPoint)
		if test.weights[index] == 0 {
			t.Errorf("%s: returned index %d, an address that was already picked", test.name, index)
		}
	}
}
