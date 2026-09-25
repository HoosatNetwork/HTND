package app

import (
	"testing"

	"github.com/HoosatNetwork/HTND/v2/util"
)

// TestMergedAndValidatedFrozenAddresses is HTN-159's regression test.
//
// --freeze-address used to replace the built-in frozen address list outright, so freezing one more
// address silently unfroze the default one, despite the flag's own description ("can be specified
// multiple times") reading as additive. Addresses were also never decoded, so a mistyped, differently
// cased, or wrong-network address froze nothing with no warning.
func TestMergedAndValidatedFrozenAddresses(t *testing.T) {
	defaultAddress := "hoosat:qpkcfshjeazmwex3t7x7qlctmhhratqauhkd5j254vfnmnuec7k6q4yzppn5q"

	t.Run("appends to the defaults instead of replacing them", func(t *testing.T) {
		operatorAddress := "hoosat:qqu3e723wll2v7wcn0ppkskeu5k24ev6k052mxtlkkq7dulh9aas72fh57pt4"
		merged, err := mergedAndValidatedFrozenAddresses(
			[]string{defaultAddress}, []string{operatorAddress}, util.Bech32PrefixHoosat)
		if err != nil {
			t.Fatalf("mergedAndValidatedFrozenAddresses: %+v", err)
		}
		if len(merged) != 2 || merged[0] != defaultAddress || merged[1] != operatorAddress {
			t.Fatalf("expected [%s %s], got %v", defaultAddress, operatorAddress, merged)
		}
	})

	t.Run("rejects a malformed address instead of silently freezing nothing", func(t *testing.T) {
		_, err := mergedAndValidatedFrozenAddresses(
			[]string{defaultAddress}, []string{"not-a-real-address"}, util.Bech32PrefixHoosat)
		if err == nil {
			t.Fatalf("expected an error for a malformed --freeze-address, got none")
		}
	})

	t.Run("rejects an address from the wrong network", func(t *testing.T) {
		testnetAddress := "hoosattest:qqu3e723wll2v7wcn0ppkskeu5k24ev6k052mxtlkkq7dulh9aas72fh57pt4"
		_, err := mergedAndValidatedFrozenAddresses(
			[]string{defaultAddress}, []string{testnetAddress}, util.Bech32PrefixHoosat)
		if err == nil {
			t.Fatalf("expected an error for a --freeze-address from the wrong network, got none")
		}
	})

	t.Run("no extra addresses leaves the defaults untouched", func(t *testing.T) {
		merged, err := mergedAndValidatedFrozenAddresses([]string{defaultAddress}, nil, util.Bech32PrefixHoosat)
		if err != nil {
			t.Fatalf("mergedAndValidatedFrozenAddresses: %+v", err)
		}
		if len(merged) != 1 || merged[0] != defaultAddress {
			t.Fatalf("expected [%s], got %v", defaultAddress, merged)
		}
	})
}
