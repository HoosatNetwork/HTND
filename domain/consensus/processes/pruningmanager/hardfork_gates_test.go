package pruningmanager

import (
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
)

func TestRefuseMismatchedPruningPointGateIsUnscheduled(t *testing.T) {
	if dagconfig.RefuseMismatchedImportVersion != ^uint16(0) {
		t.Fatalf("RefuseMismatchedImportVersion is scheduled at block version %d",
			dagconfig.RefuseMismatchedImportVersion)
	}
	for _, version := range []uint16{1, 9, 10, 11, ^uint16(0) - 1} {
		if refuseMismatchedPruningPointForVersion(version) {
			t.Errorf("mismatched pruning-point refusal active at reachable block version %d", version)
		}
	}
	if !refuseMismatchedPruningPointForVersion(^uint16(0)) {
		t.Fatal("mismatched pruning-point refusal does not activate at its gate")
	}
}
