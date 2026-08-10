// Copyright (c) 2024 The HoosatNetwork developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package autoupdate

import (
	"context"
	"fmt"
	"io"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/HoosatNetwork/HTND/version"
	"github.com/pkg/errors"
)

// UpdateStatus represents the current status of the updater
type UpdateStatus struct {
	CurrentVersion    string
	LatestVersion     string
	AvailableVersion  string
	LastCheckTime     time.Time
	LastCheckError    string
	UpdateInProgress  bool
	DownloadProgress  float64
	DownloadCompleted bool
	InstallPending    bool
	LastUpdateError   string
}

// Updater is the main auto-update manager
type Updater struct {
	config      *Config
	github      *GitHubClient
	downloader  *Downloader
	status      UpdateStatus
	statusMutex sync.RWMutex

	// Channels for communication
	updateAvailableChan chan struct{}
	shutdownChan        chan struct{}
	wg                  sync.WaitGroup

	// Callbacks
	onUpdateAvailable func(version string)
	onUpdateProgress  func(progress float64)
	onUpdateComplete  func(newVersion string, err error)

	// State
	ctx                  context.Context
	cancel               context.CancelFunc
	updateDir            string
	currentBinaryPath    string
	downloadedBinaryPath string
}

// NewUpdater creates a new auto-updater instance
func NewUpdater(cfg *Config) *Updater {
	// Validate config
	if cfg == nil {
		cfg = DefaultConfig()
	}

	// Ensure valid update channel
	if !IsValidUpdateChannel(cfg.UpdateChannel) {
		cfg.UpdateChannel = "stable"
		log.Warnf("Invalid update channel '%s', defaulting to 'stable'", cfg.UpdateChannel)
	}

	// Initialize random seed
	rand.Seed(time.Now().UnixNano())

	// Create context
	ctx, cancel := context.WithCancel(context.Background())

	// Determine current binary path
	currentBinaryPath, _ := os.Executable()

	// Create update directory
	updateDir := filepath.Join(filepath.Dir(currentBinaryPath), "updates")

	githubClient := NewGitHubClient(cfg.GitHubOwner, cfg.GitHubRepo)

	// Configure GitHub token for error reporting if provided
	if cfg.GitHubToken != "" {
		githubClient.SetToken(cfg.GitHubToken)
	}

	return &Updater{
		config:              cfg,
		github:              githubClient,
		downloader:          NewDownloader(),
		updateAvailableChan: make(chan struct{}, 1),
		shutdownChan:        make(chan struct{}),
		ctx:                 ctx,
		cancel:              cancel,
		updateDir:           updateDir,
		currentBinaryPath:   currentBinaryPath,
		status: UpdateStatus{
			CurrentVersion: version.Version(),
			LastCheckTime:  time.Now().Add(-cfg.CheckInterval),
		},
	}
}

// Start begins the auto-update process
func (u *Updater) Start() {
	if !u.config.Enabled {
		log.Info("Auto-update is disabled")
		return
	}

	if u.config.NotifyOnly {
		log.Info("Auto-update is in notify-only mode")
	}

	log.Infof("Starting auto-updater (check interval: %v, channel: %s)",
		u.config.CheckInterval, u.config.UpdateChannel)

	// Initial check
	u.wg.Add(1)
	go u.checkForUpdates()

	// Start periodic check
	u.wg.Add(1)
	go u.periodicCheck()
}

// Stop stops the auto-updater
func (u *Updater) Stop() {
	log.Info("Stopping auto-updater...")
	close(u.shutdownChan)
	u.cancel()
	u.wg.Wait()
	log.Info("Auto-updater stopped")
}

// periodicCheck runs periodic update checks
func (u *Updater) periodicCheck() {
	defer u.wg.Done()

	ticker := time.NewTicker(u.config.CheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-u.shutdownChan:
			return
		case <-ticker.C:
			u.wg.Add(1)
			go u.checkForUpdates()
		}
	}
}

