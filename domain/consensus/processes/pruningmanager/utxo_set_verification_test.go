package pruningmanager

import (
	"strings"
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
)

func mustHash(t *testing.T, hexString string) *externalapi.DomainHash {
	t.Helper()
	hash, err := externalapi.NewDomainHashFromString(hexString)
	if err != nil {
		t.Fatalf("could not parse %s: %s", hexString, err)
	}
	return hash
}

// TestShouldRunStrictUTXOSetFitCheck pins the condition that gates both the fatal
// validateUTXOSetFitsCommitment call and the "Validating the UTXO set fits commitment" log line.
//
// The default-node case is the one that matters: the flag is off, so the check does not run - and
// therefore nothing may claim in the log that it did.
func TestShouldRunStrictUTXOSetFitCheck(t *testing.T) {
	genesis := mustHash(t, "0000000000000000000000000000000000000000000000000000000000000001")
	pruningPoint := mustHash(t, "a5390732d49c545c1435d43b6a3d529e3bf365fb6800f8ca003acc6e8bc33121")

	tests := []struct {
		name         string
		flagEnabled  bool
		pruningPoint *externalapi.DomainHash
		want         bool
	}{
		{
			name:         "default node: flag off, so the check does not run and must not be claimed",
			flagEnabled:  false,
			pruningPoint: pruningPoint,
			want:         false,
		},
		{
			name:         "flag off at genesis",
			flagEnabled:  false,
			pruningPoint: genesis,
			want:         false,
		},
		{
			name:         "flag on at genesis: still skipped, there is nothing to compare",
			flagEnabled:  true,
			pruningPoint: genesis,
			want:         false,
		},
		{
			name:         "flag on past genesis: the only case that actually validates",
			flagEnabled:  true,
			pruningPoint: pruningPoint,
			want:         true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pm := &pruningManager{
				shouldSanityCheckPruningUTXOSet: test.flagEnabled,
				genesisHash:                     genesis,
			}
			if got := pm.shouldRunStrictUTXOSetFitCheck(test.pruningPoint); got != test.want {
				t.Errorf("shouldRunStrictUTXOSetFitCheck = %t, want %t", got, test.want)
			}
		})
	}
}

func TestShortHash(t *testing.T) {
	hash := mustHash(t, "5164183495ca18a09f42a8e49dcee3487d3526929a60f7319c7c567a68c25830")

	if got := shortHash(hash); got != "5164183495ca18a0" {
		t.Errorf("shortHash = %q, want %q", got, "5164183495ca18a0")
	}
	if got := len(shortHash(hash)); got != shortHashLen {
		t.Errorf("shortHash length = %d, want %d", got, shortHashLen)
	}

	// A producer that could not supply a hash must read as absent, never as a hash of zeroes -
	// the status line is the thing operators act on.
	if got := shortHash(nil); got != "n/a" {
		t.Errorf("shortHash(nil) = %q, want %q", got, "n/a")
	}
	if strings.Contains(shortHash(nil), "0000") {
		t.Errorf("shortHash(nil) must not look like a hash")
	}
}

func TestUint64ToString(t *testing.T) {
	tests := map[uint64]string{
		0:                    "0",
		7:                    "7",
		221433570:            "221433570",
		14076815:             "14076815",
		18446744073709551615: "18446744073709551615",
	}
	for value, want := range tests {
		if got := uint64ToString(value); got != want {
			t.Errorf("uint64ToString(%d) = %q, want %q", value, got, want)
		}
	}
}
