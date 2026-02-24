package client

import (
	"context"
	"time"

	"github.com/Hoosat-Oy/HTND/cmd/htnwallet/daemon/server"
	"github.com/pkg/errors"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	pb "github.com/Hoosat-Oy/HTND/cmd/htnwallet/daemon/pb"
)

// Connect connects to htnwalletd with a 2-second timeout.
// Fully compatible with gRPC Go v1.68+ and Go 1.24.
func Connect(address string) (pb.HtnwalletdClient, func(), error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	client, err := grpc.NewClient(
		address,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(server.MaxDaemonMsgSize),
		),
	)
	if err != nil {
		return nil, nil, errors.Wrap(err, "failed to initialize gRPC client")
	}

	// Force connection attempt with a no-op health check
	_ = client.Invoke(ctx, "/grpc.health.v1.Health/Check", &struct{}{}, &struct{}{})

	if ctx.Err() != nil {
		client.Close() // ignore error — we're failing anyway
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, nil, errors.New("htnwallet daemon is not running — start it with `htnwallet start-daemon`")
		}
		return nil, nil, errors.Wrap(ctx.Err(), "connection timeout")
	}

	return pb.NewHtnwalletdClient(client), func() {
		_ = client.Close() // ignore error in cleanup
	}, nil
}
