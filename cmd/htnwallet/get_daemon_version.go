package main

import (
	"context"
	"fmt"

	"github.com/Hoosat-Oy/HTND/cmd/htnwallet/daemon/client"
	"github.com/Hoosat-Oy/HTND/cmd/htnwallet/daemon/pb"
)

func getDaemonVersion(conf *getDaemonVersionConfig) error {
	daemonClient, tearDown, err := client.Connect(conf.DaemonAddress)
	if err != nil {
		return err
	}
	defer tearDown()

	ctx, cancel := context.WithTimeout(context.Background(), daemonTimeout)
	defer cancel()
	response, err := daemonClient.GetVersion(ctx, &pb.GetVersionRequest{})
	if err != nil {
		return err
	}
	fmt.Println(response.Version)

	return nil
}
