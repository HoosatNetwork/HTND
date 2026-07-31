// Copyright (c) 2013, 2014 The btcsuite developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package util_test

import (
	"math"
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/util"
)

func TestAmountCreation(t *testing.T) {
	tests := []struct {
		name     string
		amount   float64
		valid    bool
		expected util.Amount
	}{
		// Positive tests.
		{
			name:     "zero",
			amount:   0,
			valid:    true,
			expected: util.Amount(0),
		},
		{
			name:     "max producible",
			amount:   17100000000,
			valid:    true,
			expected: util.Amount(constants.MaxSompi),
		},
		{
			name:     "one hundred",
			amount:   100,
			valid:    true,
			expected: util.Amount(100 * constants.SompiPerHoosat),
		},
		{
			name:     "fraction",
			amount:   0.01234567,
			valid:    true,
			expected: util.Amount(1234567),
		},
		{
			name:     "rounding up",
			amount:   54.999999999999943157,
			valid:    true,
			expected: util.Amount(55 * constants.SompiPerHoosat),
		},
		{
			name:     "rounding down",
			amount:   55.000000000000056843,
			valid:    true,
			expected: util.Amount(55 * constants.SompiPerHoosat),
		},

		// Negative tests.
		{
			name:   "not-a-number",
			amount: math.NaN(),
			valid:  false,
		},
		{
			name:   "-infinity",
			amount: math.Inf(-1),
			valid:  false,
		},
		{
			name:   "+infinity",
			amount: math.Inf(1),
			valid:  false,
		},
	}

	for _, test := range tests {
		a, err := util.NewAmount(test.amount)
		switch {
		case test.valid && err != nil:
			t.Errorf("%v: Positive test Amount creation failed with: %v", test.name, err)
			continue
		case !test.valid && err == nil:
			t.Errorf("%v: Negative test Amount creation succeeded (value %v) when should fail", test.name, a)
			continue
		}

		if a != test.expected {
			t.Errorf("%v: Created amount %v does not match expected %v", test.name, a, test.expected)
			continue
		}
	}
}

func TestAmountUnitConversions(t *testing.T) {
	tests := []struct {
		name      string
		amount    util.Amount
		unit      util.AmountUnit
		converted float64
		s         string
	}{
		{
			name:      "MHSAT",
			amount:    util.Amount(constants.MaxSompi),
			unit:      util.AmountMegaHSAT,
			converted: 17100,
			s:         "17100 MHSAT",
		},
		{
			name:      "kHSAT",
			amount:    util.Amount(44433322211100),
			unit:      util.AmountKiloHSAT,
			converted: 444.33322211100,
			s:         "444.333222111 kHSAT",
		},
		{
			name:      "HTN",
			amount:    util.Amount(44433322211100),
			unit:      util.AmountHSAT,
			converted: 444333.22211100,
			s:         "444333.222111 HTN",
		},
		{
			name:      "mHSAT",
			amount:    util.Amount(44433322211100),
			unit:      util.AmountMilliHSAT,
			converted: 444333222.11100,
			s:         "444333222.111 mHSAT",
		},
		{
			name:      "μHSAT",
			amount:    util.Amount(44433322211100),
			unit:      util.AmountMicroHSAT,
			converted: 444333222111.00,
			s:         "444333222111 μHSAT",
		},
		{
			name:      "sompi",
			amount:    util.Amount(44433322211100),
			unit:      util.AmountSompi,
			converted: 44433322211100,
			s:         "44433322211100 Sompi",
		},
		{
			name:      "non-standard unit",
			amount:    util.Amount(44433322211100),
			unit:      util.AmountUnit(-1),
			converted: 4443332.2211100,
			s:         "4443332.22111 1e-1 HTN",
		},
	}

	for _, test := range tests {
		f := test.amount.ToUnit(test.unit)
		if f != test.converted {
			t.Errorf("%v: converted value %v does not match expected %v", test.name, f, test.converted)
			continue
		}

		s := test.amount.Format(test.unit)
		if s != test.s {
			t.Errorf("%v: format '%v' does not match expected '%v'", test.name, s, test.s)
			continue
		}

		// Verify that Amount.ToHSAT works as advertised.
		f1 := test.amount.ToUnit(util.AmountHSAT)
		f2 := test.amount.ToHSAT()
		if f1 != f2 {
			t.Errorf("%v: ToHSAT does not match ToUnit(AmountHSAT): %v != %v", test.name, f1, f2)
		}

		// Verify that Amount.String works as advertised.
		s1 := test.amount.Format(util.AmountHSAT)
		s2 := test.amount.String()
		if s1 != s2 {
			t.Errorf("%v: String does not match Format(AmountHSAT): %v != %v", test.name, s1, s2)
		}
	}
}

func TestAmountMulF64(t *testing.T) {
	tests := []struct {
		name string
		amt  util.Amount
		mul  float64
		res  util.Amount
	}{
		{
			name: "Multiply 0.1 HTN by 2",
			amt:  util.Amount(100e5), // 0.1 HTN
			mul:  2,
			res:  util.Amount(200e5), // 0.2 HTN
		},
		{
			name: "Multiply 0.2 HTN by 0.02",
			amt:  util.Amount(200e5), // 0.2 HTN
			mul:  1.02,
			res:  util.Amount(204e5), // 0.204 HTN
		},
		{
			name: "Round down",
			amt:  util.Amount(49), // 49 Sompi
			mul:  0.01,
			res:  util.Amount(0),
		},
		{
			name: "Round up",
			amt:  util.Amount(50), // 50 Sompi
			mul:  0.01,
			res:  util.Amount(1), // 1 Sompi
		},
		{
			name: "Multiply by 0.",
			amt:  util.Amount(1e8), // 1 HTN
			mul:  0,
			res:  util.Amount(0), // 0 HTN
		},
		{
			name: "Multiply 1 by 0.5.",
			amt:  util.Amount(1), // 1 Sompi
			mul:  0.5,
			res:  util.Amount(1), // 1 Sompi
		},
		{
			name: "Multiply 100 by 66%.",
			amt:  util.Amount(100), // 100 Sompi
			mul:  0.66,
			res:  util.Amount(66), // 66 Sompi
		},
		{
			name: "Multiply 100 by 66.6%.",
			amt:  util.Amount(100), // 100 Sompi
			mul:  0.666,
			res:  util.Amount(67), // 67 Sompi
		},
		{
			name: "Multiply 100 by 2/3.",
			amt:  util.Amount(100), // 100 Sompi
			mul:  2.0 / 3,
			res:  util.Amount(67), // 67 Sompi
		},
	}

	for _, test := range tests {
		a := test.amt.MulF64(test.mul)
		if a != test.res {
			t.Errorf("%v: expected %v got %v", test.name, test.res, a)
		}
	}
}
