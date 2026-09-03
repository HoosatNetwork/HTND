package server

import (
	"context"
	"slices"
	"strconv"
	"time"

	"github.com/HoosatNetwork/HTND/cmd/htnwallet/daemon/pb"
	"github.com/HoosatNetwork/HTND/cmd/htnwallet/libhtnwallet"
	"github.com/HoosatNetwork/HTND/cmd/htnwallet/libhtnwallet/serialization"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/domain/miningmanager/mempool"
	"github.com/HoosatNetwork/HTND/util"
	"github.com/pkg/errors"
)

// TODO: Implement a better fee estimation mechanism
const feePerInput = 10000

func checkedUint64FromInt(value int) (uint64, error) {
	parsedValue, err := strconv.ParseUint(strconv.Itoa(value), 10, 64)
	if err != nil {
		return 0, err
	}
	return parsedValue, nil
}

func (s *server) CreateUnsignedTransactions(_ context.Context, request *pb.CreateUnsignedTransactionsRequest) (
	*pb.CreateUnsignedTransactionsResponse, error,
) {
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

	unsignedTransactions, err := s.createUnsignedTransactions(request.Address, request.Amount, request.IsSendAll,
		request.From, request.UseExistingChangeAddress, request.Payload, limit)
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

	limit := uint32(10000)
	if request.GetLimit() != "" {
		limit64, err := strconv.ParseUint(request.GetLimit(), 10, 32)
		if err != nil {
			return nil, errors.Errorf("invalid limit: %s", request.GetLimit())
		}
		limit = uint32(limit64)
	}

	unsignedTransactions, err := s.createUnsignedCompoundTransaction(request.Address, request.From, request.UseExistingChangeAddress, limit)
	if err != nil {
		return nil, err
	}

	return &pb.CreateUnsignedCompoundTransactionResponse{UnsignedTransactions: unsignedTransactions}, nil
}

func (s *server) createUnsignedCompoundTransaction(address string, fromAddressesString []string, useExistingChangeAddress bool, limit uint32) ([][]byte, error) {
	if !s.isSynced() {
		return nil, errors.Errorf("wallet daemon is not synced yet, %s", s.formatSyncStateReport())
	}

	err := s.refreshUTXOs(limit)
	if err != nil {
		return nil, err
	}
	log.Infof("Fetched %d UTXO from the Node", len(s.utxosSortedByAmount))

	toAddress, err := util.DecodeAddress(address, s.params.Prefix)
	if err != nil {
		return nil, err
	}

	var fromAddresses []*walletAddress
	for _, from := range fromAddressesString {
		fromAddress, exists := s.addressSet[from]
		if !exists {
			return nil, errors.Errorf("specified from address %s does not exists", from)
		}
		fromAddresses = append(fromAddresses, fromAddress)
	}

	selectedUTXOs, _, _, err := s.selectUTXOsForCompounding(feePerInput, fromAddresses)
	if err != nil {
		return nil, err
	}

	if len(selectedUTXOs) < 2 {
		return nil, errors.Errorf("nothing to compound")
	}

	changeAddress, changeWalletAddress, err := s.changeAddress(useExistingChangeAddress, fromAddresses)
	if err != nil {
		return nil, err
	}

	// For compounding we want to consolidate inputs into a single output.
	// Send the net amount (after fees) to the requested address and avoid creating
	// an additional change output to keep base mass low and prevent dust.
	// Note: changeAddress is still used by maybeAutoCompoundTransaction for split/merge flows.
	unsignedTransaction, spentUTXOs, err := s.buildCompoundTransactionWithinStandardMass(selectedUTXOs, toAddress)
	if err != nil {
		return nil, err
	}

	// Mark the spent UTXOs as used to prevent respending in case of submission failure. Only the ones
	// the transaction actually spends: buildCompoundTransactionWithinStandardMass may have dropped
	// some to fit the mass limit, and marking those would sideline them until the used-outpoint
	// expiry even though nothing ever spent them.
	for _, spentUTXO := range spentUTXOs {
		s.usedOutpoints[*spentUTXO.Outpoint] = time.Now()
	}

	unsignedTransactions, err := s.maybeAutoCompoundTransaction(unsignedTransaction, toAddress, changeAddress, changeWalletAddress)
	if err != nil {
		return nil, err
	}
	return unsignedTransactions, nil
}

