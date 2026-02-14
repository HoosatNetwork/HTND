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
	"time"

	"github.com/Hoosat-Oy/HTND/app"
)

func periodicAggressiveRelease() error {
	minutes := 90
	htnd_gc_timer_argument := os.Getenv("HTND_GC_TIMER")
	if htnd_gc_timer_argument != "" {
		var err error
		minutes, err = strconv.Atoi(htnd_gc_timer_argument)
		if err != nil {
			return err
		}
	}

	ticker := time.NewTicker(time.Duration(minutes) * time.Minute)
	for range ticker.C {
		debug.FreeOSMemory()
	}
	return nil
}

func getEnvInt(key string, defaultVal int) int64 {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return int64(n)
		}
	}
	return int64(defaultVal)
}

func main() {
	var gogc string = "500"
	if os.Getenv("GOGC") != "" {
		gogc = os.Getenv("GOGC")
	}
	os.Setenv("GOGC", gogc)
	debug.SetMemoryLimit(getEnvInt("GOMEMLIMIT", 8_000_000_000))
	runtime.GOMAXPROCS(runtime.NumCPU()) // Set the maximum number of CPUs that can be executing simultaneously
	if os.Getenv("HTND_PROFILER") != "" {
		runtime.SetBlockProfileRate(1)     // Set block profile rate to 1 to enable block profiling
		runtime.SetMutexProfileFraction(1) // Set mutex profile fraction to 1 to enable mutex profiling
		go func() {
			log.Println(http.ListenAndServe("127.0.0.1:6060", nil))
		}()
	}

	go periodicAggressiveRelease()
	if err := app.StartApp(); err != nil {
		os.Exit(1)
	}
}
