package autoupdate

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
	"time"
)

// downloadableRelease serves a release whose archive for this platform is not a real archive, so that an install
// attempt fails at extraction and never touches the running binary.
func downloadableRelease(t *testing.T) *GitHubRelease {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("not an archive"))
	}))
	t.Cleanup(server.Close)

	osName := runtime.GOOS
	if osName == "darwin" {
		osName = "osx"
	}
	release := &GitHubRelease{TagName: "v9.9.9"}
	release.Assets = append(release.Assets, struct {
		Name        string `json:"name"`
		DownloadURL string `json:"browser_download_url"`
		Size        int64  `json:"size"`
	}{Name: fmt.Sprintf("HTND-v9.9.9-%s-%s.tar.gz", osName, runtime.GOARCH), DownloadURL: server.URL})
	return release
}

func autoInstallConfig(delayMin, delayMax time.Duration) *Config {
	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.AutoDownload = true
	cfg.AutoInstall = true
	cfg.AllowUnverifiedInstall = true
	cfg.InstallDelayMin = delayMin
	cfg.InstallDelayMax = delayMax
	return cfg
}

// TestStopInterruptsTheWaitBeforeAnAutomaticInstall downloads an update with auto-install on, and stops the updater
// while it waits for its random install delay. ComponentManager.Stop calls Updater.Stop first and waits for it, so
// the node cannot shut down until that wait ends.
func TestStopInterruptsTheWaitBeforeAnAutomaticInstall(t *testing.T) {
	updater := NewUpdater(autoInstallConfig(time.Hour, 2*time.Hour))
	updater.updateDir = t.TempDir()

	updater.wg.Add(1)
	go func() {
		defer updater.wg.Done()
		updater.downloadUpdate(downloadableRelease(t))
	}()
	deadline := time.Now().Add(10 * time.Second)
	for !updater.GetStatus().DownloadCompleted {
		if time.Now().After(deadline) {
			t.Fatalf("the update was not downloaded: %s", updater.GetStatus().LastUpdateError)
		}
		time.Sleep(10 * time.Millisecond)
	}

	stopped := make(chan struct{})
	go func() {
		updater.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not return while the updater waited to install an update")
	}
}

// TestAutomaticInstallWithoutDelaysDoesNotPanic downloads an update with auto-install on and no install delays set,
// as the node configures the updater (app/component_manager.go sets neither delay).
func TestAutomaticInstallWithoutDelaysDoesNotPanic(t *testing.T) {
	updater := NewUpdater(autoInstallConfig(0, 0))
	updater.updateDir = t.TempDir()
	t.Cleanup(updater.Stop)

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("installing an update with no install delays panicked: %v", r)
		}
	}()
	updater.downloadUpdate(downloadableRelease(t))
	if !updater.GetStatus().DownloadCompleted {
		t.Fatalf("the update was not downloaded: %s", updater.GetStatus().LastUpdateError)
	}
}

// TestNoAutomaticInstallAfterStopWithoutDelays stops the updater right after a download completes, before the
// install wait, with no install delays set, as the node configures the updater. The wait's timer and the stop are
// then both ready, and select picks either one at random, so this is tried many times.
func TestNoAutomaticInstallAfterStopWithoutDelays(t *testing.T) {
	release := downloadableRelease(t)
	for range 100 {
		updater := NewUpdater(autoInstallConfig(0, 0))
		updater.updateDir = t.TempDir()
		// downloadUpdate runs on this goroutine, unregistered, so Stop returns once it has cancelled the updater.
		updater.SetOnUpdateProgress(func(float64) { updater.Stop() })
		installed := false
		updater.SetOnUpdateComplete(func(string, error) { installed = true })

		updater.downloadUpdate(release)
		if !updater.GetStatus().DownloadCompleted {
			t.Fatalf("the update was not downloaded: %s", updater.GetStatus().LastUpdateError)
		}
		if installed {
			t.Fatal("an update was installed after Stop")
		}
	}
}
