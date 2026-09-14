// Copyright (c) 2013-2016 The btcsuite developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	httpprof "net/http/pprof"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"time"

	"github.com/HoosatNetwork/HTND/app"
	"github.com/HoosatNetwork/HTND/infrastructure/autoupdate"
	"github.com/HoosatNetwork/HTND/infrastructure/config"
	"github.com/HoosatNetwork/HTND/infrastructure/rlimit"
	"github.com/HoosatNetwork/HTND/version"
)

// defaultMemoryLimit is the soft memory limit htnd uses when GOMEMLIMIT is not set.
const defaultMemoryLimit = 8_000_000_000

// applyDefaultMemoryLimit sets defaultMemoryLimit unless GOMEMLIMIT is set. The Go runtime has already applied a set
// GOMEMLIMIT, in every form it accepts ("4GiB", "off", plain bytes); this used to re-parse it as a plain integer and
// replace anything else with the default, so GOMEMLIMIT=4GiB on a small machine ran with an 8 GB limit.
func applyDefaultMemoryLimit(lookupEnv func(string) (string, bool), setMemoryLimit func(int64) int64) {
	if _, isSet := lookupEnv("GOMEMLIMIT"); isSet {
		return
	}
	setMemoryLimit(defaultMemoryLimit)
}

func getEnvStr(key string, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

func init() {
	if v := getEnvStr("GOGC", "100"); v != "" {
		if pct, err := strconv.Atoi(v); err == nil {
			debug.SetGCPercent(pct)
		}
	}
	applyDefaultMemoryLimit(os.LookupEnv, debug.SetMemoryLimit)
	runtime.GOMAXPROCS(runtime.NumCPU())
}

// reportPanicToGitHub creates a GitHub issue for panics using shared GitHubClient
func reportPanicToGitHub(githubClient *autoupdate.GitHubClient, panicMsg any, stack []byte, autoReport bool) {
	if !autoReport {
		return
	}
	go func() {
		title := fmt.Sprintf("[PANIC] HTND v%s", version.Version())
		body := fmt.Sprintf(`**Node Crashed with Panic**

**Node Information:**
- Version: %s
- OS: %s
- Architecture: %s
- Timestamp: %s

**Panic Message:**
%v

**Stack Trace:**
%s
`,
			version.Version(), runtime.GOOS, runtime.GOARCH, time.Now().UTC().Format(time.RFC3339),
			panicMsg, string(stack))

		// Use shared GitHubClient to create issue
		_, err := githubClient.CreateIssue(context.Background(), title, body, []string{"bug", "panic", "auto-reported"})
		if err != nil {
			log.Printf("Failed to report panic to GitHub: %v", err)
		}
	}()
}

func main() {
	// Load configuration to access autoupdate settings
	cfg, err := config.LoadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	rlimit.RaiseFileLimit()

	// Get autoupdate config from loaded config
	autoUpdateCfg := autoupdate.DefaultConfig()
	autoUpdateCfg.AutoReportIssues = bool(cfg.AutoReportIssues)

	// Initialize GitHub client for error reporting
	githubClient := autoupdate.NewGitHubClient("HoosatNetwork", "HTND")
	if autoUpdateCfg.GitHubToken != "" {
		githubClient.SetToken(autoUpdateCfg.GitHubToken)
	}

	// Recover from panics and report to GitHub
	defer func() {
		if r := recover(); r != nil {
			stack := debug.Stack()
			reportPanicToGitHub(githubClient, r, stack, autoUpdateCfg.AutoReportIssues)
			// Re-panic to maintain normal crash behavior
			panic(r)
		}
	}()

	if os.Getenv("HTND_PROFILER") != "" {
		runtime.SetBlockProfileRate(1)
		runtime.SetMutexProfileFraction(1)
		go func() {
			mux := http.NewServeMux()
			mux.Handle("/", http.RedirectHandler("/debug/pprof", http.StatusSeeOther))
			mux.HandleFunc("/debug/pprof/", httpprof.Index)
			mux.HandleFunc("/debug/pprof/cmdline", httpprof.Cmdline)
			mux.HandleFunc("/debug/pprof/profile", httpprof.Profile)
			mux.HandleFunc("/debug/pprof/symbol", httpprof.Symbol)
			mux.HandleFunc("/debug/pprof/trace", httpprof.Trace)

			srv := &http.Server{
				Addr:         "127.0.0.1:6060",
				Handler:      mux,
				ReadTimeout:  5 * time.Second,
				WriteTimeout: 10 * time.Second,
				IdleTimeout:  120 * time.Second,
			}
			log.Println(srv.ListenAndServe())
		}()
	}

	if err := app.StartAppWithConfig(cfg); err != nil {
		os.Exit(1)
	}
}
