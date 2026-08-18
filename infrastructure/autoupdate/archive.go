// Copyright (c) 2024 The HoosatNetwork developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package autoupdate

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/pkg/errors"
)

// extractTarGz extracts a .tar.gz archive
func extractTarGz(archivePath, destDir string) (string, error) {
	log.Infof("Extracting tar.gz archive: %s", archivePath)

	file, err := os.Open(archivePath)
	if err != nil {
		return "", errors.Wrap(err, "failed to open tar.gz file")
	}
	defer file.Close()

	// First decompress gzip
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return "", errors.Wrap(err, "failed to create gzip reader")
	}
	defer gzipReader.Close()

	// Then untar
	tarReader := tar.NewReader(gzipReader)
	return extractTarReader(tarReader, destDir)
}

// extractZip extracts a .zip archive
func extractZip(archivePath, destDir string) (string, error) {
	log.Infof("Extracting zip archive: %s", archivePath)

	// Open the zip file
	r, err := zip.OpenReader(archivePath)
	if err != nil {
		return "", errors.Wrap(err, "failed to open zip file")
	}
	defer r.Close()

	var firstFilePath string

	// Iterate through the files in the archive
	for _, f := range r.File {
		if firstFilePath == "" {
			firstFilePath = f.Name
		}

		// Create the full path for the file
		fullPath := filepath.Join(destDir, f.Name)

		// Create parent directory if needed
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(fullPath, 0755); err != nil {
				return "", errors.Wrapf(err, "failed to create directory: %s", fullPath)
			}
			continue
		}

		// Create parent directory
		parentDir := filepath.Dir(fullPath)
		if err := os.MkdirAll(parentDir, 0755); err != nil {
			return "", errors.Wrapf(err, "failed to create parent directory: %s", parentDir)
		}

		// Open the file in the archive
		rc, err := f.Open()
		if err != nil {
			return "", errors.Wrapf(err, "failed to open file in archive: %s", f.Name)
		}

		// Create the destination file
		outFile, err := os.OpenFile(fullPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
		if err != nil {
			rc.Close()
			return "", errors.Wrapf(err, "failed to create file: %s", fullPath)
		}

		// Copy the file contents
		_, err = io.Copy(outFile, rc)
		outFile.Close()
		rc.Close()

		if err != nil {
			return "", errors.Wrapf(err, "failed to copy file: %s", fullPath)
		}

		// Set permissions
		if err := os.Chmod(fullPath, f.Mode()); err != nil {
			log.Warnf("Failed to set permissions for %s: %v", fullPath, err)
		}
	}

	if firstFilePath != "" {
		return filepath.Join(destDir, firstFilePath), nil
	}

	return destDir, nil
}

// extractTarReader extracts files from a tar reader
func extractTarReader(tr *tar.Reader, destDir string) (string, error) {
	var firstFilePath string

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", errors.Wrap(err, "failed to read tar header")
		}

		if firstFilePath == "" {
			firstFilePath = header.Name
		}

		// Create the full path for the file
		target := filepath.Join(destDir, header.Name)

		// Check for directory
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				return "", errors.Wrapf(err, "failed to create directory: %s", target)
			}
			continue

		case tar.TypeReg:
			// Create parent directory
			parentDir := filepath.Dir(target)
			if err := os.MkdirAll(parentDir, 0755); err != nil {
				return "", errors.Wrapf(err, "failed to create parent directory: %s", parentDir)
			}

			// Create the file
			file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, os.FileMode(header.Mode))
			if err != nil {
				return "", errors.Wrapf(err, "failed to create file: %s", target)
			}

			// Copy the file contents
			if _, err := io.Copy(file, tr); err != nil {
				file.Close()
				return "", errors.Wrapf(err, "failed to copy file: %s", target)
			}

			file.Close()

			// Set permissions
			if err := os.Chmod(target, os.FileMode(header.Mode)); err != nil {
				log.Warnf("Failed to set permissions for %s: %v", target, err)
			}

		case tar.TypeSymlink:
			// Handle symbolic links
			linkTarget := filepath.Join(destDir, header.Linkname)
			if err := os.Symlink(linkTarget, target); err != nil {
				log.Warnf("Failed to create symlink %s -> %s: %v", target, linkTarget, err)
			}

		default:
			log.Warnf("Unhandled tar type: %c for %s", header.Typeflag, header.Name)
		}
	}

	if firstFilePath != "" {
		// Clean up the path (remove leading ./ if present)
		cleanedPath := strings.TrimPrefix(firstFilePath, "./")
		return filepath.Join(destDir, cleanedPath), nil
	}

	return destDir, nil
}

// ExtractArchive extracts a tar.gz or zip archive to a destination directory
func ExtractArchive(archivePath, destDir string) (string, error) {
	log.Infof("Extracting archive: %s to %s", archivePath, destDir)

	// Create destination directory if it doesn't exist
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return "", errors.Wrap(err, "failed to create extraction directory")
	}

	// Try to detect archive type by extension
	lowerPath := strings.ToLower(archivePath)

	if strings.HasSuffix(lowerPath, ".tar.gz") || strings.HasSuffix(lowerPath, ".tgz") {
		return extractTarGz(archivePath, destDir)
	}

	if strings.HasSuffix(lowerPath, ".zip") {
		return extractZip(archivePath, destDir)
	}

	return "", errors.Errorf("unsupported archive format: %s", archivePath)
}

// CleanupExtractedFiles removes the extracted files
func CleanupExtractedFiles(destDir string) error {
	log.Infof("Cleaning up extracted files in: %s", destDir)
	return os.RemoveAll(destDir)
}
