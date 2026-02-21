package server

import (
	"context"
	"fmt"
	"slices"

	"github.com/Hoosat-Oy/HTND/cmd/htnwallet/daemon/pb"
	"github.com/Hoosat-Oy/HTND/cmd/htnwallet/libhtnwallet"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/constants"
	"github.com/Hoosat-Oy/HTND/util"
	"github.com/pkg/errors"
)

// TODO: Implement a better fee estimation mechanism
const feePerInput = 10000

// The minimal change amount to target in order to avoid large storage mass (see KIP9 for more details).
// By having at least 0.2KAS in the change output we make sure that every transaction with send value >= 0.2KAS
// should succeed (at most 50K storage mass for each output, thus overall lower than standard mass upper bound which is 100K gram)
const minChangeTarget = constants.SompiPerHoosat / 5

func (s *server) CreateUnsignedTransactions(_ context.Context, request *pb.CreateUnsignedTransactionsRequest) (
	*pb.CreateUnsignedTransactionsResponse, error,
) {
	s.lock.Lock()
	defer s.lock.Unlock()

	unsignedTransactions, err := s.createUnsignedTransactions(request.Address, request.Amount, request.IsSendAll,
		request.From, request.UseExistingChangeAddress, request.Payload)
	if err != nil {
		return nil, err
	}

	return &pb.CreateUnsignedTransactionsResponse{UnsignedTransactions: unsignedTransactions}, nil
}

func (s *server) CreateUnsignedCompoundTransaction(_ context.Context, request *pb.CreateUnsignedCompoundTransactionRequest) (
	*pb.CreateUnsignedCompoundTransactionResponse, error,
) {
	s.lock.Lock()
	defer s.lock.Unlock()

	unsignedTransactions, err := s.createUnsignedCompoundTransaction(request.Address, request.From, request.UseExistingChangeAddress)
	if err != nil {
		return nil, err
	}

	return &pb.CreateUnsignedCompoundTransactionResponse{UnsignedTransactions: unsignedTransactions}, nil
}

func (s *server) createUnsignedCompoundTransaction(address string, fromAddressesString []string, useExistingChangeAddress bool) ([][]byte, error) {
	if !s.isSynced() {
		return nil, errors.Errorf("wallet daemon is not synced yet, %s", s.formatSyncStateReport())
	}

	err := s.refreshUTXOs()
	if err != nil {
		return nil, err
	}

	toAddress, err := util.DecodeAddress(address, s.params.Prefix)
	if err != nil {
		return nil, err
	}

	var fromAddresses []*walletAddress
	for _, from := range fromAddressesString {
		fromAddress, exists := s.addressSet[from]
		if !exists {
			return nil, fmt.Errorf("specified from address %s does not exists", from)
		}
		fromAddresses = append(fromAddresses, fromAddress)
	}

	selectedUTXOs, _, changeSompi, err := s.selectCompoundUTXOs(feePerInput, fromAddresses)
	if err != nil {
		return nil, err
	}

	if len(selectedUTXOs) == 0 {
		return nil, errors.Errorf("couldn't find funds to spend")
	}

	changeAddress, changeWalletAddress, err := s.changeAddress(useExistingChangeAddress, fromAddresses)
	if err != nil {
		return nil, err
	}

	// For compounding we want to consolidate inputs into a single output.
	// Send the net amount (after fees) to the requested address and avoid creating
	// an additional change output to keep base mass low and prevent dust.
	// Note: changeAddress is still used by maybeAutoCompoundTransaction for split/merge flows.
	payments := []*libhtnwallet.Payment{{
		Address: toAddress,
		Amount:  changeSompi,
	}}
	unsignedTransaction, err := libhtnwallet.CreateUnsignedTransaction(s.keysFile.ExtendedPublicKeys,
		s.keysFile.MinimumSignatures,
		payments, selectedUTXOs, nil)
	if err != nil {
		return nil, err
	}

	unsignedTransactions, err := s.maybeAutoCompoundTransaction(unsignedTransaction, toAddress, changeAddress, changeWalletAddress)
	if err != nil {
		return nil, err
	}
	return unsignedTransactions, nil
}

// Add this constant next to your others
var targetCompoundInputs = 88

