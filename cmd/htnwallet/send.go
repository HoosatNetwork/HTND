package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/HoosatNetwork/HTND/cmd/htnwallet/daemon/client"
	"github.com/HoosatNetwork/HTND/cmd/htnwallet/daemon/pb"
	"github.com/HoosatNetwork/HTND/cmd/htnwallet/keys"
	"github.com/HoosatNetwork/HTND/cmd/htnwallet/libhtnwallet"
	"github.com/HoosatNetwork/HTND/cmd/htnwallet/utils"
	"github.com/pkg/errors"
)

const (
	maxRetries = 10
	retryDelay = 2 * time.Second
)

func send(conf *sendConfig) error {
	keysFile, err := keys.ReadKeysFile(conf.NetParams(), conf.KeysFile)
	if err != nil {
		return err
	}

	if len(keysFile.ExtendedPublicKeys) > len(keysFile.EncryptedMnemonics) {
		return errors.Errorf("Cannot use 'send' command for multisig wallet without all of the keys")
	}

	daemonClient, tearDown, err := client.Connect(conf.DaemonAddress)
	if err != nil {
		return err
	}
	defer tearDown()

	ctx, cancel := context.WithTimeout(context.Background(), daemonTimeout)
	defer cancel()

	var sendAmountSompi uint64
	if !conf.IsSendAll {
		sendAmountSompi, err = utils.KasToSompi(conf.SendAmount)
		if err != nil {
			return err
		}
	}
retry:
	for attempt := 0; attempt <= maxRetries; attempt++ {
		createUnsignedTransactionsResponse, err := daemonClient.CreateUnsignedTransactions(ctx, &pb.CreateUnsignedTransactionsRequest{
			From:                     conf.FromAddresses,
			Address:                  conf.ToAddress,
			Amount:                   sendAmountSompi,
			IsSendAll:                conf.IsSendAll,
			UseExistingChangeAddress: conf.UseExistingChangeAddress,
			Payload:                  nil,
			Limit:                    &conf.Limit,
		})
		if err != nil {
			if strings.Contains(err.Error(), "Insufficient funds for send") {
				fmt.Printf("Waiting for spendable UTXO.\n")
				// Wait before asking again; retrying at once hammered the daemon in a tight loop.
				time.Sleep(retryDelay)
				attempt--
			} else {
				fmt.Printf("Failed to create unsigned transactions after %d attempts: %s\n", attempt, err)
				time.Sleep(retryDelay)
			}
			continue retry
		}

		if len(conf.Password) == 0 {
			conf.Password = keys.GetPassword("Password:")
		}
		mnemonics, err := keysFile.DecryptMnemonics(conf.Password)
		if err != nil {
			if strings.Contains(err.Error(), "message authentication failed") {
				fmt.Fprintf(os.Stderr, "Password decryption failed. Sometimes this is a result of not "+
					"specifying the same keys file used by the wallet daemon process.\n")
			}
			return err
		}

		signedTransactions := make([][]byte, len(createUnsignedTransactionsResponse.UnsignedTransactions))
		for i, unsignedTransaction := range createUnsignedTransactionsResponse.UnsignedTransactions {
			signedTransaction, err := libhtnwallet.Sign(conf.NetParams(), mnemonics, unsignedTransaction, keysFile.ECDSA)
			if err != nil {
				fmt.Printf("Failed to sign unsigned transactions after %d attempts: %s\n", attempt, err)
				time.Sleep(retryDelay)
				continue retry
			}
			signedTransactions[i] = signedTransaction
		}

		fmt.Printf("Broadcasting %d transaction(s)\n", len(signedTransactions))
		// Since we waited for user input when getting the password, which could take unbound amount of time -
		// create a new context for broadcast, to reset the timeout.
		broadcastCtx, broadcastCancel := context.WithTimeout(context.Background(), daemonTimeout)
		defer broadcastCancel()

		const chunkSize = 100 // To avoid sending a message bigger than the gRPC max message size, we split it to chunks
		for offset := 0; offset < len(signedTransactions); offset += chunkSize {
			end := min(offset+chunkSize, len(signedTransactions))

			chunk := signedTransactions[offset:end]
			response, err := daemonClient.Broadcast(broadcastCtx, &pb.BroadcastRequest{Transactions: chunk})
			if err != nil {
				// Never build new transactions and try again after a broadcast error: it does not mean nothing
				// was sent. The daemon submits transactions one by one, and a lost response looks the same as
				// a failure, so the payment may already be in the node's mempool. New transactions would spend
				// other coins and pay the recipient again.
				return errors.Wrapf(err, "failed to broadcast transactions (%d of %d were broadcast before the "+
					"failing batch); check the recipient's balance and the wallet's before trying again",
					offset, len(signedTransactions))
			}

			fmt.Printf("Broadcasted %d transaction(s) (broadcasted %.2f%% of the transactions so far)\n", len(chunk), 100*float64(end)/float64(len(signedTransactions)))
			fmt.Println("Broadcasted Transaction ID(s): ")
			for _, txID := range response.TxIDs {
				fmt.Printf("\t%s\n", txID)
			}
		}

		if conf.Verbose {
			fmt.Println("Serialized Transaction(s) (can be parsed via the `parse` command or resent via `broadcast`): ")
			for _, signedTx := range signedTransactions {
				fmt.Printf("\t%x\n\n", signedTx)
			}
		}
		return nil
	}

	return errors.Errorf("Failed to send after %d attempts", maxRetries+1)
}
