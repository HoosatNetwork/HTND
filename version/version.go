package version

import (
	"fmt"
	"runtime/debug"
	"strings"
)

// validCharacters  is a list of characters valid in the appBuild string
const validCharacters = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz-"

const (
	appMajor uint = 2
	appMinor uint = 15
	appPatch uint = 0
)

// shortCommitLength is the number of hex digits kept from the full commit hash
// recorded in the binary, matching the width 'git rev-parse --short' currently
// produces for this repository.
const shortCommitLength = 9

// appBuild is defined as a variable so it can be overridden during the build
// process with '-ldflags "-X github.com/HoosatNetwork/HTND/version.appBuild=foo"' if needed.
// It MUST only contain characters from validCharacters.
//
// When it is left empty the build metadata falls back to the commit hash the Go
// toolchain stamps into the binary at build time. The commit must be resolved at
// build time: a running node generally has neither the source repository nor a
// git binary available, so it cannot be looked up on startup.
var appBuild string

var version = "" // string used for memoization of version

func init() {
	if appBuild == "" {
		appBuild = buildCommit()
	}

	if version == "" {
		// Start with the major, minor, and patch versions.
		version = fmt.Sprintf("%d.%d.%d", appMajor, appMinor, appPatch)

		// Append build metadata if there is any.
		// Panic if any invalid characters are encountered
		if appBuild != "" && appBuild != "Testnet" {
			checkAppBuild(appBuild)

			version = fmt.Sprintf("%s-%s", version, appBuild)
		}
	}
}

// buildCommit returns the short commit hash the toolchain recorded in the binary
// when it was built, suffixed with "-dirty" if the working tree held uncommitted
// changes at that point. It returns an empty string when the binary was built
// without VCS stamping, for example with -buildvcs=false or from a source tree
// that is not a git checkout.
func buildCommit() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}

	revision := ""
	modified := false
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			modified = setting.Value == "true"
		}
	}

	if revision == "" {
		return ""
	}

	if len(revision) > shortCommitLength {
		revision = revision[:shortCommitLength]
	}

	if modified {
		return revision + "-dirty"
	}

	return revision
}

// Version returns the application version as a properly formed string
func Version() string {
	return version
}

// checkAppBuild verifies that appBuild does not contain any characters outside of validCharacters.
// In case of any invalid characters checkAppBuild panics
func checkAppBuild(appBuild string) {
	for _, r := range appBuild {
		if !strings.ContainsRune(validCharacters, r) {
			panic(fmt.Errorf("appBuild string (%s) contains forbidden characters. Only alphanumeric characters and dashes are allowed", appBuild))
		}
	}
}
