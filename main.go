// Copyright (c) 2013-2016 The btcsuite developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package main

import (
	"log"
	"net/http"
	_ "net/http/pprof"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"

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
	os.Setenv("GOGC", getEnvStr("GOGC", "off"))
	debug.SetMemoryLimit(getEnvInt("GOMEMLIMIT", 30_000_000_000))
	runtime.GOMAXPROCS(runtime.NumCPU())
}

func main() {
	if os.Getenv("HTND_PROFILER") != "" {
		runtime.SetBlockProfileRate(1)     // Set block profile rate to 1 to enable block profiling
		runtime.SetMutexProfileFraction(1) // Set mutex profile fraction to 1 to enable mutex profiling
		go func() {
			log.Println(http.ListenAndServe("127.0.0.1:6060", nil))
		}()
	}

	if err := app.StartApp(); err != nil {
		os.Exit(1)
	}
}