// checkForUpdates checks for available updates
func (u *Updater) checkForUpdates() {
	defer u.wg.Done()

	u.statusMutex.Lock()
	u.status.UpdateInProgress = true
	u.status.LastCheckTime = time.Now()
	u.status.LastCheckError = ""
	u.statusMutex.Unlock()

	defer func() {
		u.statusMutex.Lock()
		u.status.UpdateInProgress = false
		u.statusMutex.Unlock()
	}()

	log.Info("Checking for updates...")

	// Get current version
	currentVersion := u.status.CurrentVersion
	if currentVersion == "" {
		currentVersion = version.Version()
		u.statusMutex.Lock()
		u.status.CurrentVersion = currentVersion
		u.statusMutex.Unlock()
	}

	log.Debugf("Current version: %s", currentVersion)

	// Get latest release from GitHub
	release, err := u.github.GetNewestValidRelease(u.ctx, u.config.UpdateChannel)
	if err != nil {
		u.statusMutex.Lock()
		u.status.LastCheckError = err.Error()
		u.statusMutex.Unlock()
		log.Errorf("Failed to get latest release: %v", err)
		u.reportErrorToGitHub(err)
		return
	}

	latestVersion := release.TagName
	log.Debugf("Latest version: %s", latestVersion)

	// Check if update is available
	if !IsNewerVersion(currentVersion, latestVersion) {
		log.Infof("No update available (current: %s, latest: %s)", currentVersion, latestVersion)
		u.statusMutex.Lock()
		u.status.LatestVersion = latestVersion
		u.statusMutex.Unlock()
		return
	}

	log.Infof("Update available: %s -> %s", currentVersion, latestVersion)

	// Store the available version
	u.statusMutex.Lock()
	u.status.AvailableVersion = latestVersion
	u.status.LatestVersion = latestVersion
	u.statusMutex.Unlock()

	// Notify about available update
	if u.onUpdateAvailable != nil {
		u.onUpdateAvailable(latestVersion)
	}

	// Signal that update is available
	select {
	case u.updateAvailableChan <- struct{}{}:
	default:
	}

	// If auto-download is enabled, start download
	if u.config.AutoDownload {
		u.downloadUpdate(release)
	}
}

// downloadUpdate downloads the update
func (u *Updater) downloadUpdate(release *GitHubRelease) {
	u.statusMutex.Lock()
	if u.status.UpdateInProgress {
		u.statusMutex.Unlock()
		log.Info("Download already in progress, skipping")
		return
	}
	u.status.UpdateInProgress = true
	u.status.DownloadProgress = 0
	u.status.DownloadCompleted = false
	u.statusMutex.Unlock()

	defer func() {
		u.statusMutex.Lock()
		u.status.UpdateInProgress = false
		u.statusMutex.Unlock()
	}()

	log.Infof("Downloading update: %s", release.TagName)

	// Get the download URL for the current platform
	assetURL, err := release.GetAssetForPlatform()
	if err != nil {
		u.statusMutex.Lock()
		u.status.LastUpdateError = err.Error()
		u.statusMutex.Unlock()
		log.Errorf("Failed to find asset for current platform: %v", err)
		u.reportErrorToGitHub(err)
		return
	}

	log.Infof("Found asset URL: %s", assetURL)

	// Create download path
	downloadPath := filepath.Join(u.updateDir, fmt.Sprintf("HTND-%s.tar.gz", release.TagName))

	// Download the file
	_, err = u.downloader.DownloadFile(u.ctx, assetURL, downloadPath)
	if err != nil {
		u.statusMutex.Lock()
		u.status.LastUpdateError = err.Error()
		u.statusMutex.Unlock()
		log.Errorf("Failed to download update: %v", err)
		u.reportErrorToGitHub(err)
		return
	}

	log.Infof("Update downloaded to: %s", downloadPath)

	u.statusMutex.Lock()
	u.status.DownloadProgress = 100
	u.status.DownloadCompleted = true
	u.statusMutex.Unlock()
	u.downloadedBinaryPath = downloadPath

	// Notify about download completion
	if u.onUpdateProgress != nil {
		u.onUpdateProgress(100)
	}

	// If auto-install is enabled, install the update after random delay
	if u.config.AutoInstall {
		// Generate random delay between InstallDelayMin and InstallDelayMax
		delay := u.config.InstallDelayMin +
			time.Duration(rand.Int63n(int64(u.config.InstallDelayMax-u.config.InstallDelayMin)))

		log.Infof("Waiting %v before auto-installing update %s", delay, release.TagName)
		time.Sleep(delay)
		u.installUpdate(release.TagName)
	}
}

