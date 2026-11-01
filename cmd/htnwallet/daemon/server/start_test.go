package server

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/keys"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/libhtnwallet"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter/server/grpcserver/protowire"
	"github.com/HoosatNetwork/HTND/v2/version"
	"google.golang.org/grpc"
)

// noDAGInfoNode is a node RPC server that answers GetInfo and fails every GetBlockDAGInfo.
type noDAGInfoNode struct {
	protowire.UnimplementedRPCServer
	openStreams atomic.Int32
}

func (n *noDAGInfoNode) MessageStream(stream protowire.RPC_MessageStreamServer) error {
	n.openStreams.Add(1)
	defer n.openStreams.Add(-1)
	for {
		request, err := stream.Recv()
		if err != nil {
			return nil
		}
		appMessage, err := request.ToAppMessage()
		if err != nil {
			return err
		}
		var response appmessage.Message
		switch appMessage.(type) {
		case *appmessage.GetInfoRequestMessage:
			response = appmessage.NewGetInfoResponseMessage("", 0, version.Version(), false, true, true, 0, 0, "")
		case *appmessage.GetBlockDAGInfoRequestMessage:
			response = &appmessage.GetBlockDAGInfoResponseMessage{Error: appmessage.RPCErrorf("test failure")}
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

const startTestChildEnv = "HTNWALLET_START_TEST_CHILD"

// TestStartReturnsDAGInfoError pins that Start fails, and releases what it acquired, when the node cannot report its
// DAG info. Start used to return nil there, so start-daemon exited with success without serving or saying why.
func TestStartReturnsDAGInfoError(t *testing.T) {
	// Start sets up process-wide state that can be set up only once (the log backend), so the scenario runs in a
	// child process of its own.
	if os.Getenv(startTestChildEnv) != "1" {
		child := exec.Command(os.Args[0], "-test.run=^TestStartReturnsDAGInfoError$", "-test.count=1", "-test.v")
		child.Env = append(os.Environ(), startTestChildEnv+"=1")
		output, err := child.CombinedOutput()
		if err != nil {
			t.Fatalf("the Start scenario failed: %v\n%s", err, output)
		}
		return
	}

	logDir := t.TempDir()
	savedLogFile, savedErrLogFile := defaultLogFile, defaultErrLogFile
	defaultLogFile, defaultErrLogFile = filepath.Join(logDir, "daemon.log"), filepath.Join(logDir, "daemon_err.log")
	t.Cleanup(func() { defaultLogFile, defaultErrLogFile = savedLogFile, savedErrLogFile })

	nodeListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %+v", err)
	}
	node := &noDAGInfoNode{}
	grpcServer := grpc.NewServer()
	protowire.RegisterRPCServer(grpcServer, node)
	go func() { _ = grpcServer.Serve(nodeListener) }()
	t.Cleanup(grpcServer.Stop)

	params := &dagconfig.MainnetParams
	mnemonic, err := libhtnwallet.CreateMnemonic()
	if err != nil {
		t.Fatalf("CreateMnemonic: %+v", err)
	}
	keysFile, err := keys.NewFileFromMnemonic(params, mnemonic, "password")
	if err != nil {
		t.Fatalf("NewFileFromMnemonic: %+v", err)
	}
	keysFilePath := filepath.Join(t.TempDir(), "keys.json")
	if err := keysFile.SetPath(params, keysFilePath, true); err != nil {
		t.Fatalf("SetPath: %+v", err)
	}
	if err := keysFile.Save(); err != nil {
		t.Fatalf("Save: %+v", err)
	}

	walletListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %+v", err)
	}
	walletAddress := walletListener.Addr().String()
	_ = walletListener.Close()

	nodeAddress := nodeListener.Addr().String()
	err = Start(params, walletAddress, nodeAddress, keysFilePath, "", 5)
	if err == nil {
		t.Fatalf("Start returned nil although the node failed GetBlockDAGInfo")
	}
	if !strings.Contains(err.Error(), "test failure") || !strings.Contains(err.Error(), nodeAddress) {
		t.Fatalf("the error does not carry the node's failure and address: %s", err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for node.openStreams.Load() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("Start left %d RPC streams to the node open after failing", node.openStreams.Load())
		}
		time.Sleep(10 * time.Millisecond)
	}

	walletListener, err = net.Listen("tcp", walletAddress)
	if err != nil {
		t.Fatalf("Start kept listening on %s after failing: %+v", walletAddress, err)
	}
	_ = walletListener.Close()
	sameKeysFile, err := keys.ReadKeysFile(params, keysFilePath)
	if err != nil {
		t.Fatalf("ReadKeysFile: %+v", err)
	}
	if err := sameKeysFile.TryLock(); err != nil {
		t.Fatalf("Start kept the keys file locked after failing: %+v", err)
	}
}
