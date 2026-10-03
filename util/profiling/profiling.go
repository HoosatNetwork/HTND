package profiling

import (
	"fmt"
	"net"
	"net/http"
	httppprof "net/http/pprof"
	"os"
	"path/filepath"
	"runtime"
	runtimepprof "runtime/pprof"
	"strconv"
	"strings"
	"time"

	"github.com/HoosatNetwork/HTND/v2/infrastructure/logger"
	"github.com/pkg/errors"

	"github.com/HoosatNetwork/HTND/v2/util/panics"
)

// heapDumpFileName is the name of the heap dump file. We want every run to have its own
// file, so we append the timestamp of the program launch time to the file name (note the
// custom format for compliance with file name rules on all OSes).
var heapDumpFileName = fmt.Sprintf("heap-%s.pprof", time.Now().Format("01-02-2006T15.04.05"))

// ListenAddress returns the address the profiling server listens on for a --profile value. A bare
// port listens on every interface, as --profile always has. host:port listens on exactly that
// address, which keeps pprof off the network (127.0.0.1:6061) and lets several nodes on one machine
// each run a profiler on their own port.
func ListenAddress(profile string) (string, error) {
	host, port := "", profile
	if strings.Contains(profile, ":") {
		var err error
		host, port, err = net.SplitHostPort(profile)
		if err != nil {
			return "", errors.Errorf("the profile address %q is neither a port nor host:port: %s", profile, err)
		}
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1024 || portNumber > 65535 {
		return "", errors.Errorf("the profile port %q must be between 1024 and 65535", port)
	}
	return net.JoinHostPort(host, port), nil
}

// Start starts the profiling server on profile, a port or host:port - see ListenAddress.
// WARNING: The pprof endpoint is exposed on /debug/pprof. Do not use in production environments without proper access controls. (gosec G108)
func Start(profile string, log *logger.Logger) {
	listenAddr, err := ListenAddress(profile)
	if err != nil {
		log.Errorf("Not starting the profile server: %s", err)
		return
	}
	spawn := panics.GoroutineWrapperFunc(log)
	spawn("profiling.Start", func() {
		log.Infof("Profile server listening on %s", listenAddr)
		mux := http.NewServeMux()
		profileRedirect := http.RedirectHandler("/debug/pprof", http.StatusSeeOther)
		mux.Handle("/", profileRedirect)
		mux.HandleFunc("/debug/pprof/", httppprof.Index)
		mux.HandleFunc("/debug/pprof/cmdline", httppprof.Cmdline)
		mux.HandleFunc("/debug/pprof/profile", httppprof.Profile)
		mux.HandleFunc("/debug/pprof/symbol", httppprof.Symbol)
		mux.HandleFunc("/debug/pprof/trace", httppprof.Trace)

		srv := &http.Server{
			Addr:         listenAddr,
			Handler:      mux,
			ReadTimeout:  5 * time.Second,
			WriteTimeout: 10 * time.Second,
			IdleTimeout:  120 * time.Second,
		}
		log.Error(srv.ListenAndServe())
	})
}

// TrackHeap tracks the size of the heap and dumps a profile if it passes a limit
func TrackHeap(appDir string, log *logger.Logger) {
	spawn := panics.GoroutineWrapperFunc(log)
	spawn("profiling.TrackHeap", func() {
		dumpFolder := filepath.Join(appDir, "dumps")
		err := os.MkdirAll(dumpFolder, 0o700)
		if err != nil {
			log.Errorf("Could not create heap dumps folder at %s", dumpFolder)
			return
		}
		const limitInGigabytes = 64 // We want to support 8 GB RAM, so we profile at 7
		trackHeapSize(limitInGigabytes*1024*1024*1024, dumpFolder, log)
	})
}

func trackHeapSize(heapLimit uint64, dumpFolder string, log *logger.Logger) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		memStats := &runtime.MemStats{}
		runtime.ReadMemStats(memStats)
		// If we passed the expected heap limit, dump the heap profile to a file
		if memStats.HeapAlloc > heapLimit {
			dumpHeapProfile(heapLimit, dumpFolder, memStats, log)
		}
	}
}

func dumpHeapProfile(heapLimit uint64, dumpFolder string, memStats *runtime.MemStats, log *logger.Logger) {
	heapFile := filepath.Join(dumpFolder, heapDumpFileName)
	log.Infof("Saving heap statistics into %s (HeapAlloc=%d > %d=heapLimit)", heapFile, memStats.HeapAlloc, heapLimit)
	f, err := os.Create(heapFile)
	if err != nil {
		log.Infof("Could not create heap profile: %s", err)
		return
	}
	defer f.Close()
	if err := runtimepprof.WriteHeapProfile(f); err != nil {
		log.Infof("Could not write heap profile: %s", err)
	}
}