// installUpdate installs the downloaded update
func (u *Updater) installUpdate(version string) {
	u.statusMutex.Lock()
	u.status.UpdateInProgress = true
	u.status.InstallPending = true
	u.statusMutex.Unlock()

	defer func() {
		u.statusMutex.Lock()
		u.status.UpdateInProgress = false
		u.status.InstallPending = false
		u.statusMutex.Unlock()
	}()

	log.Infof("Installing update: %s", version)

	// Extract the archive
	downloadPath := u.downloadedBinaryPath
	if downloadPath == "" {
		err := errors.New("no downloaded binary found")
		u.statusMutex.Lock()
		u.status.LastUpdateError = err.Error()
		u.statusMutex.Unlock()
		if u.onUpdateComplete != nil {
			u.onUpdateComplete("", err)
		}
		return
	}

	// Create extraction directory
	extractDir := filepath.Join(u.updateDir, fmt.Sprintf("extracted-%s", version))
	if err := os.RemoveAll(extractDir); err != nil && !os.IsNotExist(err) {
		log.Warnf("Failed to clean up old extraction directory: %v", err)
	}

	// Extract the archive
	_, err := ExtractArchive(downloadPath, extractDir)
	if err != nil {
		u.statusMutex.Lock()
		u.status.LastUpdateError = err.Error()
		u.statusMutex.Unlock()
		log.Errorf("Failed to extract archive: %v", err)
		u.reportErrorToGitHub(err)
		if u.onUpdateComplete != nil {
			u.onUpdateComplete("", err)
		}
		return
	}

	// Find the binary in the extracted directory
	binaryPath, err := FindBinaryInDirectory(extractDir)
	if err != nil {
		u.statusMutex.Lock()
		u.status.LastUpdateError = err.Error()
		u.statusMutex.Unlock()
		log.Errorf("Failed to find binary in extracted archive: %v", err)
		u.reportErrorToGitHub(err)
		// Try to clean up
		CleanupExtractedFiles(extractDir)
		if u.onUpdateComplete != nil {
			u.onUpdateComplete("", err)
		}
		return
	}

	log.Infof("Found binary at: %s", binaryPath)

	// Verify the binary is valid (basic check)
	if err := verifyBinary(binaryPath); err != nil {
		u.statusMutex.Lock()
		u.status.LastUpdateError = err.Error()
		u.statusMutex.Unlock()
		log.Errorf("Binary verification failed: %v", err)
		u.reportErrorToGitHub(err)
		CleanupExtractedFiles(extractDir)
		if u.onUpdateComplete != nil {
			u.onUpdateComplete("", err)
		}
		return
	}

	// Create backup of current binary
	backupPath := u.currentBinaryPath + ".backup"
	if err := createBackup(u.currentBinaryPath, backupPath); err != nil {
		log.Warnf("Failed to create backup: %v", err)
		// Continue without backup
	}

	// Install the new binary
	if err := installBinary(binaryPath, u.currentBinaryPath); err != nil {
		u.statusMutex.Lock()
		u.status.LastUpdateError = err.Error()
		u.statusMutex.Unlock()
		log.Errorf("Failed to install binary: %v", err)
		u.reportErrorToGitHub(err)
		// Restore backup if it exists
		if _, err := os.Stat(backupPath); err == nil {
			if restoreErr := restoreBackup(backupPath, u.currentBinaryPath); restoreErr != nil {
				log.Errorf("Failed to restore backup: %v", restoreErr)
			} else {
				log.Info("Restored backup successfully")
			}
		}
		CleanupExtractedFiles(extractDir)
		if u.onUpdateComplete != nil {
			u.onUpdateComplete("", err)
		}
		return
	}

	log.Infof("Binary installed successfully")

	// Clean up
	if err := CleanupExtractedFiles(extractDir); err != nil {
		log.Warnf("Failed to clean up extracted files: %v", err)
	}

	// Remove backup after successful installation
	if _, err := os.Stat(backupPath); err == nil {
		if err := os.Remove(backupPath); err != nil {
			log.Warnf("Failed to remove backup: %v", err)
		}
	}

	// Update status
	u.statusMutex.Lock()
	u.status.CurrentVersion = version
	u.status.LastUpdateError = ""
	u.status.DownloadCompleted = false
	u.statusMutex.Unlock()
	u.downloadedBinaryPath = ""

	log.Infof("Update to version %s installed successfully", version)

	// Notify about completion
	if u.onUpdateComplete != nil {
		u.onUpdateComplete(version, nil)
	}

	// Trigger restart if configured
	if u.config.AutoInstall {
		log.Info("Auto-restarting node with new version...")
		if err := u.RestartNode(); err != nil {
			u.statusMutex.Lock()
			u.status.LastUpdateError = err.Error()
			u.statusMutex.Unlock()
			log.Errorf("Failed to restart node: %v", err)
		}
	}
}

