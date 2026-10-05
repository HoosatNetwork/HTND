package pruningmanager

import (
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
)

func TestRefuseMismatchedPruningPointGateIsUnscheduled(t *testing.T) {
	gates := dagconfig.MainnetParams.HardForkGates
	if gates.RefuseMismatchedImportVersion != ^uint16(0) {
		t.Fatalf("RefuseMismatchedImportVersion is scheduled on mainnet at block version %d",
			gates.RefuseMismatchedImportVersion)
	}
	for _, version := range []uint16{1, 9, 10, 11, ^uint16(0) - 1} {
		if refuseMismatchedPruningPointForVersion(gates, version) {
			t.Errorf("mismatched pruning-point refusal active at reachable block version %d", version)
		}
	}
	if !refuseMismatchedPruningPointForVersion(gates, ^uint16(0)) {
		t.Fatal("mismatched pruning-point refusal does not activate at its gate")
	}
}
