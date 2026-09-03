package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/HoosatNetwork/HTND/cmd/htnwallet/daemon/client"
	"github.com/HoosatNetwork/HTND/cmd/htnwallet/daemon/pb"
	"github.com/HoosatNetwork/HTND/cmd/htnwallet/keys"
	"github.com/HoosatNetwork/HTND/cmd/htnwallet/libhtnwallet"
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
		fmt.Printf("[%s] compound failed: %v\n", time.Now().Format("15:04:05"), err)
	}
	for {
		<-ticker.C
		if err := compoundOnce(conf, daemonClient, mnemonics, keysFile.ECDSA); err != nil {
			fmt.Printf("[%s] compound failed: %v\n", time.Now().Format("15:04:05"), err)
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
		Limit:                    &conf.Limit,
	})
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

	// 2. Sign every transaction the daemon produced, not just the first.
	//
	// createUnsignedCompoundTransaction can return more than one. When the compound exceeds
	// MaximumStandardTransactionMass it is split, and maybeSplitAndMergeTransaction returns
	// [split_1 ... split_N, mergeTx]: every split pays the CHANGE address, and only mergeTx pays the
	// address the user asked for. Taking UnsignedTransactions[0] and dropping the rest therefore
	// broadcast a transaction that moved the coins to the change address, reported its txid as
	// success, and left the destination with nothing - which is exactly what a P2PKH or P2SH wallet
	// saw, because their larger signature scripts are what pushed the compound over the mass limit
	// and into splitting in the first place.
	//
	// Order is preserved: the daemon's broadcast submits sequentially, so each split is in the
	// mempool before mergeTx - which spends their outputs - is submitted.
	signedTxs := make([][]byte, len(resp.UnsignedTransactions))
	for i, unsignedTx := range resp.UnsignedTransactions {
		signedTx, err := libhtnwallet.Sign(conf.NetParams(), mnemonics, unsignedTx, ecdsa)
		if err != nil {
			return errors.Wrapf(err, "signing failed for transaction %d of %d", i+1, len(resp.UnsignedTransactions))
		}
		signedTxs[i] = signedTx
	}

	// 3. Broadcast
	bctx, bcancel := context.WithTimeout(context.Background(), daemonTimeout)
	defer bcancel()
	isHighPriority := false

	bresp, err := client.Broadcast(bctx, &pb.BroadcastRequest{
		Transactions:   signedTxs,
		AllowOrphan:    false,
		IsHighPriority: &isHighPriority,
	})
	if err != nil {
		errString := err.Error()
		// Handle rate limit gracefully
		switch {
		case strings.Contains(errString, "Compound transaction rate limit exceeded"):
			fmt.Printf("[%s] RATE LIMITED, backing off for 30s\n", time.Now().Format("15:04:05"))
			return errRateLimited
		case strings.Contains(errString, "already spent by transaction"):
			fmt.Printf("[%s] COMPOUND INPUTS WENT STALE, refreshing UTXOs and retrying in 5s\n", time.Now().Format("15:04:05"))
			time.Sleep(5 * time.Second)
			return nil
		default:
			fmt.Printf("[%s] COMPOUND SUBMIT FAILED, backing off for 30s, err: %s\n", time.Now().Format("15:04:05"), err)
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