func (s *server) selectCompoundUTXOs(feePerInput int, fromAddresses []*walletAddress) (
	selectedUTXOs []*libhtnwallet.UTXO, totalReceived uint64, changeSompi uint64, err error) {

	selectedUTXOs = make([]*libhtnwallet.UTXO, 0, targetCompoundInputs)
	var totalValue uint64

	dagInfo, err := s.rpcClient.GetBlockDAGInfo()
	if err != nil {
		return nil, 0, 0, errors.Wrap(err, "failed to get DAG info")
	}

	s.sortUTXOsByAmountDescending()
	for _, highestUTXO := range s.utxosSortedByAmount {
		if len(selectedUTXOs) >= 1 {
			break
		}
		if (fromAddresses != nil && !walletAddressesContain(fromAddresses, highestUTXO.address)) ||
			!s.isUTXOSpendable(highestUTXO, dagInfo.VirtualDAAScore) {
			continue
		}

		if broadcastTime, ok := s.usedOutpoints[*highestUTXO.Outpoint]; ok {
			if s.usedOutpointHasExpired(broadcastTime) {
				delete(s.usedOutpoints, *highestUTXO.Outpoint)
			} else {
				continue
			}
		}

		selectedUTXOs = append(selectedUTXOs, &libhtnwallet.UTXO{
			Outpoint:       highestUTXO.Outpoint,
			UTXOEntry:      highestUTXO.UTXOEntry,
			DerivationPath: s.walletAddressPath(highestUTXO.address),
		})
		totalValue += highestUTXO.UTXOEntry.Amount()
	}
	log.Infof("Selected %d big UTXO for compound", totalValue/100_000_000)

	s.sortUTXOsByAmountAscending()
	log.Infof("Found %d UTXO", len(s.utxosSortedByAmount))

	// Step 1: Collect up to targetCompoundInputs smallest spendable UTXOs
	for _, utxo := range s.utxosSortedByAmount {
		if selectedUTXOsContain(selectedUTXOs, utxo) { // Don't accidentally spend same UTXO.
			continue
		}
		if len(selectedUTXOs) >= targetCompoundInputs {
			break
		}
		if (fromAddresses != nil && !walletAddressesContain(fromAddresses, utxo.address)) ||
			!s.isUTXOSpendable(utxo, dagInfo.VirtualDAAScore) ||
			utxo.UTXOEntry.BlockDAAScore() == 0 ||
			utxo.UTXOEntry.BlockDAAScore()+1 > dagInfo.VirtualDAAScore {
			continue
		}

		if broadcastTime, ok := s.usedOutpoints[*utxo.Outpoint]; ok {
			if s.usedOutpointHasExpired(broadcastTime) {
				delete(s.usedOutpoints, *utxo.Outpoint)
			} else {
				continue
			}
		}

		selectedUTXOs = append(selectedUTXOs, &libhtnwallet.UTXO{
			Outpoint:       utxo.Outpoint,
			UTXOEntry:      utxo.UTXOEntry,
			DerivationPath: s.walletAddressPath(utxo.address),
		})
		totalValue += utxo.UTXOEntry.Amount()
	}
	log.Infof("Selected %d UTXO", len(s.utxosSortedByAmount))

	if len(selectedUTXOs) == 0 {
		return nil, 0, 0, errors.New("no spendable UTXOs for compounding")
	}

	// Calculate fees based on the actual number of selected inputs
	fee := uint64(len(selectedUTXOs)) * uint64(feePerInput)
	if totalValue <= fee {
		return nil, 0, 0, errors.Errorf("not enough funds: total %d sompi < fee %d sompi", totalValue, fee)
	}

	changeSompi = totalValue - fee

	return selectedUTXOs, totalValue, changeSompi, nil
}

func (s *server) createUnsignedTransactions(address string, amount uint64, isSendAll bool, fromAddressesString []string, useExistingChangeAddress bool, payload []byte) ([][]byte, error) {
	if !s.isSynced() {
		return nil, errors.Errorf("wallet daemon is not synced yet, %s", s.formatSyncStateReport())
	}

	err := s.refreshUTXOs()
	if err != nil {
		return nil, err
	}

	// make sure address string is correct before proceeding to a
	// potentially long UTXO refreshment operation
	toAddress, err := util.DecodeAddress(address, s.params.Prefix)
	if err != nil {
		return nil, err
	}

	var fromAddresses []*walletAddress
	for _, from := range fromAddressesString {
		fromAddress, exists := s.addressSet[from]
		if !exists {
			return nil, fmt.Errorf("specified from address %s does not exists", from)
		}
		fromAddresses = append(fromAddresses, fromAddress)
	}

	selectedUTXOs, spendValue, changeSompi, err := s.selectUTXOs(amount, isSendAll, feePerInput, fromAddresses)
	if err != nil {
		return nil, err
	}

	if len(selectedUTXOs) == 0 {
		return nil, errors.Errorf("couldn't find funds to spend")
	}

	changeAddress, changeWalletAddress, err := s.changeAddress(useExistingChangeAddress, fromAddresses)
	if err != nil {
		return nil, err
	}

	payments := []*libhtnwallet.Payment{{
		Address: toAddress,
		Amount:  spendValue,
	}}
	if changeSompi > 0 {
		payments = append(payments, &libhtnwallet.Payment{
			Address: changeAddress,
			Amount:  changeSompi,
		})
	}
	unsignedTransaction, err := libhtnwallet.CreateUnsignedTransaction(s.keysFile.ExtendedPublicKeys,
		s.keysFile.MinimumSignatures,
		payments, selectedUTXOs, payload)
	if err != nil {
		return nil, err
	}

	unsignedTransactions, err := s.maybeAutoCompoundTransaction(unsignedTransaction, toAddress, changeAddress, changeWalletAddress)
	if err != nil {
		return nil, err
	}
	return unsignedTransactions, nil
}

