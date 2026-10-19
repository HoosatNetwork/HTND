//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

// The platforms on which htnd shuts down on SIGTERM (infrastructure/os/signal/signalsigterm.go).

package app

import (
	"bufio"
	"net"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/config"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/logger"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/rpcclient"
)

const secondSignalChildEnv = "HTND_SHUTDOWN_STRESS_SIGNAL_CHILD"

func freeLoopbackAddress(t *testing.T) string {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().String()
}

func secondSignalConfig(appDir, p2pAddress, rpcAddress string) *config.Config {
	cfg := config.DefaultConfig()
	params := dagconfig.SimnetParams
	cfg.ActiveNetParams = &params
	cfg.Simnet = true
	cfg.AppDir = appDir
	cfg.Listeners = []string{p2pAddress}
	cfg.RPCListeners = []string{rpcAddress}
	cfg.DisableDNSSeed = true
	cfg.TargetOutboundPeers = 0
	cfg.AutoUpdateEnabled = config.Bool(false)
	return cfg
}

// TestShutdownStressSecondSignal runs htnd in a child process, through StartAppWithConfig as main.go does (including
// the log backend's teardown), and sends it a second SIGTERM while the shutdown that the first one started is still
// running. An RPC client keeps a stream open, which holds the RPC server's graceful stop for its two seconds. The
// child must log both signals, the second one before it closes the database, exit with status 0, and leave a datadir
// that opens again.
func TestShutdownStressSecondSignal(t *testing.T) {
	if os.Getenv("HTND_SHUTDOWN_STRESS") != "1" {
		t.Skip("set HTND_SHUTDOWN_STRESS=1 to run the shutdown stress harness")
	}
	appDir, p2pAddress, rpcAddress := t.TempDir(), freeLoopbackAddress(t), freeLoopbackAddress(t)
	child := exec.Command(os.Args[0], "-test.run=^TestShutdownStressSecondSignalChild$", "-test.v", "-test.count=1")
	child.Env = append(os.Environ(), secondSignalChildEnv+"="+strings.Join([]string{appDir, p2pAddress, rpcAddress}, ","))
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	child.Stderr = child.Stdout
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	lines := make(chan string, 1024)
	exited := make(chan struct{})
	var scanErr, exitErr error
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		scanErr = scanner.Err()
		close(lines)
		exitErr = child.Wait()
		close(exited)
	}()
	// However the test ends, the child is killed if it is still running, and reaped.
	t.Cleanup(func() {
		_ = child.Process.Kill()
		for range lines {
		}
		<-exited
	})
	var output strings.Builder
	// endOfOutput fails if reading the child's output failed; call it once lines is closed.
	endOfOutput := func() {
		t.Helper()
		if scanErr != nil {
			t.Fatalf("reading the child's output: %+v; output:\n%s", scanErr, output.String())
		}
	}
	// waitFor reads the child's output until a line contains want, failing when timeout fires first. It fails if a
	// line containing any of notBefore comes first.
	waitFor := func(timeout <-chan time.Time, want string, notBefore ...string) {
		t.Helper()
		for {
			select {
			case line, ok := <-lines:
				if !ok {
					endOfOutput()
					t.Fatalf("the child exited before logging %q; output:\n%s", want, output.String())
				}
				output.WriteString(line + "\n")
				for _, unwanted := range notBefore {
					if strings.Contains(line, unwanted) {
						t.Fatalf("the child logged %q before %q; output:\n%s", unwanted, want, output.String())
					}
				}
				if strings.Contains(line, want) {
					return
				}
			case <-timeout:
				t.Fatalf("the child did not log %q; output:\n%s", want, output.String())
			}
		}
	}

	var rpcClient *rpcclient.RPCClient
	deadline := time.Now().Add(time.Minute)
	for rpcClient == nil {
		rpcClient, err = rpcclient.NewRPCClient(rpcAddress)
		if err != nil {
			if time.Now().After(deadline) {
				t.Fatalf("the child's RPC server did not come up: %+v", err)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	defer rpcClient.Close()

	if err := child.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("sending the first SIGTERM: %+v", err)
	}
	waitFor(time.After(time.Minute), "Received signal (terminated). Shutting down")
	// One deadline, from the second signal to the child's exit.
	timeout := time.After(time.Minute)
	if err := child.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("sending the second SIGTERM: %+v", err)
	}
	waitFor(timeout, "Already shutting down", "Gracefully shutting down the database")
	for open := true; open; {
		select {
		case line, ok := <-lines:
			open = ok
			if ok {
				output.WriteString(line + "\n")
			}
		case <-timeout:
			t.Fatalf("the child did not exit within a minute of the second signal; output:\n%s", output.String())
		}
	}
	endOfOutput()
	select {
	case <-exited:
	case <-timeout:
		t.Fatalf("the child did not exit within a minute of the second signal; output:\n%s", output.String())
	}
	if exitErr != nil {
		t.Fatalf("the child exited with %v; output:\n%s", exitErr, output.String())
	}
	if !strings.Contains(output.String(), "Shutdown complete") {
		t.Fatalf("the child did not complete its shutdown; output:\n%s", output.String())
	}

	db, err := openDB(secondSignalConfig(appDir, p2pAddress, rpcAddress))
	if err != nil {
		t.Fatalf("the datadir does not open again: %+v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("closing the reopened datadir: %+v", err)
	}
}

// TestShutdownStressSecondSignalChild is the child process of TestShutdownStressSecondSignal.
func TestShutdownStressSecondSignalChild(t *testing.T) {
	arguments := os.Getenv(secondSignalChildEnv)
	if arguments == "" {
		t.Skip("run by TestShutdownStressSecondSignal")
	}
	parts := strings.Split(arguments, ",")
	logger.SetLogLevels(logger.LevelInfo)
	logger.InitLogStdout(logger.LevelInfo)
	if err := StartAppWithConfig(secondSignalConfig(parts[0], parts[1], parts[2])); err != nil {
		t.Fatalf("StartAppWithConfig: %+v", err)
	}
}
