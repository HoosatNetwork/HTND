// Copyright (c) 2013-2016 The btcsuite developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package main

import (
	"log"
	"net/http"
	httppprof "net/http/pprof"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"time"

	"github.com/Hoosat-Oy/HTND/app"
)

func getEnvInt(key string, defaultVal int) int64 {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return int64(n)
		}
	}
	return int64(defaultVal)
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
	debug.SetMemoryLimit(getEnvInt("GOMEMLIMIT", 8_000_000_000))
	runtime.GOMAXPROCS(runtime.NumCPU())
}

func main() {
	if os.Getenv("HTND_PROFILER") != "" {
		runtime.SetBlockProfileRate(1)     // Set block profile rate to 1 to enable block profiling
		runtime.SetMutexProfileFraction(1) // Set mutex profile fraction to 1 to enable mutex profiling
		go func() {
			mux := http.NewServeMux()
			mux.Handle("/", http.RedirectHandler("/debug/pprof", http.StatusSeeOther))
			mux.HandleFunc("/debug/pprof/", httppprof.Index)
			mux.HandleFunc("/debug/pprof/cmdline", httppprof.Cmdline)
			mux.HandleFunc("/debug/pprof/profile", httppprof.Profile)
			mux.HandleFunc("/debug/pprof/symbol", httppprof.Symbol)
			mux.HandleFunc("/debug/pprof/trace", httppprof.Trace)

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

	if err := app.StartApp(); err != nil {
		os.Exit(1)
	}
}
