// Copyright (c) 2024 The HoosatNetwork developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package autoupdate

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// HTN-162: the auto-updater used to install whatever it downloaded. VerifyChecksum and
// VerifyFileSize existed in downloader.go but had no callers anywhere in the tree, so an attacker
// who could serve a release asset - or anyone who could MITM the download, or a compromised release
// pipeline - got code execution as the node's user on every node with auto-install enabled.
//
// These tests pin the rule that replaced that: an archive is installable only when its SHA-256
// appears in a checksum list carrying a valid ed25519 signature from the configured key.

// releaseFixture is a fake release plus a local "server": a directory of files, keyed by the URL
// the release advertises for them.
type releaseFixture struct {
	release *GitHubRelease
	files   map[string][]byte
	dir     string
}

func newReleaseFixture(t *testing.T) *releaseFixture {
	t.Helper()
	return &releaseFixture{
		release: &GitHubRelease{TagName: "v9.9.9"},
		files:   map[string][]byte{},
		dir:     t.TempDir(),
	}
}

// addAsset registers an asset under a URL derived from its name.
func (f *releaseFixture) addAsset(name string, contents []byte) {
	url := "https://example.invalid/" + name
	f.release.Assets = append(f.release.Assets, struct {
		Name        string `json:"name"`
		DownloadURL string `json:"browser_download_url"`
		Size        int64  `json:"size"`
	}{Name: name, DownloadURL: url, Size: int64(len(contents))})
	f.files[url] = contents
}

// fetch is an assetFetcher backed by the fixture's in-memory files.
func (f *releaseFixture) fetch(_ context.Context, url, destPath string) (string, error) {
	contents, ok := f.files[url]
	if !ok {
		return "", fmt.Errorf("no such asset: %s", url)
	}
	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return "", err
	}
	if err := os.WriteFile(destPath, contents, 0644); err != nil {
		return "", err
	}
	return destPath, nil
}

// writeArchive writes an archive file on disk and returns its path.
func (f *releaseFixture) writeArchive(t *testing.T, name string, contents []byte) string {
	t.Helper()
	path := filepath.Join(f.dir, name)
	if err := os.WriteFile(path, contents, 0644); err != nil {
		t.Fatalf("writing archive: %+v", err)
	}
	return path
}

func sha256Hex(contents []byte) string {
	sum := sha256.Sum256(contents)
	return hex.EncodeToString(sum[:])
}

// signedRelease builds a release with an archive, a checksum list covering it, and a detached
// signature over that list produced by a freshly generated key.
func signedRelease(t *testing.T) (fixture *releaseFixture, publicKeyHex, archiveName, archivePath string,
	privateKey ed25519.PrivateKey, checksumList []byte) {
	t.Helper()

	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating key: %+v", err)
	}

	fixture = newReleaseFixture(t)
	archiveName = "HTND-v9.9.9-linux-amd64.tar.gz"
	archiveContents := []byte("not really a tarball, but it hashes like a file")
	archivePath = fixture.writeArchive(t, archiveName, archiveContents)

	checksumList = []byte(fmt.Sprintf("%s  %s\n%s  %s\n",
		sha256Hex(archiveContents), archiveName,
		sha256Hex([]byte("some other asset")), "HTND-v9.9.9-windows-amd64.zip"))

	fixture.addAsset(archiveName, archiveContents)
	fixture.addAsset("SHA256SUMS", checksumList)
	fixture.addAsset("SHA256SUMS.sig", ed25519.Sign(privateKey, checksumList))

	return fixture, hex.EncodeToString(publicKey), archiveName, archivePath, privateKey, checksumList
}

func TestVerifyArchiveAcceptsACorrectlySignedRelease(t *testing.T) {
	fixture, publicKeyHex, archiveName, archivePath, _, _ := signedRelease(t)

	verifier, err := NewReleaseVerifier(publicKeyHex, false)
	if err != nil {
		t.Fatalf("NewReleaseVerifier: %+v", err)
	}
	if !verifier.HasPinnedKey() {
		t.Fatal("HasPinnedKey is false although a key was supplied")
	}

	err = verifier.VerifyArchive(context.Background(), fixture.fetch, fixture.release,
		archiveName, archivePath, t.TempDir())
	if err != nil {
		t.Fatalf("a correctly signed release was rejected: %+v", err)
	}
}

// TestVerifyArchiveRefusesWithoutAPinnedKey is the default posture of a stock build: this
// repository ships no signing key, so auto-install must refuse rather than install blindly.
func TestVerifyArchiveRefusesWithoutAPinnedKey(t *testing.T) {
	fixture, _, archiveName, archivePath, _, _ := signedRelease(t)

	verifier, err := NewReleaseVerifier("", false)
	if err != nil {
		t.Fatalf("NewReleaseVerifier: %+v", err)
	}
	if verifier.HasPinnedKey() {
		t.Fatal("HasPinnedKey is true although this build pins no key - pinnedReleasePublicKey " +
			"must stay empty until a real release key exists")
	}

	err = verifier.VerifyArchive(context.Background(), fixture.fetch, fixture.release,
		archiveName, archivePath, t.TempDir())
	if err == nil {
		t.Fatal("an unverifiable archive was accepted with no signing key configured")
	}
	if !strings.Contains(err.Error(), "no release signing key is configured") {
		t.Fatalf("refusal did not explain the missing key: %v", err)
	}
}

