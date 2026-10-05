package externalapi

import (
	"encoding/hex"
	"sync"
	"testing"
)

// TestDomainSubnetworkIDStringIsSafeConcurrently pins that formatting subnetwork IDs from several goroutines at
// once returns each ID's own hex. String used to hex-encode into one package-level buffer and copy it out, so a
// concurrent call could overwrite the buffer between the two steps and hand back another ID - RPC transaction
// conversion and log formatting run on many goroutines.
func TestDomainSubnetworkIDStringIsSafeConcurrently(t *testing.T) {
	var first, second DomainSubnetworkID
	for i := range first {
		first[i] = byte(i + 1)
		second[i] = byte(0xff - i)
	}
	ids := []DomainSubnetworkID{first, second}
	want := []string{hex.EncodeToString(first[:]), hex.EncodeToString(second[:])}

	const goroutines = 8
	const iterations = 20000
	var wg sync.WaitGroup
	errs := make(chan string, goroutines)
	for g := range goroutines {
		wg.Add(1)
		go func(which int) {
			defer wg.Done()
			for range iterations {
				if got := ids[which].String(); got != want[which] {
					errs <- got
					return
				}
			}
		}(g % 2)
	}
	wg.Wait()
	close(errs)
	for got := range errs {
		t.Fatalf("String returned %s, which is not the hex of the ID it was called on (%s or %s)", got, want[0], want[1])
	}
}
