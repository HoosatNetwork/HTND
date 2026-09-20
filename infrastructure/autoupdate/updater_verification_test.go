// Copyright (c) 2024 The HoosatNetwork developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package autoupdate

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/infrastructure/os/signal"
)

// newTestUpdater builds an Updater without touching the network or the running binary's directory.
func newTestUpdater(t *testing.T, cfg *Config) *Updater {
	t.Helper()
	updater := NewUpdater(cfg)
	updater.updateDir = t.TempDir()
	t.Cleanup(updater.Stop)
	return updater
}

// TestInstallUpdateRefusesAnArchiveThatWasNeverVerified pins HTN-162's last line of defence.
//
// downloadUpdate already refuses to record an unverified archive, but InstallUpdate is exported and
// can be driven directly, so installUpdate must not trust downloadedBinaryPath on its own. The
// check compares against the exact path that was approved: a bool would have let a previously
// approved download vouch for a later, different file.
func TestInstallUpdateRefusesAnArchiveThatWasNeverVerified(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.AutoDownload = true
	updater := newTestUpdater(t, cfg)

	archivePath := filepath.Join(t.TempDir(), "HTND-v9.9.9-linux-amd64.tar.gz")
	if err := os.WriteFile(archivePath, []byte("unverified payload"), 0644); err != nil {
		t.Fatalf("writing archive: %+v", err)
	}
	updater.downloadedBinaryPath = archivePath
	// verifiedBinaryPath deliberately left empty: nothing approved this file.

	var completedErr error
	var completed sync.WaitGroup
	completed.Add(1)
	updater.SetOnUpdateComplete(func(_ string, err error) {
		completedErr = err
		completed.Done()
	})

	updater.installUpdate("v9.9.9")
	completed.Wait()

	if completedErr == nil {
		t.Fatal("installUpdate accepted an archive that was never verified")
	}
	if !strings.Contains(completedErr.Error(), "has not been verified") {
		t.Fatalf("expected a verification refusal, got: %v", completedErr)
	}

	// The archive must still be sitting there untouched rather than extracted.
	if _, err := os.Stat(filepath.Join(updater.updateDir, "extracted-v9.9.9")); !os.IsNotExist(err) {
		t.Fatalf("installUpdate extracted an unverified archive (stat error was %v)", err)
	}
}

// TestInstallUpdateRefusesAPathOtherThanTheVerifiedOne is the stale-approval case: one archive was
// verified, then downloadedBinaryPath moved on to a different file.
func TestInstallUpdateRefusesAPathOtherThanTheVerifiedOne(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.AutoDownload = true
	updater := newTestUpdater(t, cfg)

	dir := t.TempDir()
	verified := filepath.Join(dir, "good.tar.gz")
	other := filepath.Join(dir, "other.tar.gz")
	for _, path := range []string{verified, other} {
		if err := os.WriteFile(path, []byte("payload"), 0644); err != nil {
			t.Fatalf("writing %s: %+v", path, err)
		}
	}

	updater.verifiedBinaryPath = verified
	updater.downloadedBinaryPath = other

	var completedErr error
	var completed sync.WaitGroup
	completed.Add(1)
	updater.SetOnUpdateComplete(func(_ string, err error) {
		completedErr = err
		completed.Done()
	})

	updater.installUpdate("v9.9.9")
	completed.Wait()

	if completedErr == nil {
		t.Fatal("a previously verified archive vouched for a different file")
	}
	if !strings.Contains(completedErr.Error(), "has not been verified") {
		t.Fatalf("expected a verification refusal, got: %v", completedErr)
	}
}