// CheckForUpdate manually triggers an update check
func (u *Updater) CheckForUpdate() {
	if !u.config.Enabled {
		return
	}
	u.wg.Add(1)
	go u.checkForUpdates()
}

// InstallUpdate manually triggers the installation of a downloaded update
func (u *Updater) InstallUpdate() {
	if !u.config.Enabled || !u.config.AutoDownload {
		return
	}

	u.statusMutex.Lock()
	if !u.status.DownloadCompleted {
		u.statusMutex.Unlock()
		log.Info("No downloaded update available to install")
		return
	}
	version := u.status.AvailableVersion
	u.statusMutex.Unlock()

	if version == "" {
		log.Info("No update version available")
		return
	}

	u.wg.Add(1)
	go u.installUpdate(version)
}

// GetStatus returns the current update status
func (u *Updater) GetStatus() UpdateStatus {
	u.statusMutex.RLock()
	defer u.statusMutex.RUnlock()
	return u.status
}

// SetOnUpdateAvailable sets the callback for when an update is available
func (u *Updater) SetOnUpdateAvailable(callback func(version string)) {
	u.onUpdateAvailable = callback
}

// SetOnUpdateProgress sets the callback for update progress
func (u *Updater) SetOnUpdateProgress(callback func(progress float64)) {
	u.onUpdateProgress = callback
}

// SetOnUpdateComplete sets the callback for when update is complete
func (u *Updater) SetOnUpdateComplete(callback func(newVersion string, err error)) {
	u.onUpdateComplete = callback
}

// reportErrorToGitHub reports an error to GitHub if token is configured
func (u *Updater) reportErrorToGitHub(err error) {
	if u.config.GitHubToken == "" {
		return
	}
	go func() {
		if reportErr := u.github.ReportError(
			u.ctx,
			err,
			version.Version(),
			runtime.GOOS,
			runtime.GOARCH,
		); reportErr != nil {
			log.Debugf("Failed to report error to GitHub: %v", reportErr)
		}
	}()
}

// verifyBinary performs basic verification of a binary
func verifyBinary(path string) error {
	// Check if file exists
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return errors.Wrap(err, "binary file does not exist")
	}

	// Check if it's a regular file
	fileInfo, err := os.Stat(path)
	if err != nil {
		return errors.Wrap(err, "failed to get file info")
	}

	if !fileInfo.Mode().IsRegular() {
		return errors.New("binary is not a regular file")
	}

	// Check if it's executable (on Unix-like systems)
	if runtime.GOOS != "windows" {
		if (fileInfo.Mode() & 0111) == 0 {
			// Try to make it executable
			if err := os.Chmod(path, 0755); err != nil {
				return errors.Wrap(err, "failed to make binary executable")
			}
		}
	}

	// Try to get file header to verify it's a valid executable
	file, err := os.Open(path)
	if err != nil {
		return errors.Wrap(err, "failed to open binary for verification")
	}
	defer file.Close()

	// Read the first few bytes to check for ELF or Mach-O magic numbers
	header := make([]byte, 4)
	if _, err := file.Read(header); err != nil && err != io.EOF {
		return errors.Wrap(err, "failed to read binary header")
	}

	// Check for ELF (Linux)
	if len(header) >= 4 && header[0] == 0x7F && string(header[1:4]) == "ELF" {
		return nil
	}

	// Check for Mach-O (macOS)
	if len(header) >= 4 && (header[0] == 0xCF && header[1] == 0xFA) ||
		(header[0] == 0xCE && header[1] == 0xFA) ||
		(header[0] == 0xFE && header[1] == 0xED) ||
		(header[0] == 0xCA && header[1] == 0xFE) {
		return nil
	}

	// Check for PE (Windows)
	if len(header) >= 2 && header[0] == 'M' && header[1] == 'Z' {
		return nil
	}

	// If we can't verify the header, just accept it (might be a script or other format)
	log.Warnf("Could not verify binary header for %s, but accepting anyway", path)
	return nil
}

