package main

import (
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
)

func TestPartitionOf(t *testing.T) {
	id, err := externalapi.NewDomainTransactionIDFromString(
		"abcdef0000000000000000000000000000000000000000000000000000000000")
	if err != nil {
		t.Fatal(err)
	}
	outpoint := &externalapi.DomainOutpoint{TransactionID: *id}
	for _, c := range []struct {
		bits int
		want uint32
		name string
	}{{4, 0xa, "a/4"}, {12, 0xabc, "abc/12"}, {16, 0xabcd, "abcd/16"}, {20, 0xabcde, "abcde/20"}} {
		got := partitionOf(outpoint, c.bits)
		if got != c.want {
			t.Errorf("bits %d: partition %x, want %x", c.bits, got, c.want)
		}
		if name := partitionName(got, c.bits); name != c.name {
			t.Errorf("bits %d: name %s, want %s", c.bits, name, c.name)
		}
	}
}