func (s *server) sortUTXOsByAmountAscending() {
	slices.SortStableFunc(s.utxosSortedByAmount, func(a, b *walletUTXO) int {
		switch {
		case a.UTXOEntry.Amount() < b.UTXOEntry.Amount():
			return -1
		case a.UTXOEntry.Amount() > b.UTXOEntry.Amount():
			return 1
		default:
			return 0
		}
	})
}

func (s *server) sortUTXOsByAmountDescending() {
	slices.SortStableFunc(s.utxosSortedByAmount, func(a, b *walletUTXO) int {
		switch {
		case a.UTXOEntry.Amount() < b.UTXOEntry.Amount():
			return 1
		case a.UTXOEntry.Amount() > b.UTXOEntry.Amount():
			return -1
		default:
			return 0
		}
	})
}

func (s *server) selectUTXOs(spendAmount uint64, isSendAll bool, feePerInput uint64, fromAddresses []*walletAddress) (
	selectedUTXOs []*libhtnwallet.UTXO, totalReceived uint64, changeSompi uint64, err error) {

	selectedUTXOs = []*libhtnwallet.UTXO{}
	totalValue := uint64(0)

	dagInfo, err := s.rpcClient.GetBlockDAGInfo()
	if err != nil {
		return nil, 0, 0, err
	}

	s.sortUTXOsByAmountDescending()

	for _, utxo := range s.utxosSortedByAmount {
		if (fromAddresses != nil && !walletAddressesContain(fromAddresses, utxo.address)) ||
			!s.isUTXOSpendable(utxo, dagInfo.VirtualDAAScore) ||
			utxo.UTXOEntry.BlockDAAScore() == 0 ||
			utxo.UTXOEntry.BlockDAAScore()+1 > dagInfo.VirtualDAAScore {
			continue
		}

		if broadcastTime, ok := s.usedOutpoints[*utxo.Outpoint]; ok {
			if s.usedOutpointHasExpired(broadcastTime) {
				delete(s.usedOutpoints, *utxo.Outpoint)
			} else {
				continue
			}
		}

		selectedUTXOs = append(selectedUTXOs, &libhtnwallet.UTXO{
			Outpoint:       utxo.Outpoint,
			UTXOEntry:      utxo.UTXOEntry,
			DerivationPath: s.walletAddressPath(utxo.address),
		})

		totalValue += utxo.UTXOEntry.Amount()

		fee := feePerInput * uint64(len(selectedUTXOs))
		totalSpend := spendAmount + fee
		// Two break cases (if not send all):
		// 		1. totalValue == totalSpend, so there's no change needed -> number of outputs = 1, so a single input is sufficient
		// 		2. totalValue > totalSpend, so there will be change and 2 outputs, therefor in order to not struggle with --
		//		   2.1 go-nodes dust patch we try and find at least 2 inputs (even though the next one is not necessary in terms of spend value)
		// 		   2.2 KIP9 we try and make sure that the change amount is not too small
		if !isSendAll && (totalValue == totalSpend || (totalValue >= totalSpend+minChangeTarget && len(selectedUTXOs) > 1)) {
			break
		}
	}

	fee := feePerInput * uint64(len(selectedUTXOs))
	var totalSpend uint64
	if isSendAll {
		totalSpend = totalValue
		totalReceived = totalValue - fee
	} else {
		totalSpend = spendAmount + fee
		totalReceived = spendAmount
	}
	if totalValue < totalSpend {
		return nil, 0, 0, errors.Errorf("Insufficient funds for send: %f required, while only %f available",
			float64(totalSpend)/constants.SompiPerHoosat, float64(totalValue)/constants.SompiPerHoosat)
	}

	return selectedUTXOs, totalReceived, totalValue - totalSpend, nil
}

func walletAddressesContain(addresses []*walletAddress, contain *walletAddress) bool {
	for _, address := range addresses {
		if *address == *contain {
			return true
		}
	}

	return false
}

// selectedUTXOsContain checks if a given walletUTXO is already present in the selectedUTXOs slice.
func selectedUTXOsContain(selectedUTXOs []*libhtnwallet.UTXO, utxo *walletUTXO) bool {
	for _, s := range selectedUTXOs {
		if s.Outpoint != nil && utxo.Outpoint != nil && *s.Outpoint == *utxo.Outpoint {
			return true
		}
	}
	return false
}
