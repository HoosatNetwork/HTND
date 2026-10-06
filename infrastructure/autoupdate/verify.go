// Copyright (c) 2024 The HoosatNetwork developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package autoupdate

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/pkg/errors"
)

// pinnedReleasePublicKey is the hex-encoded ed25519 public key that release checksum files are
// signed with.
//
// It is deliberately EMPTY. This repository does not hold the project's release signing key, and a
// key invented here would be worse than none: it would make every check below appear to pass while
// verifying against something no release is actually signed with. Setting it is a release-captain
// task, done either by editing this constant or, for operators running their own builds, by passing
// --autoupdate-public-key.
//
// With no key configured, automatic installation refuses to run at all unless the operator
// explicitly accepts unverified archives - see ReleaseVerifier.VerifyArchive.
const pinnedReleasePublicKey = ""

// checksumAssetNames are the release asset names searched, in order and case-insensitively, for the
// list of per-asset SHA-256 checksums.
var checksumAssetNames = []string{
	"SHA256SUMS",
	"SHA256SUMS.txt",
	"sha256sums.txt",
	"checksums.txt",
	"CHECKSUMS.txt",
}

// signatureAssetSuffix is appended to the checksum asset's name to find its detached signature.
const signatureAssetSuffix = ".sig"

// assetFetcher downloads url to destPath and returns the path actually written. It matches
// Downloader.DownloadFile so the real downloader satisfies it directly, and so tests can supply a
// local fake without a network.
type assetFetcher func(ctx context.Context, url, destPath string) (string, error)

// ReleaseVerifier decides whether a downloaded release archive may be installed.
//
// The rule it enforces is HTN-162's: no unsigned archive installs. An archive is installable only
// when its SHA-256 appears in a checksum list that carries a valid ed25519 signature from the
// pinned key. Everything else - no key configured, no checksum asset, no signature asset, a bad
// signature, a checksum that does not match, an asset name absent from the list - is a refusal.
//
// The single deliberate escape hatch is allowUnverified, which an operator must set explicitly. It
// exists because this repository ships no key (see pinnedReleasePublicKey), so without it the
// feature would be unusable for anyone running their own builds rather than the project's releases.
type ReleaseVerifier struct {
	publicKey       ed25519.PublicKey
	allowUnverified bool
}

// NewReleaseVerifier builds a verifier from a hex-encoded ed25519 public key. An empty publicKeyHex
// falls back to pinnedReleasePublicKey, which is itself empty unless a release captain has set it.
func NewReleaseVerifier(publicKeyHex string, allowUnverified bool) (*ReleaseVerifier, error) {
	if publicKeyHex == "" {
		publicKeyHex = pinnedReleasePublicKey
	}
	verifier := &ReleaseVerifier{allowUnverified: allowUnverified}
	if publicKeyHex == "" {
		return verifier, nil
	}

	keyBytes, err := hex.DecodeString(strings.TrimSpace(publicKeyHex))
	if err != nil {
		return nil, errors.Wrap(err, "release public key is not valid hex")
	}
	if len(keyBytes) != ed25519.PublicKeySize {
		return nil, errors.Errorf("release public key must be %d bytes, got %d",
			ed25519.PublicKeySize, len(keyBytes))
	}
	verifier.publicKey = ed25519.PublicKey(keyBytes)
	return verifier, nil
}

// HasPinnedKey reports whether a key is configured at all.
func (v *ReleaseVerifier) HasPinnedKey() bool {
	return v.publicKey != nil
}

