package externalapi

import (
	"math/big"
	"testing"
)

// TestCompareBlueWorkMatchesBigIntCmp pins that CompareBlueWork orders exactly as comparing the
// BlueWork copies did, which the DAA window's minimum-timestamp tie-break relies on, and that it
// leaves both values untouched.
func TestCompareBlueWorkMatchesBigIntCmp(t *testing.T) {
	values := []*big.Int{big.NewInt(0), big.NewInt(1), big.NewInt(1), new(big.Int).Lsh(big.NewInt(1), 200)}
	for _, a := range values {
		for _, b := range values {
			dataA := NewBlockGHOSTDAGData(0, a, nil, nil, nil, nil, 0)
			dataB := NewBlockGHOSTDAGData(0, b, nil, nil, nil, nil, 0)
			if got, want := dataA.CompareBlueWork(dataB), a.Cmp(b); got != want {
				t.Fatalf("CompareBlueWork(%s, %s) = %d, want %d", a, b, got, want)
			}
			if dataA.BlueWork().Cmp(a) != 0 || dataB.BlueWork().Cmp(b) != 0 {
				t.Fatalf("CompareBlueWork modified a blue work")
			}
		}
	}

	withNil := NewBlockGHOSTDAGData(0, nil, nil, nil, nil, nil, 0)
	withZero := NewBlockGHOSTDAGData(0, big.NewInt(0), nil, nil, nil, nil, 0)
	if withNil.CompareBlueWork(withZero) != -1 || withZero.CompareBlueWork(withNil) != 1 ||
		withNil.CompareBlueWork(withNil) != 0 {
		t.Fatalf("a nil blue work must order before any other and equal another nil")
	}
}
