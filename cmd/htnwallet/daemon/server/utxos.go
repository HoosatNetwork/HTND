package server

import (
	"context"

	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/daemon/pb"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/libhtnwallet"
	"github.com/pkg/errors"
)

// GetUTXOs lists the coins on every address the daemon tracks, each with the address and the path that
// address is derived at. It reads the same addresses as GetBalance, so the two always agree.
func (s *server) GetUTXOs(_ context.Context, _ *pb.GetUTXOsRequest) (*pb.GetUTXOsResponse, error) {
	s.lock.RLock()
	defer s.lock.RUnlock()

	if !s.isSynced() {
		return nil, errors.Errorf("wallet daemon is not synced yet, %s", s.formatSyncStateReport())
	}

	getUTXOsByAddressesResponse, err := s.rpcClient.GetUTXOsByAddresses(s.addressSet.strings(), 0)
	if err != nil {
		return nil, err
	}
	utxos, err := s.walletUTXOs(getUTXOsByAddressesResponse.Entries)
	if err != nil {
		return nil, err
	}
	return &pb.GetUTXOsResponse{Utxos: utxos}, nil
}

// walletUTXOs labels each coin the node returned with the derivation path of its address. The caller
// holds s.lock.
func (s *server) walletUTXOs(entries []*appmessage.UTXOsByAddressesEntry) ([]*pb.WalletUtxo, error) {
	utxos := make([]*pb.WalletUtxo, 0, len(entries))
	for _, entry := range entries {
		address, ok := s.addressSet[entry.Address]
		if !ok {
			return nil, errors.Errorf("Got result from address %s even though it wasn't requested", entry.Address)
		}
		// The converter leaves the address out, so it comes from the node's entry.
		converted := libhtnwallet.AppMessageUTXOToHoosatwalletdUTXO(entry)
		utxos = append(utxos, &pb.WalletUtxo{
			Address:        entry.Address,
			Outpoint:       converted.Outpoint,
			UtxoEntry:      converted.UtxoEntry,
			DerivationPath: s.walletAddressLabel(address),
		})
	}
	return utxos, nil
}
