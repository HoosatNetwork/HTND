package server

import (
	"context"

	"github.com/pkg/errors"

	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/daemon/pb"
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

// coinbaseReorgSafetyMargin is how many DAA score units, beyond consensus coinbase maturity, the wallet
// waits before it spends a coinbase output.
//
// A coinbase output exists only while the block that pays it stays on the selected chain. When virtual
// reorgs to a sibling chain, the coinbase outputs of the blocks it leaves are not accepted by the new
// chain, and they vanish from the virtual UTXO set. Ordinary transactions from those blocks are merged
// by the new chain and survive; coinbase outputs are the only coins a reorg deletes outright.
//
// Consensus maturity alone does not cover that on mainnet. It is 100 DAA score units, roughly ten
// seconds at the current block rate, and reorgs that deep happen: on 29.9.2026 two chains ran in
// parallel from DAA 231080602 until about 231080711. The node's virtual followed the chain through
// 35b4eeaf for about ten seconds, during which the coinbase of 0d44a4cf (tx 6b54377120…) was in the
// virtual UTXO set and passed the maturity check. A compound transaction (34195391…) spent two of its
// outputs, stamped at DAA 231080611, at DAA 231080716 - five units past maturity - and was mined on
// that chain. Virtual then moved to the other chain, and those outputs never existed on it.
//
// 1000 units is about one and a half to three minutes. It delays spending a mining reward by that
// much, and it keeps the wallet from handing out coins that a reorg of ten times the observed depth
// could still delete.
const coinbaseReorgSafetyMargin = 1000

// isCoinbaseSafelyMature reports whether a coinbase output stamped with blockDAAScore is mature by
// consensus rules plus coinbaseReorgSafetyMargin at virtualDAAScore.
func isCoinbaseSafelyMature(blockDAAScore, coinbaseMaturity, virtualDAAScore uint64) bool {
	return blockDAAScore+coinbaseMaturity+coinbaseReorgSafetyMargin < virtualDAAScore
}

func (s *server) isUTXOSpendable(entry *walletUTXO, virtualDAAScore uint64) bool {
	if entry.UTXOEntry.BlockDAAScore() == 0 || entry.UTXOEntry.BlockDAAScore()+1 > virtualDAAScore {
		return false
	}
	if !entry.UTXOEntry.IsCoinbase() {
		return true
	}
	return isCoinbaseSafelyMature(entry.UTXOEntry.BlockDAAScore(), s.coinbaseMaturity, virtualDAAScore)
}
