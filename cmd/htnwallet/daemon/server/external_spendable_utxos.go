package server

import (
	"context"

	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/daemon/pb"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/libhtnwallet"
	"github.com/HoosatNetwork/HTND/v2/util"
)

func (s *server) GetExternalSpendableUTXOs(_ context.Context, request *pb.GetExternalSpendableUTXOsRequest) (*pb.GetExternalSpendableUTXOsResponse, error) {
	s.lock.RLock()
	defer s.lock.RUnlock()

	_, err := util.DecodeAddress(request.Address, s.params.Prefix)
	if err != nil {
		return nil, err
	}
	externalUTXOs, err := s.rpcClient.GetUTXOsByAddresses([]string{request.Address}, 0)
	if err != nil {
		return nil, err
	}
	selectedUTXOs, err := s.selectExternalSpendableUTXOs(externalUTXOs, request.Address)
	if err != nil {
		return nil, err
	}
	return &pb.GetExternalSpendableUTXOsResponse{
		Entries: selectedUTXOs,
	}, nil
}

func (s *server) selectExternalSpendableUTXOs(externalUTXOs *appmessage.GetUTXOsByAddressesResponseMessage, _ string) ([]*pb.UtxosByAddressesEntry, error) {
	dagInfo, err := s.rpcClient.GetBlockDAGInfo()
	if err != nil {
		return nil, err
	}

	daaScore := dagInfo.VirtualDAAScore
	maturity := s.params.BlockCoinbaseMaturity

	// we do not make because we do not know size, because of unspendable utxos
	var selectedExternalUtxos []*pb.UtxosByAddressesEntry

	for _, entry := range externalUTXOs.Entries {
		if !isExternalUTXOSpendable(entry, daaScore, maturity) {
			continue
		}
		selectedExternalUtxos = append(selectedExternalUtxos, libhtnwallet.AppMessageUTXOToHoosatwalletdUTXO(entry))
	}

	return selectedExternalUtxos, nil
}

// isExternalUTXOSpendable applies the wallet's age rule (isInputSafelyAged) to a coin of an address the
// wallet does not hold the keys of, and skips coinbase outputs worth no more than their fee.
func isExternalUTXOSpendable(entry *appmessage.UTXOsByAddressesEntry, virtualDAAScore uint64, coinbaseMaturity uint64) bool {
	if entry.UTXOEntry.IsCoinbase && entry.UTXOEntry.Amount <= feePerInput {
		return false
	}
<<<<<<< HEAD
	return entry.UTXOEntry.BlockDAAScore+coinbaseMaturity < virtualDAAScore
=======
	return isInputSafelyAged(entry.UTXOEntry.BlockDAAScore, entry.UTXOEntry.IsCoinbase, coinbaseMaturity, virtualDAAScore)
>>>>>>> df732b44d (feat(mempool,htnwallet): require every input to be at least 1000 DAA old)
}