// TestVerifyArchiveAllowsUnverifiedOnlyWhenExplicitlyAsked pins the one escape hatch. It has to
// exist because no key ships here, but it must never be the default.
func TestVerifyArchiveAllowsUnverifiedOnlyWhenExplicitlyAsked(t *testing.T) {
	fixture, _, archiveName, archivePath, _, _ := signedRelease(t)

	verifier, err := NewReleaseVerifier("", true)
	if err != nil {
		t.Fatalf("NewReleaseVerifier: %+v", err)
	}
	err = verifier.VerifyArchive(context.Background(), fixture.fetch, fixture.release,
		archiveName, archivePath, t.TempDir())
	if err != nil {
		t.Fatalf("explicit allowUnverified still refused: %+v", err)
	}
}

func TestVerifyArchiveRejectsATamperedArchive(t *testing.T) {
	fixture, publicKeyHex, archiveName, _, _, _ := signedRelease(t)

	// The attacker swaps the archive's bytes but cannot re-sign the checksum list.
	tamperedPath := fixture.writeArchive(t, archiveName, []byte("malicious payload"))

	verifier, err := NewReleaseVerifier(publicKeyHex, false)
	if err != nil {
		t.Fatalf("NewReleaseVerifier: %+v", err)
	}
	err = verifier.VerifyArchive(context.Background(), fixture.fetch, fixture.release,
		archiveName, tamperedPath, t.TempDir())
	if err == nil {
		t.Fatal("a tampered archive was accepted")
	}
	if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("expected a checksum mismatch, got: %v", err)
	}
}

// TestVerifyArchiveRejectsAChecksumListSignedByAnotherKey is the attack the signature exists to
// stop: the attacker rewrites the checksum list to match their archive and signs it with a key
// they control.
func TestVerifyArchiveRejectsAChecksumListSignedByAnotherKey(t *testing.T) {
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating the honest key: %+v", err)
	}
	attackerPublic, attackerPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating the attacker key: %+v", err)
	}
	if string(publicKey) == string(attackerPublic) {
		t.Fatal("the two generated keys collided")
	}

	fixture := newReleaseFixture(t)
	archiveName := "HTND-v9.9.9-linux-amd64.tar.gz"
	payload := []byte("malicious payload")
	archivePath := fixture.writeArchive(t, archiveName, payload)

	// Internally consistent: the list really does describe the malicious archive.
	checksumList := []byte(fmt.Sprintf("%s  %s\n", sha256Hex(payload), archiveName))
	fixture.addAsset(archiveName, payload)
	fixture.addAsset("SHA256SUMS", checksumList)
	fixture.addAsset("SHA256SUMS.sig", ed25519.Sign(attackerPrivate, checksumList))

	verifier, err := NewReleaseVerifier(hex.EncodeToString(publicKey), false)
	if err != nil {
		t.Fatalf("NewReleaseVerifier: %+v", err)
	}
	err = verifier.VerifyArchive(context.Background(), fixture.fetch, fixture.release,
		archiveName, archivePath, t.TempDir())
	if err == nil {
		t.Fatal("a checksum list signed by the wrong key was accepted - the signature check is " +
			"not actually gating anything")
	}
	if !strings.Contains(err.Error(), "signature on SHA256SUMS is not valid") {
		t.Fatalf("expected a signature failure, got: %v", err)
	}
}

func TestVerifyArchiveRejectsAReleaseWithNoChecksumList(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating key: %+v", err)
	}
	publicKey := privateKey.Public().(ed25519.PublicKey)

	fixture := newReleaseFixture(t)
	archiveName := "HTND-v9.9.9-linux-amd64.tar.gz"
	archivePath := fixture.writeArchive(t, archiveName, []byte("contents"))
	fixture.addAsset(archiveName, []byte("contents"))

	verifier, err := NewReleaseVerifier(hex.EncodeToString(publicKey), false)
	if err != nil {
		t.Fatalf("NewReleaseVerifier: %+v", err)
	}
	err = verifier.VerifyArchive(context.Background(), fixture.fetch, fixture.release,
		archiveName, archivePath, t.TempDir())
	if err == nil {
		t.Fatal("a release with no checksum list was accepted")
	}
	if !strings.Contains(err.Error(), "publishes no checksum list") {
		t.Fatalf("expected a missing-checksum-list error, got: %v", err)
	}
}

