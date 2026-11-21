package server

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/daemon/pb"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter/server/grpcserver/protowire"
	"github.com/HoosatNetwork/HTND/v2/version"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type scanGapNode struct {
	protowire.UnimplementedRPCServer
	dagRequests atomic.Uint32
}

func (n *scanGapNode) MessageStream(stream protowire.RPC_MessageStreamServer) error {
	for {
		request, err := stream.Recv()
		if err != nil {
			return nil
		}
		message, err := request.ToAppMessage()
		if err != nil {
			return err
		}
		var response appmessage.Message
		switch message.(type) {
		case *appmessage.GetInfoRequestMessage:
			response = appmessage.NewGetInfoResponseMessage("", 0, version.Version(), false, true, true, 0, 0, "")
		case *appmessage.GetBlockDAGInfoRequestMessage:
			n.dagRequests.Add(1)
			response = &appmessage.GetBlockDAGInfoResponseMessage{VirtualDAAScore: scanGapAfterActivation}
		case *appmessage.GetUsableAddressesRequestMessage:
			response = appmessage.NewGetUsableAddressesResponse(nil)
		case *appmessage.GetMempoolEntriesByAddressesRequestMessage:
			response = appmessage.NewGetMempoolEntriesByAddressesResponseMessage(nil)
		case *appmessage.GetUTXOsByAddressesRequestMessage:
			response = appmessage.NewGetUTXOsByAddressesResponseMessage(nil)
		default:
			continue
		}
		protoResponse, err := protowire.FromAppMessage(response)
		if err != nil {
			return err
		}
		if err := stream.Send(protoResponse); err != nil {
			return nil
		}
	}
}

// This checks that Start wires the score from its sole DAG query into the notice.
// The offline scan tests cover other severities, later discovery, and repeated scans.
func TestDaemonLogsTheMLDSA44ScanGap(t *testing.T) {
	const childEnv = "HTNWALLET_SCAN_GAP_TEST_CHILD"
	if dir := os.Getenv(childEnv); dir != "" {
		runScanGapDaemon(t, dir)
		return
	}

	// Start initializes a process-wide logger, so run the daemon in a child.
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDaemonLogsTheMLDSA44ScanGap$", "-test.count=1")
	child.Env = append(os.Environ(), childEnv+"="+dir)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("daemon startup or shutdown failed: %v\n%s", err, output)
	}
	logged, err := os.ReadFile(filepath.Join(dir, "daemon_err.log"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(logged), "\n") {
		if strings.Contains(line, "[WRN] HSWD:") &&
			strings.Contains(line, "ML-DSA-44 multisig addresses can be neither built nor scanned") {
			return
		}
	}
	t.Fatalf("daemon did not log the partial-exchange warning at startup:\n%s", logged)
}

func runScanGapDaemon(t *testing.T, dir string) {
	defaultLogFile, defaultErrLogFile = filepath.Join(dir, "daemon.log"), filepath.Join(dir, "daemon_err.log")
	wallet := newMLWallet(t, 2, 2)
	delete(wallet.s.keysFile.MLDSA44Cosigners, wallet.s.keysFile.ExtendedPublicKeys[1])
	if err := wallet.s.keysFile.Save(); err != nil {
		t.Fatal(err)
	}

	node := &scanGapNode{}
	nodeListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	grpcServer := grpc.NewServer()
	protowire.RegisterRPCServer(grpcServer, node)
	go func() { _ = grpcServer.Serve(nodeListener) }()
	t.Cleanup(grpcServer.Stop)

	walletListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	walletAddress := walletListener.Addr().String()
	if err := walletListener.Close(); err != nil {
		t.Fatal(err)
	}
	started := make(chan error, 1)
	go func() {
		started <- Start(wallet.params, walletAddress, nodeListener.Addr().String(), wallet.s.keysFile.Path(), "", 5)
	}()

	connection, err := grpc.NewClient(walletAddress, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	daemon := pb.NewHtnwalletdClient(connection)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
waitForSync:
	for {
		select {
		case err := <-started:
			t.Fatalf("daemon stopped before initial sync: %v", err)
		case <-ctx.Done():
			t.Fatalf("daemon did not finish initial sync: %v", ctx.Err())
		case <-ticker.C:
			if _, err := daemon.ShowAddresses(ctx, &pb.ShowAddressesRequest{}); err == nil {
				break waitForSync
			}
		}
	}
	if got := node.dagRequests.Load(); got != 1 {
		t.Errorf("got %d DAG info requests, want only the startup request", got)
	}

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelShutdown()
	if _, err := daemon.Shutdown(shutdownCtx, &pb.ShutdownRequest{}); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	select {
	case err := <-started:
		if err != nil {
			t.Fatalf("Start returned %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("daemon did not stop after Shutdown")
	}
	backendLog.Close()
}