// Add this constant next to your others
var targetCompoundInputs = 88

// buildCompoundTransactionWithinStandardMass builds the compound transaction, dropping inputs until
// its signed mass fits MaximumStandardTransactionMass.
//
// targetCompoundInputs is a fixed count, and it was tuned against the P2PK signature script (a bare
// 66-byte push of the signature). Other input types are bigger - a P2PKH input also carries its
// public key (99 bytes), a P2SH input carries the public key and the redeem script (137) - so the
// same 88 inputs that measure 98890 mass from a P2PK wallet measure 101794 from a P2PKH one and
// 105138 from a P2SH one, against a 100000 limit. Going over is what pushed those wallets into
// maybeSplitAndMergeTransaction, whose split transactions pay the change address rather than the
// requested destination.
//
// Sizing by measured mass instead of by count keeps a compound a single transaction paying the
// destination directly, whatever the inputs look like, rather than depending on a constant that only
// happens to hold for one address type.
func (s *server) buildCompoundTransactionWithinStandardMass(selectedUTXOs []*libhtnwallet.UTXO,
	toAddress util.Address,
) ([]byte, []*libhtnwallet.UTXO, error) {
	for {
		if len(selectedUTXOs) < 2 {
			return nil, nil, errors.Errorf("not enough inputs fit within the standard transaction mass to compound")
		}

		selectedCount, err := checkedUint64FromInt(len(selectedUTXOs))
		if err != nil {
			return nil, nil, err
		}
		totalValue := uint64(0)
		for _, selectedUTXO := range selectedUTXOs {
			totalValue += selectedUTXO.UTXOEntry.Amount()
		}
		fee := selectedCount * feePerInput
		if totalValue <= fee {
			return nil, nil, errors.Errorf("not enough funds: total %d sompi < fee %d sompi", totalValue, fee)
		}

		unsignedTransaction, err := libhtnwallet.CreateUnsignedTransaction(s.keysFile.ExtendedPublicKeys,
			s.keysFile.MinimumSignatures,
			[]*libhtnwallet.Payment{{Address: toAddress, Amount: totalValue - fee}}, selectedUTXOs, nil)
		if err != nil {
			return nil, nil, err
		}

		partiallySignedTransaction, err := serialization.DeserializePartiallySignedTransaction(unsignedTransaction)
		if err != nil {
			return nil, nil, err
		}
		mass, err := s.estimateMassAfterSignatures(partiallySignedTransaction)
		if err != nil {
			return nil, nil, err
		}
		if mass <= mempool.MaximumStandardTransactionMass {
			return unsignedTransaction, selectedUTXOs, nil
		}

		// Work out how many of these inputs actually fit, from this transaction's own measured mass
		// rather than from an assumed per-input size, and retry with that many.
		transactionWithoutInputs := partiallySignedTransaction.Tx.Clone()
		transactionWithoutInputs.Inputs = []*externalapi.DomainTransactionInput{}
		massWithoutInputs := s.txMassCalculator.CalculateTransactionMass(transactionWithoutInputs)
		if massWithoutInputs >= mempool.MaximumStandardTransactionMass {
			return nil, nil, errors.Errorf("a compound transaction's outputs alone exceed the standard mass limit")
		}

		massOfAllInputs := mass - massWithoutInputs
		massPerInput := massOfAllInputs / selectedCount
		if massOfAllInputs%selectedCount > 0 {
			massPerInput++
		}
		if massPerInput == 0 {
			return nil, nil, errors.Errorf("could not determine the mass of a compound transaction's inputs")
		}

		fittingCount, err := checkedIntFromUint64((mempool.MaximumStandardTransactionMass - massWithoutInputs) / massPerInput)
		if err != nil {
			return nil, nil, err
		}
		// Always make progress, even if the estimate above is optimistic.
		if fittingCount >= len(selectedUTXOs) {
			fittingCount = len(selectedUTXOs) - 1
		}
		if fittingCount < 0 {
			fittingCount = 0
		}
		selectedUTXOs = selectedUTXOs[:fittingCount]
	}
}

