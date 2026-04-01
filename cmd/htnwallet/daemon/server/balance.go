package server

import (
	"context"

	"github.com/pkg/errors"

	"github.com/Hoosat-Oy/HTND/cmd/htnwallet/daemon/pb"
)

type (
	balancesType    struct{ available, pending uint64 }
	balancesMapType map[string]*balancesType
)

func (s *server) GetBalance(_ context.Context, _ *pb.GetBalanceRequest) (*pb.GetBalanceResponse, error) {
	s.lock.RLock()
	defer s.lock.RUnlock()

	if !s.isSynced() {
		return nil, errors.Errorf("wallet daemon is not synced yet, %s", s.formatSyncStateReport())
	}

	getBalancesByAddressesResponse, err := s.backgroundRPCClient.GetBalancesByAddresses(s.addressSet.strings())
	if err != nil {
		return nil, err
	}

	balancesMap := make(balancesMapType, 0)
	for _, entry := range getBalancesByAddressesResponse.Entries {
		amount := entry.Balance
		address := entry.Address
		balances, ok := balancesMap[address]
		if !ok {
			balances = new(balancesType)
			balancesMap[address] = balances
		}
		balances.available += amount
	}

	addressBalances := make([]*pb.AddressBalances, len(balancesMap))
	i := 0
	var available, pending uint64
	for walletAddress, balances := range balancesMap {
		addressBalances[i] = &pb.AddressBalances{
			Address:   walletAddress,
			Available: balances.available,
			Pending:   balances.pending,
		}
		i++
		available += balances.available
		pending += balances.pending
	}

	log.Infof("GetBalance request scanned over %d addresses", len(balancesMap))

	return &pb.GetBalanceResponse{
		Available:       available,
		Pending:         pending,
		AddressBalances: addressBalances,
	}, nil
}

func (s *server) isUTXOSpendable(entry *walletUTXO, virtualDAAScore uint64) bool {
	if entry.UTXOEntry.BlockDAAScore() == 0 || entry.UTXOEntry.BlockDAAScore()+1 > virtualDAAScore {
		return false
	}
	if !entry.UTXOEntry.IsCoinbase() {
		return true
	}
	return entry.UTXOEntry.BlockDAAScore()+s.coinbaseMaturity < virtualDAAScore
}