// TestVerifyArchiveRejectsASignedListThatDoesNotCoverThisArchive covers the gap between "the list
// is authentic" and "the list says anything about this file": a real signed list from a real
// release, plus an extra archive that was never signed.
func TestVerifyArchiveRejectsASignedListThatDoesNotCoverThisArchive(t *testing.T) {
	fixture, publicKeyHex, _, _, _, _ := signedRelease(t)

	unsignedName := "HTND-v9.9.9-linux-arm64.tar.gz"
	unsignedPath := fixture.writeArchive(t, unsignedName, []byte("never signed"))
	fixture.addAsset(unsignedName, []byte("never signed"))

	verifier, err := NewReleaseVerifier(publicKeyHex, false)
	if err != nil {
		t.Fatalf("NewReleaseVerifier: %+v", err)
	}
	err = verifier.VerifyArchive(context.Background(), fixture.fetch, fixture.release,
		unsignedName, unsignedPath, t.TempDir())
	if err == nil {
		t.Fatal("an archive absent from the signed list was accepted")
	}
	if !strings.Contains(err.Error(), "does not mention") {
		t.Fatalf("expected a not-in-list error, got: %v", err)
	}
}

func TestVerifyArchiveRejectsAReleaseWithNoSignature(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating key: %+v", err)
	}

	fixture := newReleaseFixture(t)
	archiveName := "HTND-v9.9.9-linux-amd64.tar.gz"
	contents := []byte("contents")
	archivePath := fixture.writeArchive(t, archiveName, contents)
	checksumList := []byte(fmt.Sprintf("%s  %s\n", sha256Hex(contents), archiveName))

	fixture.addAsset(archiveName, contents)
	fixture.addAsset("SHA256SUMS", checksumList)
	// Deliberately no .sig asset. A correct checksum with no signature proves only that the file
	// downloaded intact from whoever served it.
	_ = privateKey

	verifier, err := NewReleaseVerifier(hex.EncodeToString(publicKey), false)
	if err != nil {
		t.Fatalf("NewReleaseVerifier: %+v", err)
	}
	err = verifier.VerifyArchive(context.Background(), fixture.fetch, fixture.release,
		archiveName, archivePath, t.TempDir())
	if err == nil {
		t.Fatal("a release with an unsigned checksum list was accepted")
	}
	if !strings.Contains(err.Error(), "no detached signature") {
		t.Fatalf("expected a missing-signature error, got: %v", err)
	}
}

// TestNewReleaseVerifierRejectsAMalformedKey stops a typo in --autoupdate-public-key from
// silently degrading into "no key configured", which would then refuse every install with a
// misleading message.
func TestNewReleaseVerifierRejectsAMalformedKey(t *testing.T) {
	for _, testCase := range []struct{ name, key string }{
		{"not hex", "zzzz"},
		{"too short", "abcd"},
		{"too long", strings.Repeat("ab", ed25519.PublicKeySize+1)},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := NewReleaseVerifier(testCase.key, false); err == nil {
				t.Fatalf("NewReleaseVerifier accepted a malformed key %q", testCase.key)
			}
		})
	}
}

// TestChecksumForAssetToleratesRealisticListFormats pins the parsing leniency that keeps a
// correctly signed release from being rejected over cosmetics. The signature has already been
// verified by the time this runs, so the list's bytes are trusted here.
func TestChecksumForAssetToleratesRealisticListFormats(t *testing.T) {
	wantChecksum := sha256Hex([]byte("x"))
	for _, testCase := range []struct{ name, line string }{
		{"two spaces", wantChecksum + "  HTND-v1-linux-amd64.zip"},
		{"binary marker", wantChecksum + " *HTND-v1-linux-amd64.zip"},
		{"path prefix", wantChecksum + "  ./dist/HTND-v1-linux-amd64.zip"},
		{"single space", wantChecksum + " HTND-v1-linux-amd64.zip"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := checksumForAsset([]byte(testCase.line+"\n"), "HTND-v1-linux-amd64.zip")
			if err != nil {
				t.Fatalf("checksumForAsset: %+v", err)
			}
			if got != wantChecksum {
				t.Fatalf("got %s, want %s", got, wantChecksum)
			}
		})
	}
}

// TestReadSignatureAcceptsRawAndHex pins that a signature file may be either 64 raw bytes or the
// hex text some signing tools emit.
func TestReadSignatureAcceptsRawAndHex(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating key: %+v", err)
	}
	signature := ed25519.Sign(privateKey, []byte("payload"))
	dir := t.TempDir()

	rawPath := filepath.Join(dir, "raw.sig")
	if err := os.WriteFile(rawPath, signature, 0644); err != nil {
		t.Fatalf("writing raw signature: %+v", err)
	}
	hexPath := filepath.Join(dir, "hex.sig")
	if err := os.WriteFile(hexPath, []byte(hex.EncodeToString(signature)+"\n"), 0644); err != nil {
		t.Fatalf("writing hex signature: %+v", err)
	}

	for name, path := range map[string]string{"raw": rawPath, "hex": hexPath} {
		t.Run(name, func(t *testing.T) {
			got, err := readSignature(path)
			if err != nil {
				t.Fatalf("readSignature: %+v", err)
			}
			if string(got) != string(signature) {
				t.Fatal("readSignature returned different bytes than were written")
			}
		})
	}
}
