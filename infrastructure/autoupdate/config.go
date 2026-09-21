// Copyright (c) 2024 The HoosatNetwork developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package autoupdate

import (
	"time"
)

// Config holds the configuration for the auto-updater
type Config struct {
	// Enabled determines if auto-update is active
	Enabled bool

	// CheckInterval is how often to check for updates
	CheckInterval time.Duration

	// GitHubOwner is the GitHub repository owner (default: HoosatNetwork)
	GitHubOwner string

	// GitHubRepo is the GitHub repository name (default: HTND)
	GitHubRepo string

	// UpdateChannel determines which releases to consider:
	// "stable" - only tagged releases (default)
	// "beta" - include pre-releases
	// "all" - include all releases including drafts
	UpdateChannel string

	// AutoDownload determines if updates should be downloaded automatically
	// If false, only notifications will be logged
	AutoDownload bool

	// AutoInstall determines if updates should be installed automatically
	// If false, downloads will be stored but not installed
	// Requires AutoDownload to be true
	AutoInstall bool

	// NotifyOnly determines if the updater should only log notifications
	// without performing any actions (overrides AutoDownload and AutoInstall)
	NotifyOnly bool

	// InstallDelayMin is the minimum random delay before auto-installing
	InstallDelayMin time.Duration

	// InstallDelayMax is the maximum random delay before auto-installing
	InstallDelayMax time.Duration

	// GitHubToken is a personal access token for auto-filing error reports
	// Requires 'repo' scope. If empty, error reporting is disabled.
	GitHubToken string

	// AutoReportIssues determines if issues should be automatically reported to GitHub
	// When enabled (default), panics and update errors will create GitHub issues
	// Requires GitHubToken to be set for reporting to work
	AutoReportIssues bool

	// ReleasePublicKey is the hex-encoded ed25519 public key that release checksum lists are
	// signed with. Empty falls back to the build's pinnedReleasePublicKey, which is itself empty
	// unless a release captain set it - see verify.go.
	ReleasePublicKey string

	// AllowUnverifiedInstall permits installing an archive whose authenticity was never
	// established. It exists only because no signing key ships in this repository; it must never
	// be on by default. See ReleaseVerifier.
	AllowUnverifiedInstall bool
}

// DefaultConfig returns the default auto-update configuration
func DefaultConfig() *Config {
	return &Config{
		Enabled:          false,
		CheckInterval:    24 * time.Hour,
		GitHubOwner:      "HoosatNetwork",
		GitHubRepo:       "HTND",
		UpdateChannel:    "stable",
		AutoDownload:     false,
		AutoInstall:      false,
		NotifyOnly:       false,
		InstallDelayMin:  30 * time.Minute,
		InstallDelayMax:  180 * time.Minute,
		GitHubToken:      "",
		AutoReportIssues: false,

		// No key ships here, and unverified installs are never a default (HTN-162).
		ReleasePublicKey:       "",
		AllowUnverifiedInstall: false,
	}
}

// IsValidUpdateChannel checks if the update channel is valid
func IsValidUpdateChannel(channel string) bool {
	switch channel {
	case "stable", "beta", "all":
		return true
	}
	return false
}
