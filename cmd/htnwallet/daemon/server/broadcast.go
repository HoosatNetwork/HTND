package server

import (
	"context"
	"strings"
	"time"

	"github.com/Hoosat-Oy/HTND/app/appmessage"
	"github.com/Hoosat-Oy/HTND/cmd/htnwallet/daemon/pb"
	"github.com/Hoosat-Oy/HTND/cmd/htnwallet/libhtnwallet"
	"github.com/Hoosat-Oy/HTND/cmd/htnwallet/libhtnwallet/serialization"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/consensushashing"
	"github.com/Hoosat-Oy/HTND/infrastructure/network/rpcclient"
	"github.com/pkg/errors"
)

func (s *server) Broadcast(_ context.Context, request *pb.BroadcastRequest) (*pb.BroadcastResponse, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	txIDs, err := s.broadcast(request.Transactions, request.IsDomain, request.AllowOrphan, request.IsHighPriority)
	if err != nil {
		return nil, err
	}

	return &pb.BroadcastResponse{TxIDs: txIDs}, nil
}

func (s *server) broadcast(transactions [][]byte, isDomain bool, allowOrphan bool, isHighPriority *bool) ([]string, error) {

	txIDs := make([]string, len(transactions))
	var tx *externalapi.DomainTransaction
	var err error

	for i, transaction := range transactions {

		if isDomain {
			tx, err = serialization.DeserializeDomainTransaction(transaction)
			if err != nil {
				return nil, err
			}
		} else if !isDomain { //default in proto3 is false
			tx, err = libhtnwallet.ExtractTransaction(transaction, s.keysFile.ECDSA)
			if err != nil {
				return nil, err
			}
		}

		txIDs[i], err = sendTransaction(s.rpcClient, tx, allowOrphan, isHighPriority)
		if err != nil {
			if shouldReleaseUsedOutpointsOnBroadcastError(err) {
				s.releaseUsedOutpoints(tx)
			}
			return nil, err
		}

		for _, input := range tx.Inputs {
			s.usedOutpoints[input.PreviousOutpoint] = time.Now()
		}
	}

	s.forceSync()
	return txIDs, nil
}

func shouldReleaseUsedOutpointsOnBroadcastError(err error) bool {
	if err == nil {
		return false
	}

	errString := strings.ToLower(err.Error())
	return strings.Contains(errString, "rejected transaction") ||
		strings.Contains(errString, "already spent by transaction") ||
		strings.Contains(errString, "compound transaction rate limit exceeded")
}

func (s *server) releaseUsedOutpoints(tx *externalapi.DomainTransaction) {
	if tx == nil {
		return
	}

	for _, input := range tx.Inputs {
		delete(s.usedOutpoints, input.PreviousOutpoint)
	}
}

func sendTransaction(client *rpcclient.RPCClient, tx *externalapi.DomainTransaction, allowOrphan bool, isHighPriority *bool) (string, error) {
	submitTransactionResponse, err := client.SubmitTransactionWithPriority(appmessage.DomainTransactionToRPCTransaction(tx), consensushashing.TransactionID(tx).String(), allowOrphan, isHighPriority)
	if err != nil {
		return "", errors.Wrapf(err, "error submitting transaction")
	}
	return submitTransactionResponse.TransactionID, nil
}
