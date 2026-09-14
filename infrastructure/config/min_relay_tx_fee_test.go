package config

import (
	"testing"

	"github.com/HoosatNetwork/HTND/util"
)

// TestMinRelayTxFeeIsValid pins that --minrelaytxfee must be positive. util.Amount is unsigned, so
// util.NewAmount turns a negative fee into a huge amount, and only a zero amount used to be rejected: a negative
// fee was accepted as an enormous minimum relay fee that made the node reject essentially every transaction.
func TestMinRelayTxFeeIsValid(t *testing.T) {
	tests := []struct {
		flag  float64
		valid bool
	}{
		{flag: -1, valid: false},
		{flag: -1e-8, valid: false},
		{flag: 0, valid: false},
		{flag: 1e-12, valid: false}, // rounds to zero sompi
		{flag: 1e-5, valid: true},
		{flag: 1, valid: true},
	}
	for _, test := range tests {
		fee, err := util.NewAmount(test.flag)
		if err != nil {
			t.Fatalf("NewAmount(%v): %+v", test.flag, err)
		}
		if got := minRelayTxFeeIsValid(test.flag, fee); got != test.valid {
			t.Errorf("minrelaytxfee %v (amount %d): valid = %t, want %t", test.flag, fee, got, test.valid)
		}
	}
}
