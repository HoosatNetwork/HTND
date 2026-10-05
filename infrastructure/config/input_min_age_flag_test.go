package config

import (
	"testing"

	"github.com/jessevdk/go-flags"
)

// TestInputMinAgeDAAFlagAndAlias pins the default of --input-min-age-daa and that the former name
// --coinbase-reorg-safety-margin still sets it.
func TestInputMinAgeDAAFlagAndAlias(t *testing.T) {
	tests := []struct {
		args []string
		want uint64
	}{
		{nil, 1000},
		{[]string{"--input-min-age-daa=250"}, 250},
		{[]string{"--input-min-age-daa=0"}, 0},
		{[]string{"--coinbase-reorg-safety-margin=0"}, 0},
		{[]string{"--coinbase-reorg-safety-margin=77"}, 77},
	}
	for _, test := range tests {
		cfgFlags := defaultFlags()
		if _, err := newConfigParser(cfgFlags, flags.None).ParseArgs(test.args); err != nil {
			t.Fatalf("%v: ParseArgs: %s", test.args, err)
		}
		applyDeprecatedFlagAliases(cfgFlags)
		if cfgFlags.InputMinAgeDAA != test.want {
			t.Errorf("%v: InputMinAgeDAA = %d, want %d", test.args, cfgFlags.InputMinAgeDAA, test.want)
		}
	}
}
