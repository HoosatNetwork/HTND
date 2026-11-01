package database

import (
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"testing"
)

func TestHistoryMaskArchivalAndRetirement(t *testing.T) {
	hash := externalapi.NewDomainHashFromByteArray(&[32]byte{1})
	other := externalapi.NewDomainHashFromByteArray(&[32]byte{2})
	for _, withData := range []bool{false, true} {
		var mask HistoryMask
		mask.Publish(NewHistoryMaskSet([]*externalapi.DomainHash{hash}, withData))
		for reader := HistoryMaskStatus; reader < HistoryMaskReaders; reader++ {
			if got, want := mask.Masks(reader, hash), withData || reader == HistoryMaskStatus; got != want {
				t.Fatalf("reader %d archival %t: got %t want %t", reader, !withData, got, want)
			}
			if mask.Masks(reader, other) {
				t.Fatal("unplanned block masked")
			}
		}
		mask.Publish(nil)
		if mask.Masks(HistoryMaskStatus, hash) {
			t.Fatal("mask survived retirement")
		}
	}
}
