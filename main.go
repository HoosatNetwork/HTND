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
	"time"

	"github.com/Hoosat-Oy/HTND/app"
)

func periodicAggressiveRelease() {
	ticker := time.NewTicker(60 * time.Second)
	for range ticker.C {
		debug.FreeOSMemory()
	}
}

func main() {
	debug.SetMemoryLimit(4_000_000_000)  // Set memory soft limit to 16GB
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
