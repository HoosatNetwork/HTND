// More robust broadcast ordering: broadcast splits first, then wait for mempool observation
// (via daemon WaitForMempoolEntries RPC when available), then broadcast merge(s).
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/Hoosat-Oy/HTND/cmd/htnwallet/daemon/client"
	pb "github.com/Hoosat-Oy/HTND/cmd/htnwallet/daemon/pb"
	"github.com/Hoosat-Oy/HTND/cmd/htnwallet/keys"
	"github.com/Hoosat-Oy/HTND/cmd/htnwallet/libhtnwallet"
	"github.com/pkg/errors"
	"google.golang.org/grpc"
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
	daemonClient pb.HtnwalletdClient,
	mnemonics []string,
	ecdsa bool,
) error {
	ctx, cancel := context.WithTimeout(context.Background(), daemonTimeout)
	defer cancel()

	// 1. Create unsigned tx(s)
	resp, err := daemonClient.CreateUnsignedCompoundTransaction(ctx, &pb.CreateUnsignedCompoundTransactionRequest{
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

	// 2. Sign each unsigned transaction
	signedTransactions := make([][]byte, len(resp.UnsignedTransactions))
	for i, unsignedTx := range resp.UnsignedTransactions {
		signedTx, err := libhtnwallet.Sign(conf.NetParams(), mnemonics, unsignedTx, ecdsa)
		if err != nil {
			return errors.Wrap(err, "signing transaction")
		}
		signedTransactions[i] = signedTx
	}

	// 3. Broadcast using safer ordering: splits first, wait for mempool observation, then merges
	broadcastCtx, broadcastCancel := context.WithTimeout(context.Background(), daemonTimeout)
	defer broadcastCancel()

	txIDs, err := sendSignedBatch(broadcastCtx, daemonClient, signedTransactions)
	if err != nil {
		fmt.Printf("[%s] Failed to broadcast transactions: %v\n", time.Now().Format("15:04:05"), err)
		// Backoff similar to previous behavior
		time.Sleep(retryDelay)
		return err
	}

	for _, txID := range txIDs {
		fmt.Printf("[%s] COMPOUNDED → https://explorer.hoosat.fi/txs/%s\n", time.Now().Format("15:04:05"), txID)
	}
	return nil
}

// sendSingleTx calls daemon Broadcast for a single transaction and retries on transient failures.
func sendSingleTx(ctx context.Context, daemonClient pb.HtnwalletdClient, tx []byte, retries int, retryDelay time.Duration) (string, error) {
	for attempt := 0; attempt <= retries; attempt++ {
		resp, err := daemonClient.Broadcast(ctx, &pb.BroadcastRequest{Transactions: [][]byte{tx}})
		if err == nil {
			if len(resp.TxIDs) > 0 {
				return resp.TxIDs[0], nil
			}
			return "", fmt.Errorf("daemon.Broadcast returned empty TxIDs")
		}
		// on error, retry a few times
		if attempt < retries {
			select {
			case <-time.After(retryDelay):
				continue
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		return "", err
	}
	return "", fmt.Errorf("unreachable")
}

// sendSignedBatch broadcasts signedTransactions in a safer order: splits first, wait until splits observed in mempool,
// then broadcast merge(s). It returns txIDs in same order as the input slice.
func sendSignedBatch(ctx context.Context, daemonClient pb.HtnwalletdClient, signedTransactions [][]byte) ([]string, error) {
	numTx := len(signedTransactions)
	if numTx == 0 {
		return nil, nil
	}

	// Heuristic: if there is more than one tx, treat the last tx as merge and earlier txs as splits.
	splitCount := 0
	if numTx > 1 {
		splitCount = numTx - 1
	}

	txIDs := make([]string, numTx)

	// Broadcast splits one-by-one with small delay
	for i := 0; i < splitCount; i++ {
		id, err := sendSingleTx(ctx, daemonClient, signedTransactions[i], 2, 1*time.Second)
		if err != nil {
			return nil, fmt.Errorf("failed to broadcast split tx %d: %w", i, err)
		}
		txIDs[i] = id
		// small pause to help propagation
		select {
		case <-time.After(300 * time.Millisecond):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	// If we had splits, attempt to wait for their presence using the daemon's WaitForMempoolEntries RPC
	if splitCount > 0 {
		splitTxIDs := make([]string, 0, splitCount)
		for i := 0; i < splitCount; i++ {
			splitTxIDs = append(splitTxIDs, txIDs[i])
		}

		waitReq := &pb.WaitForMempoolEntriesRequest{
			Txids:          splitTxIDs,
			TimeoutSeconds: 10, // tune as needed for 5BPS network (start at 10s)
		}

		// Use type assertion: if the concrete client supports the generated gRPC method we call it.
		type waitForMempoolEntriesClient interface {
			WaitForMempoolEntries(ctx context.Context, in *pb.WaitForMempoolEntriesRequest, opts ...grpc.CallOption) (*pb.WaitForMempoolEntriesResponse, error)
		}

		if wc, ok := daemonClient.(waitForMempoolEntriesClient); ok {
			waitCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			waitResp, err := wc.WaitForMempoolEntries(waitCtx, waitReq)
			if err != nil {
				// RPC failure -> log and proceed (fallback)
				fmt.Printf("WaitForMempoolEntries RPC failed: %v; proceeding to broadcast merges\n", err)
			} else if !waitResp.Ok {
				fmt.Printf("WaitForMempoolEntries: not all splits observed before timeout; observed=%v; proceeding\n", waitResp.ObservedTxids)
			} else {
				fmt.Printf("All split txs observed in mempool: %v\n", waitResp.ObservedTxids)
			}
		} else {
			// Fallback: the daemon client doesn't expose WaitForMempoolEntries (older stubs). Use a wall-clock wait.
			fmt.Printf("Daemon client does not expose WaitForMempoolEntries RPC; using wall-clock wait fallback\n")
			select {
			case <-time.After(10 * time.Second):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}

	// Broadcast remaining txs (merges or single tx)
	for i := splitCount; i < numTx; i++ {
		id, err := sendSingleTx(ctx, daemonClient, signedTransactions[i], 1, 2*time.Second)
		if err != nil {
			return nil, fmt.Errorf("failed to broadcast merge/tx %d: %w", i, err)
		}
		txIDs[i] = id
	}

	return txIDs, nil
}