// VerifyArchive returns nil only if archivePath may be installed.
//
// workDir is where the checksum and signature assets are downloaded to; the caller owns it.
func (v *ReleaseVerifier) VerifyArchive(ctx context.Context, fetch assetFetcher,
	release *GitHubRelease, assetName, archivePath, workDir string) error {

	if v.publicKey == nil {
		if !v.allowUnverified {
			return errors.Errorf("refusing to install %s: no release signing key is configured, so "+
				"the archive's authenticity cannot be established. Set --autoupdate-public-key to the "+
				"project's release key, or pass --autoupdate-allow-unverified to install unverified "+
				"archives anyway (not recommended: an attacker who can serve you a release asset can "+
				"then run code as this node's user)", assetName)
		}
		log.Warnf("Installing %s WITHOUT verifying it: no release signing key is configured and "+
			"--autoupdate-allow-unverified is set. The archive's authenticity has not been checked.",
			assetName)
		return nil
	}

	checksumAssetName, checksumURL, err := findChecksumAsset(release)
	if err != nil {
		return err
	}
	signatureURL, err := findAssetURL(release, checksumAssetName+signatureAssetSuffix)
	if err != nil {
		return errors.Wrapf(err, "release %s has %s but no detached signature for it, so the "+
			"checksum list itself cannot be trusted", release.TagName, checksumAssetName)
	}

	checksumPath, err := fetch(ctx, checksumURL, filepath.Join(workDir, checksumAssetName))
	if err != nil {
		return errors.Wrapf(err, "failed to download the checksum list %s", checksumAssetName)
	}
	signaturePath, err := fetch(ctx, signatureURL, filepath.Join(workDir, checksumAssetName+signatureAssetSuffix))
	if err != nil {
		return errors.Wrapf(err, "failed to download the signature for %s", checksumAssetName)
	}

	checksumBytes, err := os.ReadFile(checksumPath)
	if err != nil {
		return errors.Wrapf(err, "failed to read the downloaded checksum list %s", checksumPath)
	}
	signature, err := readSignature(signaturePath)
	if err != nil {
		return err
	}

	// The signature covers the checksum list verbatim, so this is what makes the list authoritative.
	// Every later comparison is only as good as this one check.
	if !ed25519.Verify(v.publicKey, checksumBytes, signature) {
		return errors.Errorf("the signature on %s is not valid for the configured release key - "+
			"refusing to install %s", checksumAssetName, assetName)
	}

	expectedChecksum, err := checksumForAsset(checksumBytes, assetName)
	if err != nil {
		return err
	}
	actualChecksum, err := sha256OfFile(archivePath)
	if err != nil {
		return err
	}
	if !strings.EqualFold(actualChecksum, expectedChecksum) {
		return errors.Errorf("checksum mismatch for %s: the signed list says %s, the downloaded "+
			"file hashes to %s - refusing to install", assetName, expectedChecksum, actualChecksum)
	}

	log.Infof("Verified %s against a signed checksum list (%s), sha256 %s",
		assetName, checksumAssetName, actualChecksum)
	return nil
}

// findChecksumAsset returns the name and URL of the release's checksum list.
func findChecksumAsset(release *GitHubRelease) (string, string, error) {
	for _, candidate := range checksumAssetNames {
		for _, asset := range release.Assets {
			if strings.EqualFold(asset.Name, candidate) {
				return asset.Name, asset.DownloadURL, nil
			}
		}
	}
	return "", "", errors.Errorf("release %s publishes no checksum list (looked for %s), so no "+
		"archive from it can be verified", release.TagName, strings.Join(checksumAssetNames, ", "))
}

// findAssetURL returns the download URL of the named asset.
func findAssetURL(release *GitHubRelease, name string) (string, error) {
	for _, asset := range release.Assets {
		if strings.EqualFold(asset.Name, name) {
			return asset.DownloadURL, nil
		}
	}
	return "", errors.Errorf("release %s has no asset named %s", release.TagName, name)
}

// readSignature reads a detached ed25519 signature, accepting either raw bytes or hex.
func readSignature(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to read the downloaded signature %s", path)
	}
	if len(raw) == ed25519.SignatureSize {
		return raw, nil
	}
	decoded, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		return nil, errors.Wrapf(err, "signature %s is neither %d raw bytes nor hex",
			path, ed25519.SignatureSize)
	}
	if len(decoded) != ed25519.SignatureSize {
		return nil, errors.Errorf("signature %s decodes to %d bytes, want %d",
			path, len(decoded), ed25519.SignatureSize)
	}
	return decoded, nil
}

// checksumForAsset finds assetName's SHA-256 in a sha256sum-style list ("<hex>  <name>" per line).
//
// The name is compared on its base only. Some release tooling writes "./HTND-v1-linux-amd64.zip"
// or a path prefix, and treating that as a different file would reject a correctly signed release.
func checksumForAsset(checksumList []byte, assetName string) (string, error) {
	wanted := strings.ToLower(filepath.Base(assetName))
	scanner := bufio.NewScanner(strings.NewReader(string(checksumList)))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		// The second field may carry sha256sum's binary-mode "*" marker.
		name := strings.ToLower(filepath.Base(strings.TrimPrefix(fields[len(fields)-1], "*")))
		if name == wanted {
			return fields[0], nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", errors.Wrap(err, "failed to read the checksum list")
	}
	return "", errors.Errorf("the signed checksum list does not mention %s, so this archive is not "+
		"one of the files that was signed", assetName)
}

// sha256OfFile returns the hex-encoded SHA-256 of a file's contents.
func sha256OfFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", errors.Wrapf(err, "failed to open %s for hashing", path)
	}
	defer file.Close()

	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", errors.Wrapf(err, "failed to read %s for hashing", path)
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}
