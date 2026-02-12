package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Hoosat-Oy/HTND/cmd/htnwallet/daemon/client"
	"github.com/Hoosat-Oy/HTND/cmd/htnwallet/daemon/pb"
	"github.com/Hoosat-Oy/HTND/cmd/htnwallet/keys"
	"github.com/Hoosat-Oy/HTND/cmd/htnwallet/libhtnwallet"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/constants"
	"github.com/pkg/errors"
)

func vote(conf *voteConfig) error {
	keysFile, err := keys.ReadKeysFile(conf.NetParams(), conf.KeysFile)
	if err != nil {
		return err
	}

	if len(keysFile.ExtendedPublicKeys) > len(keysFile.EncryptedMnemonics) {
		return errors.Errorf("Cannot use 'vote' command for multisig wallet without all of the keys")
	}

	daemonClient, tearDown, err := client.Connect(conf.DaemonAddress)
	if err != nil {
		return err
	}
	defer tearDown()

	ctx, cancel := context.WithTimeout(context.Background(), daemonTimeout)
	defer cancel()

	// Fixed voting address
	votingAddress := "hoosat:qz8hek32xdryqstk6ptvvfzmrsrns95h7nd2r9f55epnxx7eummegyxa7f2lu"

	// Create vote payload
	votePayload := map[string]interface{}{
		"type":   "vote_cast",
		"v":      1,
		"pollId": conf.PollId,
		"votes":  conf.Votes,
	}
	payloadBytes, err := json.Marshal(votePayload)
	if err != nil {
		return errors.Wrap(err, "Failed to marshal vote payload")
	}

	// Send 1 HTN to the voting platform
	sendAmountSompi := uint64(constants.SompiPerHoosat)

retry:
	for attempt := 0; attempt <= maxRetries; attempt++ {
		createUnsignedTransactionsResponse, err :=
			daemonClient.CreateUnsignedTransactions(ctx, &pb.CreateUnsignedTransactionsRequest{
				From:                     conf.FromAddresses,
				Address:                  votingAddress,
				Amount:                   sendAmountSompi,
				IsSendAll:                false,
				UseExistingChangeAddress: conf.UseExistingChangeAddress,
				Payload:                  payloadBytes,
			})
		if err != nil {
			if strings.Contains(err.Error(), "Insufficient funds for send") {
				fmt.Printf("Waiting for spendable UTXO.\n")
				attempt = attempt - 1
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
			end := len(signedTransactions)
			if offset+chunkSize <= len(signedTransactions) {
				end = offset + chunkSize
			}

			chunk := signedTransactions[offset:end]
			response, err := daemonClient.Broadcast(broadcastCtx, &pb.BroadcastRequest{Transactions: chunk})
			if err != nil {
				broadcastCancel()
				fmt.Printf("Failed to broadcast transactions after %d attempts: %s\n", attempt, err)
				time.Sleep(retryDelay)
				continue retry
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
		break
	}

	return nil
}
