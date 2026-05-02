package dagconfig_test

import (
	"testing"

	"github.com/Hoosat-Oy/HTND/domain/dagconfig"
)

// Define some of the required parameters for a user-registered
// network. This is necessary to test the registration of and
// lookup of encoding magics from the network.
var mockNetParams = dagconfig.Params{
	Name: "mocknet",
	Net:  1<<32 - 1,
}

func TestRegister(t *testing.T) {
	type registerTest struct {
		name   string
		params *dagconfig.Params
		err    error
	}

	tests := []struct {
		name     string
		register []registerTest
	}{
		{
			name: "default networks",
			register: []registerTest{
				{
					name:   "duplicate mainnet",
					params: &dagconfig.MainnetParams,
					err:    dagconfig.ErrDuplicateNet,
				},
				{
					name:   "duplicate testnet",
					params: &dagconfig.TestnetParams,
					err:    dagconfig.ErrDuplicateNet,
				},
				{
					name:   "duplicate simnet",
					params: &dagconfig.SimnetParams,
					err:    dagconfig.ErrDuplicateNet,
				},
			},
		},
		{
			name: "register mocknet",
			register: []registerTest{
				{
					name:   "mocknet",
					params: &mockNetParams,
					err:    nil,
				},
			},
		},
		{
			name: "more duplicates",
			register: []registerTest{
				{
					name:   "duplicate mainnet",
					params: &dagconfig.MainnetParams,
					err:    dagconfig.ErrDuplicateNet,
				},
				{
					name:   "duplicate testnet",
					params: &dagconfig.TestnetParams,
					err:    dagconfig.ErrDuplicateNet,
				},
				{
					name:   "duplicate simnet",
					params: &dagconfig.SimnetParams,
					err:    dagconfig.ErrDuplicateNet,
				},
				{
					name:   "duplicate mocknet",
					params: &mockNetParams,
					err:    dagconfig.ErrDuplicateNet,
				},
			},
		},
	}

	for _, test := range tests {
		for _, network := range test.register {
			err := dagconfig.Register(network.params)

			if err != network.err {
				t.Errorf("%s:%s: Registered network with unexpected error: got %v expected %v",
					network.name, network.name, err, network.err)
			}
		}
	}
}
