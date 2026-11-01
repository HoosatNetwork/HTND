package pruningmanager

import (
	"sync"
	"testing"
)

func TestPruningDiagnosticsDefaultHasNoDecorator(t *testing.T) {
	lock := &sync.Mutex{}
	pm := &pruningManager{}
	if got := pm.diagnosticLock(lock, "journal"); got != lock {
		t.Fatal("default installed a diagnostic locker")
	}
	ConfigurePruningDiagnostics(pm, true)
	got := pm.diagnosticLock(lock, "journal")
	if got == lock {
		t.Fatal("diagnostics did not install a locker")
	}
	got.Lock()
	held := !lock.TryLock()
	got.Unlock()
	if !held {
		t.Fatal("diagnostics did not acquire consensus ownership")
	}
	if !lock.TryLock() {
		t.Fatal("diagnostics did not release consensus ownership")
	}
	lock.Unlock()
}