// createBackup creates a backup of the current binary
func createBackup(src, dest string) error {
	log.Infof("Creating backup: %s -> %s", src, dest)

	// Remove existing backup if it exists
	if _, err := os.Stat(dest); err == nil {
		if err := os.Remove(dest); err != nil {
			return errors.Wrap(err, "failed to remove existing backup")
		}
	}

	// Copy the file
	if err := copyFile(src, dest); err != nil {
		return errors.Wrap(err, "failed to copy file for backup")
	}

	// Preserve permissions
	if err := preservePermissions(src, dest); err != nil {
		log.Warnf("Failed to preserve permissions for backup: %v", err)
	}

	return nil
}

// restoreBackup restores a backup
func restoreBackup(src, dest string) error {
	log.Infof("Restoring backup: %s -> %s", src, dest)
	return copyFile(src, dest)
}

// installBinary installs a new binary
func installBinary(src, dest string) error {
	log.Infof("Installing binary: %s -> %s", src, dest)

	// Remove existing binary if it exists
	if _, err := os.Stat(dest); err == nil {
		// On Unix, we need to make sure the file is writable
		if runtime.GOOS != "windows" {
			if err := os.Chmod(dest, 0755); err != nil {
				log.Warnf("Failed to make existing binary writable: %v", err)
			}
		}
		if err := os.Remove(dest); err != nil {
			return errors.Wrap(err, "failed to remove existing binary")
		}
	}

	// Copy the new binary
	if err := copyFile(src, dest); err != nil {
		return errors.Wrap(err, "failed to copy new binary")
	}

	// Set permissions
	if err := os.Chmod(dest, 0755); err != nil {
		return errors.Wrap(err, "failed to set executable permissions on new binary")
	}

	return nil
}

// copyFile copies a file from src to dest
func copyFile(src, dest string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return errors.Wrap(err, "failed to open source file")
	}
	defer srcFile.Close()

	destFile, err := os.Create(dest)
	if err != nil {
		return errors.Wrap(err, "failed to create destination file")
	}
	defer destFile.Close()

	if _, err := io.Copy(destFile, srcFile); err != nil {
		return errors.Wrap(err, "failed to copy file contents")
	}

	if err := destFile.Sync(); err != nil {
		return errors.Wrap(err, "failed to sync destination file")
	}

	return nil
}

// preservePermissions copies permissions from src to dest
func preservePermissions(src, dest string) error {
	srcInfo, err := os.Stat(src)
	if err != nil {
		return err
	}

	return os.Chmod(dest, srcInfo.Mode())
}

// RestartNode restarts the node with the new binary
// This is a separate function that can be called after an update is installed
func (u *Updater) RestartNode() error {
	log.Info("Restarting node...")

	// Get the current executable path
	binaryPath, err := os.Executable()
	if err != nil {
		return errors.Wrap(err, "failed to get executable path")
	}

	// Build the command to restart
	args := os.Args[1:] // Keep the same arguments
	cmd := exec.Command(binaryPath, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()

	// Start the new instance
	if err := cmd.Start(); err != nil {
		return errors.Wrap(err, "failed to start new instance")
	}

	log.Infof("New instance started with PID: %d", cmd.Process.Pid)

	// Shutdown the current instance
	log.Info("Shutting down current instance...")
	os.Exit(0)

	return nil
}