// TestVerifyDownloadedArchiveRefusesWhenTheKeyIsMalformed pins that an operator's typo in
// --autoupdate-public-key is fatal to installing rather than a silent downgrade to no verification.
func TestVerifyDownloadedArchiveRefusesWhenTheKeyIsMalformed(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.ReleasePublicKey = "obviously not hex"
	updater := newTestUpdater(t, cfg)

	if updater.verifierErr == nil {
		t.Fatal("a malformed release key was accepted at construction")
	}

	archivePath := filepath.Join(t.TempDir(), "HTND-v9.9.9-linux-amd64.tar.gz")
	if err := os.WriteFile(archivePath, []byte("payload"), 0644); err != nil {
		t.Fatalf("writing archive: %+v", err)
	}

	err := updater.verifyDownloadedArchive(&GitHubRelease{TagName: "v9.9.9"},
		"HTND-v9.9.9-linux-amd64.tar.gz", archivePath)
	if err == nil {
		t.Fatal("verifyDownloadedArchive passed although the configured key is unusable")
	}
	if !strings.Contains(err.Error(), "cannot be used") {
		t.Fatalf("expected the refusal to name the unusable key, got: %v", err)
	}
}

// TestInstallUpdateDoesNotLeakAWaitGroupCount is HTN-231.
//
// InstallUpdate did u.wg.Add(1) and started installUpdate, which never called Done, so a single
// InstallUpdate left the counter permanently above zero and Stop's wg.Wait blocked forever - the
// node would hang on shutdown. The Done has to live at the call site: downloadUpdate also calls
// installUpdate synchronously without an Add, so a defer inside installUpdate would drive the
// counter negative and panic instead.
func TestInstallUpdateDoesNotLeakAWaitGroupCount(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.AutoDownload = true
	updater := NewUpdater(cfg)
	updater.updateDir = t.TempDir()

	// InstallUpdate only proceeds when a download is recorded as complete.
	updater.statusMutex.Lock()
	updater.status.DownloadCompleted = true
	updater.status.AvailableVersion = "v9.9.9"
	updater.statusMutex.Unlock()
	// No verified path, so the install itself is refused - which is exactly the path that used to
	// return without ever calling Done.
	updater.downloadedBinaryPath = filepath.Join(t.TempDir(), "missing.tar.gz")

	updater.InstallUpdate()

	stopped := make(chan struct{})
	go func() {
		updater.Stop()
		close(stopped)
	}()

	select {
	case <-stopped:
	case <-time.After(30 * time.Second):
		t.Fatal("Stop blocked after InstallUpdate: the wait group count from InstallUpdate was " +
			"never released, so a node with auto-update enabled hangs on shutdown")
	}
}

// TestRestartNodeRequestsShutdownInsteadOfExiting is HTN-164.
//
// RestartNode used to exec a second htnd and then call os.Exit(0): the replacement raced the
// incumbent for a datadir that was still open, and os.Exit skipped app.main's deferred
// componentManager.Stop and databaseContext.Close entirely.
//
// That this test returns at all is half the assertion - os.Exit(0) in the old code would have
// ended the whole test binary mid-run.
func TestRestartNodeRequestsShutdownInsteadOfExiting(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = true
	updater := NewUpdater(cfg)
	t.Cleanup(updater.Stop)

	// Stand in for app.main's interrupt listener.
	requested := make(chan struct{}, 1)
	go func() {
		<-signal.ShutdownRequestChannel
		requested <- struct{}{}
	}()

	if err := updater.RestartNode(); err != nil {
		t.Fatalf("RestartNode: %+v", err)
	}

	select {
	case <-requested:
	case <-time.After(30 * time.Second):
		t.Fatal("RestartNode returned without requesting a shutdown, so the node would keep " +
			"running on the old binary with no indication anything was wrong")
	}
}

// TestRestartNodeGivesUpWhenNothingIsListening pins the bound on that request.
// signal.ShutdownRequestChannel is unbuffered, so a send with no interrupt listener blocks
// forever - which in the updater's goroutine would wedge it silently for the life of the process.
func TestRestartNodeGivesUpWhenNothingIsListening(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = true
	updater := NewUpdater(cfg)

	// No listener. Cancelling the updater's context is the path that fires when the node is
	// already shutting down for an unrelated reason while a restart request is pending.
	updater.cancel()

	done := make(chan error, 1)
	go func() { done <- updater.RestartNode() }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("RestartNode reported success although no listener accepted the request")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("RestartNode blocked with no listener and a cancelled context")
	}
}
