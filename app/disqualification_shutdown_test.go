package app

import (
	"errors"
	"testing"
)

type fakeDisqualifiedTipRepairer struct {
	resetCount   uint64
	repairErr    error
	resolveErr   error
	resolveCalls int
}

func (f *fakeDisqualifiedTipRepairer) RepairDisqualifiedTipChains() (uint64, error) {
	return f.resetCount, f.repairErr
}

func (f *fakeDisqualifiedTipRepairer) ResolveVirtual(func(uint64, uint64)) error {
	f.resolveCalls++
	return f.resolveErr
}

func TestRecoverDisqualifiedTipChains(t *testing.T) {
	t.Run("repairs and resolves", func(t *testing.T) {
		repairer := &fakeDisqualifiedTipRepairer{resetCount: 15}
		reset, err := recoverDisqualifiedTipChains(repairer)
		if err != nil || reset != 15 || repairer.resolveCalls != 1 {
			t.Fatalf("reset=%d resolveCalls=%d err=%v", reset, repairer.resolveCalls, err)
		}
	})

	t.Run("does not resolve when nothing was reset", func(t *testing.T) {
		repairer := &fakeDisqualifiedTipRepairer{}
		reset, err := recoverDisqualifiedTipChains(repairer)
		if err != nil || reset != 0 || repairer.resolveCalls != 0 {
			t.Fatalf("reset=%d resolveCalls=%d err=%v", reset, repairer.resolveCalls, err)
		}
	})

	t.Run("propagates repair failure", func(t *testing.T) {
		wantErr := errors.New("repair failed")
		repairer := &fakeDisqualifiedTipRepairer{repairErr: wantErr}
		_, err := recoverDisqualifiedTipChains(repairer)
		if !errors.Is(err, wantErr) || repairer.resolveCalls != 0 {
			t.Fatalf("resolveCalls=%d err=%v", repairer.resolveCalls, err)
		}
	})
}
