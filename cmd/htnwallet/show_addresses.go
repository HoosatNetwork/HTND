package main

import (
	"context"
	"fmt"

	"github.com/Hoosat-Oy/HTND/cmd/htnwallet/daemon/client"
	"github.com/Hoosat-Oy/HTND/cmd/htnwallet/daemon/pb"
)

func showAddresses(conf *showAddressesConfig) error {
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

	request := &pb.ShowAddressesRequest{AddressType: addressType}
	if conf.IncludeBoth {
		request.IncludeBoth = true
	}

	response, err := daemonClient.ShowAddresses(ctx, request)
	if err != nil {
		return err
	}

	header := "Addresses"
	if conf.IncludeBoth {
		header = "Addresses (both P2PK and P2PKH)"
	}
	fmt.Printf("%s (%d):\n", header, len(response.Address))
	for _, address := range response.Address {
		fmt.Println(address)
	}

	fmt.Printf("\nNote: the above are only addresses that were manually created by the 'new-address' command. If you want to see a list of all addresses, including change addresses, " +
		"that have a positive balance, use the command 'balance -v'\n")
	return nil
}