func (s *server) selectUTXOsForCompounding(feePerInput int, fromAddresses []*walletAddress) (
	selectedUTXOs []*libhtnwallet.UTXO, totalReceived uint64, changeSompi uint64, err error,
) {
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
	// log.Infof("Selected %d big UTXO for compound", totalValue/100_000_000)

	s.sortUTXOsByAmountAscending()
	// log.Infof("Found %d UTXO", len(s.utxosSortedByAmount))

	// Collect up to targetCompoundInputs smallest spendable UTXOs for compounding
	for _, utxo := range s.utxosSortedByAmount {
		if selectedUTXOsContain(selectedUTXOs, utxo) { // Don't accidentally spend same UTXO.
			continue
		}
		if len(selectedUTXOs) >= targetCompoundInputs {
			break
		}
		if (fromAddresses != nil && !walletAddressesContain(fromAddresses, utxo.address)) ||
			!s.isUTXOSpendable(utxo, dagInfo.VirtualDAAScore) {
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
	// log.Infof("Selected %d UTXO", len(s.utxosSortedByAmount))

	if len(selectedUTXOs) == 0 {
		return nil, 0, 0, errors.New("no spendable UTXOs for compounding")
	}

	// Require at least 2 UTXOs to make compounding worthwhile
	if len(selectedUTXOs) < 2 {
		return nil, 0, 0, errors.New("need at least 2 UTXOs to compound")
	}

	// Calculate fees based on the actual number of selected inputs
	selectedUTXOCount := len(selectedUTXOs)
	if selectedUTXOCount < 0 {
		return nil, 0, 0, errors.Errorf("selected UTXO count %d cannot be negative", selectedUTXOCount)
	}
	selectedUTXOCountUint64, err := checkedUint64FromInt(selectedUTXOCount)
	if err != nil {
		return nil, 0, 0, err
	}
	feePerInputUint64, err := checkedUint64FromInt(feePerInput)
	if err != nil {
		return nil, 0, 0, err
	}
	fee := selectedUTXOCountUint64 * feePerInputUint64
	if totalValue <= fee {
		return nil, 0, 0, errors.Errorf("not enough funds: total %d sompi < fee %d sompi", totalValue, fee)
	}

	changeSompi = totalValue - fee
	log.Infof("Compounding %d HTN and paying %f fee", changeSompi/100_000_000, float64(fee)/float64(100_000_000))

	return selectedUTXOs, totalValue, changeSompi, nil
}

func (s *server) createUnsignedTransactions(address string, amount uint64, isSendAll bool, fromAddressesString []string, useExistingChangeAddress bool, payload []byte, limit uint32) ([][]byte, error) {
	if !s.isSynced() {
		return nil, errors.Errorf("wallet daemon is not synced yet, %s", s.formatSyncStateReport())
	}

	err := s.refreshUTXOs(limit)
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
			return nil, errors.Errorf("specified from address %s does not exists", from)
		}
		fromAddresses = append(fromAddresses, fromAddress)
	}

	selectedUTXOs, spendValue, changeSompi, err := s.selectUTXOsForTransaction(amount, isSendAll, feePerInput, fromAddresses)
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

func (s *server) selectUTXOsForTransaction(spendAmount uint64, isSendAll bool, feePerInput uint64, fromAddresses []*walletAddress) (
	selectedUTXOs []*libhtnwallet.UTXO, totalReceived uint64, changeSompi uint64, err error,
) {
	selectedUTXOs = []*libhtnwallet.UTXO{}
	totalValue := uint64(0)

	dagInfo, err := s.rpcClient.GetBlockDAGInfo()
	if err != nil {
		return nil, 0, 0, err
	}

	s.sortUTXOsByAmountDescending()

	for _, utxo := range s.utxosSortedByAmount {
		if (fromAddresses != nil && !walletAddressesContain(fromAddresses, utxo.address)) ||
			!s.isUTXOSpendable(utxo, dagInfo.VirtualDAAScore) {
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

		// For spending biggest UTXOs: break as soon as we have enough funds
		// Don't add extra inputs just to avoid dust - prioritize using largest UTXOs
		if !isSendAll && totalValue >= totalSpend {
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
