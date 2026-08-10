// Copyright (c) 2024 The HoosatNetwork developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package autoupdate

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/pkg/errors"
)

// GitHubRelease represents a GitHub release API response
type GitHubRelease struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	PublishedAt time.Time `json:"published_at"`
	Prerelease  bool      `json:"prerelease"`
	Draft       bool      `json:"draft"`
	Assets      []struct {
		Name        string `json:"name"`
		DownloadURL string `json:"browser_download_url"`
		Size        int64  `json:"size"`
	} `json:"assets"`
}

// GitHubClient handles communication with GitHub API
type GitHubClient struct {
	client      *http.Client
	owner       string
	repo        string
	userAgent   string
	rateLimiter *RateLimiter
}

// RateLimiter implements a simple rate limiter for GitHub API
type RateLimiter struct {
	lastRequest time.Time
	minInterval time.Duration
}

// NewGitHubClient creates a new GitHub API client
func NewGitHubClient(owner, repo string) *GitHubClient {
	return &GitHubClient{
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		owner:       owner,
		repo:        repo,
		userAgent:   fmt.Sprintf("HTND-AutoUpdater/%s-%s", runtime.GOOS, runtime.GOARCH),
		rateLimiter: NewRateLimiter(1 * time.Second), // GitHub rate limit: 60 requests/hour for unauthenticated
	}
}

// NewRateLimiter creates a new rate limiter
func NewRateLimiter(minInterval time.Duration) *RateLimiter {
	return &RateLimiter{
		lastRequest: time.Now().Add(-minInterval),
		minInterval: minInterval,
	}
}

// Wait enforces the rate limit
func (rl *RateLimiter) Wait() {
	sinceLast := time.Since(rl.lastRequest)
	if sinceLast < rl.minInterval {
		time.Sleep(rl.minInterval - sinceLast)
	}
	rl.lastRequest = time.Now()
}

// GetLatestRelease fetches the latest release from GitHub
func (gc *GitHubClient) GetLatestRelease(ctx context.Context, channel string) (*GitHubRelease, error) {
	gc.rateLimiter.Wait()

	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", gc.owner, gc.repo)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create request")
	}

	req.Header.Set("User-Agent", gc.userAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := gc.client.Do(req)
	if err != nil {
		return nil, errors.Wrap(err, "failed to fetch latest release")
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, errors.Errorf("GitHub API returned status %d: %s", resp.StatusCode, string(body))
	}

	var release GitHubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, errors.Wrap(err, "failed to decode release JSON")
	}

	// Filter based on channel
	if !gc.isReleaseValidForChannel(&release, channel) {
		return nil, errors.New("no valid release found for the specified channel")
	}

	return &release, nil
}

// GetAllReleases fetches all releases from GitHub
func (gc *GitHubClient) GetAllReleases(ctx context.Context) ([]GitHubRelease, error) {
	gc.rateLimiter.Wait()

	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases", gc.owner, gc.repo)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create request")
	}

	req.Header.Set("User-Agent", gc.userAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := gc.client.Do(req)
	if err != nil {
		return nil, errors.Wrap(err, "failed to fetch releases")
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, errors.Errorf("GitHub API returned status %d: %s", resp.StatusCode, string(body))
	}

	var releases []GitHubRelease
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		return nil, errors.Wrap(err, "failed to decode releases JSON")
	}

	return releases, nil
}

// GetNewestValidRelease returns the newest release that matches the channel criteria
func (gc *GitHubClient) GetNewestValidRelease(ctx context.Context, channel string) (*GitHubRelease, error) {
	releases, err := gc.GetAllReleases(ctx)
	if err != nil {
		return nil, err
	}

	var validReleases []GitHubRelease
	for _, release := range releases {
		if gc.isReleaseValidForChannel(&release, channel) {
			validReleases = append(validReleases, release)
		}
	}

	if len(validReleases) == 0 {
		return nil, errors.New("no valid releases found for the specified channel")
	}

	// Find the newest by published date
	newest := &validReleases[0]
	for _, release := range validReleases[1:] {
		if release.PublishedAt.After(newest.PublishedAt) {
			newest = &release
		}
	}

	return newest, nil
}

// isReleaseValidForChannel checks if a release is valid for the given channel
func (gc *GitHubClient) isReleaseValidForChannel(release *GitHubRelease, channel string) bool {
	switch channel {
	case "stable":
		// Only non-pre-release, non-draft releases
		return !release.Prerelease && !release.Draft
	case "beta":
		// Include pre-releases but not drafts
		return !release.Draft
	case "all":
		// Include everything
		return true
	default:
		return false
	}
}

