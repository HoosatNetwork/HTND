package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Hoosat-Oy/HTND/cmd/htnwallet/daemon/client"
	"github.com/Hoosat-Oy/HTND/cmd/htnwallet/daemon/pb"
	"github.com/Hoosat-Oy/HTND/cmd/htnwallet/keys"
	"github.com/Hoosat-Oy/HTND/cmd/htnwallet/libhtnwallet"
	"github.com/pkg/errors"
)

// sentinel error used to indicate a compound attempt hit the daemon rate limit
var errRateLimited = errors.New("rate limited")

func autoCompound(conf *autoCompoundConfig) error {
	if conf.CompoundRate < 6 {
		conf.CompoundRate = 60
	}
	tickerSecond := time.Duration(conf.CompoundRate) * time.Second
	fmt.Printf("Hoosat Auto-Compounder STARTED → 1 compound tx every %d seconds\n", int(tickerSecond.Seconds()))

	// === Load keys ===
	keysFile, err := keys.ReadKeysFile(conf.NetParams(), conf.KeysFile)
	if err != nil {
		return errors.Wrap(err, "reading keys file")
	}

	if len(keysFile.ExtendedPublicKeys) > len(keysFile.EncryptedMnemonics) {
		return errors.New("multisig wallet detected but not all private keys present")
	}

	if len(conf.Password) == 0 {
		conf.Password = keys.GetPassword("Enter wallet password: ")
	}

	mnemonics, err := keysFile.DecryptMnemonics(conf.Password)
	if err != nil {
		return errors.Wrap(err, "wrong password")
	}

	// === Connect to htnwallet daemon ===
	daemonClient, tearDown, err := client.Connect(conf.DaemonAddress)
	if err != nil {
		return errors.Wrap(err, "connecting to htnwallet daemon")
	}
	defer tearDown()

	ticker := time.NewTicker(tickerSecond)
	defer ticker.Stop()

	if err := compoundOnce(conf, daemonClient, mnemonics, keysFile.ECDSA); err != nil {
		if errors.Is(err, errRateLimited) {
			fmt.Printf("[%s] rate limited, backing off for 2m\n", time.Now().Format("15:04:05"))
			time.Sleep(2 * time.Minute)
		} else {
			fmt.Printf("[%s] compound failed: %v\n", time.Now().Format("15:04:05"), err)
		}
	}
	for {
		<-ticker.C
		if err := compoundOnce(conf, daemonClient, mnemonics, keysFile.ECDSA); err != nil {
			if errors.Is(err, errRateLimited) {
				fmt.Printf("[%s] rate limited, backing off for 2m\n", time.Now().Format("15:04:05"))
				time.Sleep(2 * time.Minute)
			} else {
				fmt.Printf("[%s] compound failed: %v\n", time.Now().Format("15:04:05"), err)
			}
			continue
		}
	}
}

func compoundOnce(
	conf *autoCompoundConfig,
	client pb.HtnwalletdClient, // CORRECT TYPE
	mnemonics []string,
	ecdsa bool,
) error {
	ctx, cancel := context.WithTimeout(context.Background(), daemonTimeout)
	defer cancel()

	// 1. Create unsigned tx
	resp, err := client.CreateUnsignedCompoundTransaction(ctx, &pb.CreateUnsignedCompoundTransactionRequest{
		From:                     conf.FromAddresses,
		Address:                  conf.ToAddress,
		UseExistingChangeAddress: conf.UseExistingChangeAddress,
	})
	fmt.Printf("%s", len(resp.UnsignedTransactions))
	if err != nil {
		fmt.Printf("[%s] NOTHING TO COMPOUND → Error: %s, backing off for 5m\n", time.Now().Format("15:04:05"), err)
		time.Sleep(5 * time.Minute)
		return nil
	}

	if len(resp.UnsignedTransactions) == 0 {
		fmt.Printf("[%s] NOTHING TO COMPOUND, backing off for 5m\n", time.Now().Format("15:04:05"))
		time.Sleep(5 * time.Minute)
		return nil
	}

	unsignedTx := resp.UnsignedTransactions[0]

	// 2. Sign
	signedTx, err := libhtnwallet.Sign(conf.NetParams(), mnemonics, unsignedTx, ecdsa)
	if err != nil {
		return errors.Wrap(err, "signing failed")
	}

	// 3. Broadcast
	bctx, bcancel := context.WithTimeout(context.Background(), daemonTimeout)
	defer bcancel()

	bresp, err := client.Broadcast(bctx, &pb.BroadcastRequest{
		Transactions: [][]byte{signedTx},
		AllowOrphan:  true,
	})
	if err != nil {
		// Handle rate limit gracefully
		if strings.Contains(err.Error(), "Compound transaction rate limit exceeded") {
			fmt.Printf("[%s] RATE LIMITED, backing off for 30s\n", time.Now().Format("15:04:05"))
			return errRateLimited
		} else {
			fmt.Printf("[%s] NOTHING TO COMPOUND, backing off for 30s, err: %s\n", time.Now().Format("15:04:05"), err)
		}
		time.Sleep(30 * time.Second)
		return nil
	}

	// 4. Success
	for _, txid := range bresp.TxIDs {
		fmt.Printf("[%s] COMPOUNDED → https://explorer.hoosat.fi/txs/%s\n",
			time.Now().Format("15:04:05"), txid)
	}

	return nil
}
