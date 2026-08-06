package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/HoosatNetwork/HTND/cmd/htnwallet/daemon/client"
	"github.com/HoosatNetwork/HTND/cmd/htnwallet/daemon/pb"
	"github.com/HoosatNetwork/HTND/cmd/htnwallet/keys"
	"github.com/HoosatNetwork/HTND/cmd/htnwallet/libhtnwallet"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/constants"
	"github.com/pkg/errors"
)

// CreateVotePayload represents the request body sent to POST /api/votes
type CreateVotePayload struct {
	TxHash    string  `json:"txHash"`
	PollID    string  `json:"pollId"`
	Voter     string  `json:"voter"`
	Votes     []int   `json:"votes"`
	Weight    float64 `json:"weight"`
	Timestamp int64   `json:"timestamp"`
}

// VoteResponse represents the successful 201 Created response structure
type VoteResponse struct {
	ID        string  `json:"id"`
	PollID    string  `json:"pollId"`
	Voter     string  `json:"voter"`
	Votes     []int   `json:"votes"`
	Weight    float64 `json:"weight"`
	Timestamp int64   `json:"timestamp"`
	TxHash    string  `json:"txHash"`
}

// APIError captures non-201 HTTP status responses returned by the API
type APIError struct {
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("API error (status %d): %s", e.StatusCode, e.Message)
}

func CreateVote(ctx context.Context, client *http.Client, payload CreateVotePayload) (*VoteResponse, error) {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}

	endpoint := fmt.Sprintf("https://vote.hoosat.net/api/votes")

	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to encode request payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewBuffer(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to build HTTP request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("network error submitting vote: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	// Handle error response statuses (400, 404, 409, 500, etc.)
	if resp.StatusCode != http.StatusCreated {
		var errData struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(respBody, &errData); err == nil && errData.Error != "" {
			return nil, &APIError{StatusCode: resp.StatusCode, Message: errData.Error}
		}
		return nil, &APIError{StatusCode: resp.StatusCode, Message: string(respBody)}
	}

	// Unmarshal success response
	var vote VoteResponse
	if err := json.Unmarshal(respBody, &vote); err != nil {
		return nil, fmt.Errorf("failed to decode success response: %w", err)
	}

	return &vote, nil
}

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

	if conf.FromAddresses[0] == "" {
		return errors.Wrap(err, "You need to specify from address")
	}

	if conf.PollID == "" {
		return errors.Wrap(err, "You need to specify Poll transaction ID")
	}

	if conf.Votes[0] == -1 {
		return errors.Wrap(err, "You need to specify at least single vote by index (starting from 0).")
	}

	// Fixed voting address
	votingAddress := "hoosat:qz8hek32xdryqstk6ptvvfzmrsrns95h7nd2r9f55epnxx7eummegyxa7f2lu"

	// Create vote payload
	votePayload := map[string]any{
		"type":   "vote_cast",
		"v":      1,
		"pollId": conf.PollID,
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
		createUnsignedTransactionsResponse, err := daemonClient.CreateUnsignedTransactions(ctx, &pb.CreateUnsignedTransactionsRequest{
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

		var firstSplitTxId string
		const chunkSize = 100 // To avoid sending a message bigger than the gRPC max message size, we split it to chunks
		for offset := 0; offset < len(signedTransactions); offset += chunkSize {
			end := min(offset+chunkSize, len(signedTransactions))

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
			firstSplitTxId = response.TxIDs[0]
		}

		if conf.Verbose {
			fmt.Println("Serialized Transaction(s) (can be parsed via the `parse` command or resent via `broadcast`): ")
			for _, signedTx := range signedTransactions {
				fmt.Printf("\t%x\n\n", signedTx)
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		payload := CreateVotePayload{
			TxHash:    firstSplitTxId,
			PollID:    conf.PollID,
			Voter:     conf.FromAddresses[0],
			Votes:     conf.Votes,
			Weight:    1.0,
			Timestamp: time.Now().Unix(),
		}

		vote, err := CreateVote(ctx, nil, payload)
		if err != nil {
			log.Fatalf("Error submitting vote: %v", err)
		}

		fmt.Printf("Vote created successfully! ID: %s\n", vote.ID)
		break
	}

	return nil
}