// GetAssetForPlatform returns the download URL for the asset matching the current platform
func (release *GitHubRelease) GetAssetForPlatform() (string, error) {
	platform := runtime.GOOS
	arch := runtime.GOARCH

	// Map GOOS and GOARCH to the naming convention used in releases
	// HTND releases use: linux, osx, windows for OS and amd64, aarch64 for architecture
	osName := platform
	archName := arch

	// Map darwin to osx
	if platform == "darwin" {
		osName = "osx"
	}

	// Map windows to lowercase for pattern matching
	if platform == "windows" {
		osName = "windows"
	}

	// Map linux stays as linux
	if platform == "linux" {
		osName = "linux"
	}

	// Architecture names match directly: amd64, aarch64, arm64, etc.
	// No need to map amd64 to x86_64 or arm64 to aarch64 since releases use amd64/aarch64

	// Extract version from tag name (preserve leading 'v' for pattern matching)
	version := release.TagName

	// Try different naming patterns with full version (including v prefix)
	// Pattern: HTND-{version}-{os}-{arch}.zip or .tar.gz
	patterns := []string{
		fmt.Sprintf("HTND-%%s-%s-%s.zip", osName, archName),
		fmt.Sprintf("HTND-%%s-%s-%s.tar.gz", osName, archName),
		fmt.Sprintf("htnd-%%s-%s-%s.zip", osName, archName),
		fmt.Sprintf("htnd-%%s-%s-%s.tar.gz", osName, archName),
	}

	for _, pattern := range patterns {
		assetName := fmt.Sprintf(pattern, version)
		for _, asset := range release.Assets {
			if asset.Name == assetName {
				return asset.DownloadURL, nil
			}
		}
	}

	// Try with version without leading 'v'
	versionNoV := strings.TrimPrefix(version, "v")
	for _, pattern := range patterns {
		assetName := fmt.Sprintf(pattern, versionNoV)
		for _, asset := range release.Assets {
			if asset.Name == assetName {
				return asset.DownloadURL, nil
			}
		}
	}

	// Try patterns without architecture (e.g., HTND-v2.12.0-osx.zip)
	osOnlyPatterns := []string{
		fmt.Sprintf("HTND-%%s-%s.zip", osName),
		fmt.Sprintf("HTND-%%s-%s.tar.gz", osName),
		fmt.Sprintf("htnd-%%s-%s.zip", osName),
		fmt.Sprintf("htnd-%%s-%s.tar.gz", osName),
	}

	for _, pattern := range osOnlyPatterns {
		assetName := fmt.Sprintf(pattern, version)
		for _, asset := range release.Assets {
			if asset.Name == assetName {
				return asset.DownloadURL, nil
			}
		}
	}

	for _, pattern := range osOnlyPatterns {
		assetName := fmt.Sprintf(pattern, versionNoV)
		for _, asset := range release.Assets {
			if asset.Name == assetName {
				return asset.DownloadURL, nil
			}
		}
	}

	// Fallback: try to find any asset that matches the OS
	osLower := strings.ToLower(osName)
	archLower := strings.ToLower(archName)

	for _, asset := range release.Assets {
		nameLower := strings.ToLower(asset.Name)
		if strings.Contains(nameLower, osLower) {
			// If we have an architecture, prefer assets that match both
			if strings.Contains(nameLower, archLower) {
				log.Warnf("Using heuristic match for asset: %s", asset.Name)
				return asset.DownloadURL, nil
			}
		}
	}

	// Last resort: try to find any asset (should not happen)
	if len(release.Assets) > 0 {
		log.Warnf("Using first available asset as fallback: %s", release.Assets[0].Name)
		return release.Assets[0].DownloadURL, nil
	}

	return "", errors.Errorf("no asset found for platform %s/%s in release %s", platform, arch, release.TagName)
}

// GetCurrentVersion returns the current version from the running binary
func GetCurrentVersion() string {
	// Try to read version from embedded variable
	// This will be populated at build time with -ldflags
	return os.Getenv("HTND_VERSION")
}

// CompareVersions compares two version strings (simple semantic version comparison)
// Returns 1 if v1 > v2, -1 if v1 < v2, 0 if equal
func CompareVersions(v1, v2 string) int {
	// Remove leading 'v' if present
	v1 = strings.TrimPrefix(v1, "v")
	v2 = strings.TrimPrefix(v2, "v")

	// Split by dots
	parts1 := strings.Split(v1, ".")
	parts2 := strings.Split(v2, ".")

	// Pad with zeros to make equal length
	maxLen := len(parts1)
	if len(parts2) > maxLen {
		maxLen = len(parts2)
	}

	for i := 0; i < maxLen; i++ {
		var num1, num2 int
		if i < len(parts1) {
			fmt.Sscanf(parts1[i], "%d", &num1)
		}
		if i < len(parts2) {
			fmt.Sscanf(parts2[i], "%d", &num2)
		}

		if num1 > num2 {
			return 1
		}
		if num1 < num2 {
			return -1
		}
	}

	return 0
}

// IsNewerVersion checks if newVersion is newer than currentVersion
func IsNewerVersion(currentVersion, newVersion string) bool {
	return CompareVersions(newVersion, currentVersion) > 0
}
