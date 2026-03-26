package main

import (
	"context"
	"fmt"

	"github.com/Hoosat-Oy/HTND/cmd/htnwallet/daemon/client"
	"github.com/Hoosat-Oy/HTND/cmd/htnwallet/daemon/pb"
	"github.com/Hoosat-Oy/HTND/cmd/htnwallet/utils"
)

func balance(conf *balanceConfig) error {
	daemonClient, tearDown, err := client.Connect(conf.DaemonAddress)
	if err != nil {
		return err
	}
	defer tearDown()

	ctx, cancel := context.WithTimeout(context.Background(), daemonTimeout)
	defer cancel()
	response, err := daemonClient.GetBalance(ctx, &pb.GetBalanceRequest{})
	if err != nil {
		return err
	}

	pendingSuffix := ""
	if response.Pending > 0 {
		pendingSuffix = " (pending)"
	}
	if conf.Verbose {
		pendingSuffix = ""
		println("Address                                                                       Available  ")
		println("-----------------------------------------------------------------------------------------")
		for _, addressBalance := range response.AddressBalances {
			fmt.Printf("%s %s %s\n", addressBalance.Address, utils.FomatHSAT(addressBalance.Available), utils.FomatHSAT(addressBalance.Pending))
		}
		println("-----------------------------------------------------------------------------------------")
		print("                                                 ")
	}
	fmt.Printf("Total balance, HTN %s %s%s\n", utils.FomatHSAT(response.Available), utils.FomatHSAT(response.Pending), pendingSuffix)

	return nil
}
