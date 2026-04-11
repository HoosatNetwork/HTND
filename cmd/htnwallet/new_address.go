package main

import (
	"context"
	"fmt"

	"github.com/Hoosat-Oy/HTND/cmd/htnwallet/daemon/client"
	"github.com/Hoosat-Oy/HTND/cmd/htnwallet/daemon/pb"
)

func newAddress(conf *newAddressConfig) error {
	daemonClient, tearDown, err := client.Connect(conf.DaemonAddress)
	if err != nil {
		return err
	}
	defer tearDown()

	ctx, cancel := context.WithTimeout(context.Background(), daemonTimeout)
	defer cancel()

	addressType, err := parseAddressTypeFlag(conf.AddressType)
	if err != nil {
		return err
	}

	response, err := daemonClient.NewAddress(ctx, &pb.NewAddressRequest{AddressType: addressType})
	if err != nil {
		return err
	}

	fmt.Printf("New address:\n%s\n", response.Address)
	if conf.IncludeBoth && (response.P2PkAddress != "" || response.P2PkhAddress != "") {
		fmt.Printf("\nP2PK:\n%s\n", response.P2PkAddress)
		fmt.Printf("P2PKH:\n%s\n", response.P2PkhAddress)
	}
	return nil
}
