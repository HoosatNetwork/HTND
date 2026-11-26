package server

import (
	"context"
	"strconv"

	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/daemon/pb"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/libhtnwallet"
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

	send, err := s.createUnsignedTransactions(request.ToAddress, request.Amount, request.IsSendAll,
		request.From, request.UseExistingChangeAddress, nil, limit)
	if err != nil {
		return nil, err
	}

	signedTransactions, err := s.signTransactions([][]byte{send.transaction}, request.Password)
	if err != nil {
		return nil, err
	}

	tx, err := libhtnwallet.ExtractTransaction(signedTransactions[0], s.keysFile.ECDSA)
	if err != nil {
		return nil, err
	}
	// A failed reply can leave delivery uncertain, so keep the reservation from submission onward.
	if err := s.commitChange(send.change); err != nil {
		return nil, err
	}
	txID, err := s.submitTransaction(tx, false, nil)
	if err != nil {
		return nil, err
	}

	s.forceSync()
	return &pb.SendResponse{TxIDs: []string{txID}, SignedTransactions: signedTransactions}, nil
}
