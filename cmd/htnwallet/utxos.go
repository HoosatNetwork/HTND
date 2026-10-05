package main

import (
	"context"
	"fmt"

	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/daemon/client"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/daemon/pb"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/utils"
)

func utxos(conf *utxosConfig) error {
	daemonClient, tearDown, err := client.Connect(conf.DaemonAddress)
	if err != nil {
		return err
	}
	defer tearDown()

	ctx, cancel := context.WithTimeout(context.Background(), daemonTimeout)
	defer cancel()
	response, err := daemonClient.GetUTXOs(ctx, &pb.GetUTXOsRequest{})
	if err != nil {
		return err
	}

	var total uint64
	for _, utxo := range response.Utxos {
		total += utxo.UtxoEntry.Amount
		coinbase := ""
		if utxo.UtxoEntry.IsCoinbase {
			coinbase = " coinbase"
		}
		fmt.Printf("%s:%d %s %s %s DAA %d%s\n", utxo.Outpoint.TransactionId, utxo.Outpoint.Index,
			utils.FomatHSAT(utxo.UtxoEntry.Amount), utxo.Address, utxo.DerivationPath,
			utxo.UtxoEntry.BlockDaaScore, coinbase)
	}
	fmt.Printf("%d UTXOs, total HTN %s\n", len(response.Utxos), utils.FomatHSAT(total))
	return nil
}
