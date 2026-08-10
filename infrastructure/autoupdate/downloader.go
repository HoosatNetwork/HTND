// Copyright (c) 2024 The HoosatNetwork developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package autoupdate

import (
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pkg/errors"
)

// DownloadProgress represents the progress of a download
type DownloadProgress struct {
	TotalBytes   int64
	Downloaded   int64
	Percentage   float64
	SpeedBytesPS float64
}

// Downloader handles downloading and verifying update files
type Downloader struct {
	client *http.Client
}

// NewDownloader creates a new downloader
func NewDownloader() *Downloader {
	return &Downloader{
		client: &http.Client{
			Timeout: 300 * time.Second, // 5 minutes for large downloads
		},
	}
}

// DownloadFile downloads a file from a URL to a destination path
// Returns the path to the downloaded file and any error
func (d *Downloader) DownloadFile(ctx context.Context, url, destPath string) (string, error) {
	log.Infof("Downloading update from: %s", url)

	// Create destination directory if it doesn't exist
	destDir := filepath.Dir(destPath)
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return "", errors.Wrap(err, "failed to create destination directory")
	}

	// Create a temporary file for downloading
	tmpPath := destPath + ".tmp"
	tmpFile, err := os.Create(tmpPath)
	if err != nil {
		return "", errors.Wrap(err, "failed to create temporary file")
	}
	defer tmpFile.Close()

	// Create request
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", errors.Wrap(err, "failed to create download request")
	}

	// Get the file
	resp, err := d.client.Do(req)
	if err != nil {
		os.Remove(tmpPath)
		return "", errors.Wrap(err, "failed to download file")
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		os.Remove(tmpPath)
		return "", errors.Errorf("download failed with status code: %d", resp.StatusCode)
	}

	// Get content length if available
	contentLength := resp.Header.Get("Content-Length")
	var totalBytes int64
	if contentLength != "" {
		fmt.Sscanf(contentLength, "%d", &totalBytes)
	}

	// Download with progress
	startTime := time.Now()
	var downloadedBytes int64
	buf := make([]byte, 8192)

	for {
		select {
		case <-ctx.Done():
			os.Remove(tmpPath)
			return "", errors.New("download cancelled")
		default:
		}

		n, err := resp.Body.Read(buf)
		if err != nil && err != io.EOF {
			os.Remove(tmpPath)
			return "", errors.Wrap(err, "failed to read download data")
		}
		if n == 0 {
			break
		}

		// Write to file
		if _, err := tmpFile.Write(buf[:n]); err != nil {
			os.Remove(tmpPath)
			return "", errors.Wrap(err, "failed to write to temporary file")
		}

		downloadedBytes += int64(n)

		// Log progress periodically
		if totalBytes > 0 && downloadedBytes%1048576 == 0 { // Every 1MB
			elapsed := time.Since(startTime).Seconds()
			speed := float64(downloadedBytes) / elapsed
			percentage := float64(downloadedBytes) / float64(totalBytes) * 100
			log.Infof("Download progress: %.1f%% (%d/%d bytes, %.1f KB/s)",
				percentage, downloadedBytes, totalBytes, speed/1024)
		}

		if err == io.EOF {
			break
		}
	}

	// Sync the file to disk
	if err := tmpFile.Sync(); err != nil {
		os.Remove(tmpPath)
		return "", errors.Wrap(err, "failed to sync temporary file")
	}

	// Rename to final destination
	if err := os.Rename(tmpPath, destPath); err != nil {
		os.Remove(tmpPath)
		return "", errors.Wrap(err, "failed to rename temporary file")
	}

	// Set executable permissions
	if err := os.Chmod(destPath, 0755); err != nil {
		log.Warnf("Failed to set executable permissions: %v", err)
	}

	log.Infof("Download completed: %d bytes", downloadedBytes)

	return destPath, nil
}

