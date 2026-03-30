package server

import (
	"context"
	"strconv"

	"github.com/Hoosat-Oy/HTND/cmd/htnwallet/daemon/pb"
	"github.com/pkg/errors"
)

func (s *server) Send(_ context.Context, request *pb.SendRequest) (*pb.SendResponse, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	limit := uint32(10000)
	if request.GetLimit() != "" {
		limit64, err := strconv.ParseUint(request.GetLimit(), 10, 32)
		if err != nil {
			return nil, errors.Errorf("invalid limit: %s", request.GetLimit())
		}
		limit = uint32(limit64)
	}

	unsignedTransactions, err := s.createUnsignedTransactions(request.ToAddress, request.Amount, request.IsSendAll,
		request.From, request.UseExistingChangeAddress, nil, limit)
	if err != nil {
		return nil, err
	}

	signedTransactions, err := s.signTransactions(unsignedTransactions, request.Password)
	if err != nil {
		return nil, err
	}

	txIDs, err := s.broadcast(signedTransactions, false, false, nil)
	if err != nil {
		return nil, err
	}

	return &pb.SendResponse{TxIDs: txIDs, SignedTransactions: signedTransactions}, nil
}
