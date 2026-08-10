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
}

// DefaultConfig returns the default auto-update configuration
func DefaultConfig() *Config {
	return &Config{
		Enabled:        false,
		CheckInterval:  24 * time.Hour,
		GitHubOwner:    "HoosatNetwork",
		GitHubRepo:     "HTND",
		UpdateChannel:  "stable",
		AutoDownload:   true,
		AutoInstall:    false, // Safety: don't auto-install by default
		NotifyOnly:     false,
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
