package ghostdagmanager

import (
	"math/rand"
	"sort"
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
)

// TestHashLessMatchesHexStringOrder pins the substitution made when the hex-string comparisons in
// this package were replaced with DomainHash.Less.
//
// Those comparators called DomainHash.String() on both sides of every comparison, and String()
// allocates a fresh 64-character hex string each call - a live mainnet allocation profile attributed
// 141GB, 91.7% of all DomainHash.String allocation, to makeUMCVotingKey's two sorts alone. Less
// compares the underlying arrays with bytes.Compare and allocates nothing.
//
// The orders must agree exactly, because these sorts feed a cache key and, in the coinbase manager,
// the order merge set blocks are paid in. They do agree: lowercase hex is a monotonic encoding, and
// every DomainHash is the same fixed length, so lexicographic order over the hex text is the same
// total order as bytes.Compare over the bytes. This test is the executable form of that argument.
func TestHashLessMatchesHexStringOrder(t *testing.T) {
	random := rand.New(rand.NewSource(1))

	// Include bytes either side of hex's two digit runs ('0'-'9' at 0x30, 'a'-'f' at 0x61), which is
	// where a non-monotonic encoding would disagree, plus random hashes for general coverage.
	edgeBytes := []byte{0x00, 0x09, 0x0a, 0x0f, 0x10, 0x7f, 0x80, 0x99, 0xa0, 0xaf, 0xf0, 0xff}
	hashes := make([]*externalapi.DomainHash, 0, 256)
	for _, b := range edgeBytes {
		var array [externalapi.DomainHashSize]byte
		for i := range array {
			array[i] = b
		}
		hashes = append(hashes, externalapi.NewDomainHashFromByteArray(&array))

		// Same hash but differing in the last byte, so ordering has to be decided deep in the string.
		var tail [externalapi.DomainHashSize]byte
		for i := range tail {
			tail[i] = b
		}
		tail[externalapi.DomainHashSize-1] ^= 0xff
		hashes = append(hashes, externalapi.NewDomainHashFromByteArray(&tail))
	}
	for len(hashes) < 256 {
		var array [externalapi.DomainHashSize]byte
		random.Read(array[:])
		hashes = append(hashes, externalapi.NewDomainHashFromByteArray(&array))
	}

	for i, left := range hashes {
		for j, right := range hashes {
			if got, want := left.Less(right), left.String() < right.String(); got != want {
				t.Fatalf("hashes[%d].Less(hashes[%d]) = %t, but the hex comparison it replaced says %t\n  %s\n  %s",
					i, j, got, want, left, right)
			}
		}
	}

	byLess := append([]*externalapi.DomainHash(nil), hashes...)
	sort.Slice(byLess, func(i, j int) bool { return byLess[i].Less(byLess[j]) })

	byString := append([]*externalapi.DomainHash(nil), hashes...)
	sort.Slice(byString, func(i, j int) bool { return byString[i].String() < byString[j].String() })

	for i := range byLess {
		if !byLess[i].Equal(byString[i]) {
			t.Fatalf("sorting by Less and by hex string disagree at index %d:\n  %s\n  %s",
				i, byLess[i], byString[i])
		}
	}
}