// VerifyChecksum verifies a file against a expected checksum
// The hashAlgorithm can be "sha256" or "sha512"
func VerifyChecksum(filePath, expectedChecksum, hashAlgorithm string) (bool, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return false, errors.Wrap(err, "failed to open file for checksum verification")
	}
	defer file.Close()

	var hashFunc hash.Hash
	switch strings.ToLower(hashAlgorithm) {
	case "sha256":
		hashFunc = sha256.New()
	case "sha512":
		hashFunc = sha512.New()
	default:
		return false, errors.Errorf("unsupported hash algorithm: %s", hashAlgorithm)
	}

	if _, err := io.Copy(hashFunc, file); err != nil {
		return false, errors.Wrap(err, "failed to read file for checksum")
	}

	actualChecksum := hex.EncodeToString(hashFunc.Sum(nil))

	// Compare checksums (case-insensitive)
	if strings.EqualFold(actualChecksum, expectedChecksum) {
		log.Infof("Checksum verification passed (%s: %s)", hashAlgorithm, actualChecksum)
		return true, nil
	}

	log.Warnf("Checksum verification failed!")
	log.Warnf("  Expected (%s): %s", hashAlgorithm, expectedChecksum)
	log.Warnf("  Actual (%s): %s", hashAlgorithm, actualChecksum)
	return false, errors.New("checksum mismatch")
}

// VerifyFileSize verifies that a downloaded file has the expected size
func VerifyFileSize(filePath string, expectedSize int64) (bool, error) {
	fileInfo, err := os.Stat(filePath)
	if err != nil {
		return false, errors.Wrap(err, "failed to get file info")
	}

	actualSize := fileInfo.Size()
	if actualSize != expectedSize {
		log.Warnf("File size verification failed!")
		log.Warnf("  Expected: %d bytes", expectedSize)
		log.Warnf("  Actual: %d bytes", actualSize)
		return false, errors.Errorf("file size mismatch: expected %d, got %d", expectedSize, actualSize)
	}

	log.Infof("File size verification passed: %d bytes", actualSize)
	return true, nil
}

// GetFileHash calculates the hash of a file
func GetFileHash(filePath, hashAlgorithm string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", errors.Wrap(err, "failed to open file for hashing")
	}
	defer file.Close()

	var hashFunc hash.Hash
	switch strings.ToLower(hashAlgorithm) {
	case "sha256":
		hashFunc = sha256.New()
	case "sha512":
		hashFunc = sha512.New()
	default:
		return "", errors.Errorf("unsupported hash algorithm: %s", hashAlgorithm)
	}

	if _, err := io.Copy(hashFunc, file); err != nil {
		return "", errors.Wrap(err, "failed to read file for hashing")
	}

	return hex.EncodeToString(hashFunc.Sum(nil)), nil
}

// FindBinaryInDirectory searches for an executable binary in a directory
func FindBinaryInDirectory(dir string) (string, error) {
	// Check common binary names
	binaryNames := []string{"HTND", "htnd"}

	for _, name := range binaryNames {
		binaryPath := filepath.Join(dir, name)
		if _, err := os.Stat(binaryPath); err == nil {
			// Check if it's a file and executable
			if fileInfo, err := os.Stat(binaryPath); err == nil {
				if fileInfo.Mode().IsRegular() && (fileInfo.Mode()&0111) != 0 {
					return binaryPath, nil
				}
			}
		}
	}

	// Recursively search up to 3 levels deep
	return findBinaryRecursive(dir, 3)
}

// findBinaryRecursive searches for a binary recursively
func findBinaryRecursive(dir string, maxDepth int) (string, error) {
	if maxDepth <= 0 {
		return "", errors.New("binary not found")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}

	for _, entry := range entries {
		fullPath := filepath.Join(dir, entry.Name())

		if entry.IsDir() {
			if result, err := findBinaryRecursive(fullPath, maxDepth-1); err == nil {
				return result, nil
			}
		} else if isExecutableFile(entry, fullPath) {
			return fullPath, nil
		}
	}

	return "", errors.New("binary not found")
}

// isExecutableFile checks if a file entry is an executable binary
func isExecutableFile(entry os.DirEntry, fullPath string) bool {
	if !entry.IsDir() {
		// Check file extension or name
		name := strings.ToLower(entry.Name())
		if strings.HasPrefix(name, "htnd") || strings.HasPrefix(name, "HTND") {
			// Check if it's executable
			if fileInfo, err := os.Stat(fullPath); err == nil {
				return fileInfo.Mode().IsRegular() && (fileInfo.Mode()&0111) != 0
			}
		}
	}
	return false
}
