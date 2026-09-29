package server

import (
	"context"

	"github.com/pkg/errors"

	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/daemon/pb"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/constants"
)

type (
	balancesType    struct{ available, pending uint64 }
	balancesMapType map[string]*balancesType
)

// GetBalance reports each address's balance split into available coins, which the wallet will spend,
// and pending ones, which are not old enough yet (see isInputSafelyAged). The total per address comes
// from the node's balance index; the pending part is worked out from the address's UTXOs.
func (s *server) GetBalance(_ context.Context, _ *pb.GetBalanceRequest) (*pb.GetBalanceResponse, error) {
	s.lock.RLock()
	defer s.lock.RUnlock()

	if !s.isSynced() {
		return nil, errors.Errorf("wallet daemon is not synced yet, %s", s.formatSyncStateReport())
	}

	addresses := s.addressSet.strings()
	getBalancesByAddressesResponse, err := s.backgroundRPCClient.GetBalancesByAddresses(addresses)
	if err != nil {
		return nil, err
	}

	balancesMap := make(balancesMapType, 0)
	for _, entry := range getBalancesByAddressesResponse.Entries {
		balances, ok := balancesMap[entry.Address]
		if !ok {
			balances = new(balancesType)
			balancesMap[entry.Address] = balances
		}
		balances.available += entry.Balance
	}

	if len(balancesMap) > 0 {
		dagInfo, err := s.rpcClient.GetBlockDAGInfo()
		if err != nil {
			return nil, err
		}
		getUTXOsByAddressesResponse, err := s.rpcClient.GetUTXOsByAddresses(addresses, 0)
		if err != nil {
			return nil, err
		}
		err = s.movePendingOutOfAvailable(balancesMap, getUTXOsByAddressesResponse.Entries, dagInfo.VirtualDAAScore)
		if err != nil {
			return nil, err
		}
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

// movePendingOutOfAvailable moves the value of every coin the wallet may not spend yet from its
// address's available balance to its pending one.
func (s *server) movePendingOutOfAvailable(balancesMap balancesMapType, entries []*appmessage.UTXOsByAddressesEntry,
	virtualDAAScore uint64,
) error {
	for _, entry := range entries {
		utxoEntry, err := appmessage.RPCUTXOEntryToUTXOEntry(entry.UTXOEntry)
		if err != nil {
			return err
		}
		if isInputSafelyAged(utxoEntry.BlockDAAScore(), utxoEntry.IsCoinbase(), s.coinbaseMaturity, virtualDAAScore) {
			continue
		}
		balances, ok := balancesMap[entry.Address]
		if !ok {
			continue
		}
		moved := min(utxoEntry.Amount(), balances.available)
		balances.available -= moved
		balances.pending += moved
	}
	return nil
}

// inputMinAgeDAAScore is how many DAA score units old a coin must be before the wallet spends it -
// counted after consensus coinbase maturity for a coinbase output. The node's mempool refuses to accept
// or relay a transaction with a younger input (mempool.Config.InputMinAgeDAAScore, same default), and
// refuses any transaction spending an output of an unconfirmed one, which this wallet therefore never
// builds.
//
// A coin exists only while the chain that accepted it stays selected. When virtual reorgs to a sibling
// chain, the coinbase outputs of the blocks it leaves are not accepted by the new chain and vanish, and
// so does everything built on them. Consensus maturity, 100 DAA score units on mainnet or roughly ten
// seconds, does not cover that: on 29.9.2026 two chains ran in parallel from DAA 231080602 until about
// 231080711. The node's virtual followed the chain through 35b4eeaf for about ten seconds, during which
// the coinbase of 0d44a4cf (tx 6b54377120…) was in the virtual UTXO set and passed the maturity check. A
// compound transaction (34195391…) spent two of its outputs, stamped at DAA 231080611, at DAA 231080716 -
// five units past maturity - and was mined on that chain. Virtual then moved to the other chain, and
// those outputs never existed on it.
//
// 1000 units is about one and a half to three minutes, ten times the depth of the reorg observed.
const inputMinAgeDAAScore = 1000

// isInputSafelyAged reports whether a coin stamped with blockDAAScore may be spent at virtualDAAScore:
// it must be strictly more than inputMinAgeDAAScore old (plus coinbaseMaturity for a coinbase output),
// one unit more than the node's mempool requires, so that a transaction built here is still accepted
// if virtual has not moved on by the time it arrives. A coin that has not been accepted by a block has
// no age and is never spendable.
func isInputSafelyAged(blockDAAScore uint64, isCoinbase bool, coinbaseMaturity, virtualDAAScore uint64) bool {
	if blockDAAScore == 0 || blockDAAScore == constants.UnacceptedDAAScore {
		return false
	}
	required := blockDAAScore + inputMinAgeDAAScore
	if isCoinbase {
		required += coinbaseMaturity
	}
	return required < virtualDAAScore
}

func (s *server) isUTXOSpendable(entry *walletUTXO, virtualDAAScore uint64) bool {
	return isInputSafelyAged(entry.UTXOEntry.BlockDAAScore(), entry.UTXOEntry.IsCoinbase(), s.coinbaseMaturity,
		virtualDAAScore)
}
