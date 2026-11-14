package server

import (
	"context"
	"slices"
	"strconv"
	"time"

	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/daemon/pb"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/libhtnwallet"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/libhtnwallet/serialization"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/v2/domain/miningmanager/mempool"
	"github.com/HoosatNetwork/HTND/v2/util"
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

	refreshed := false
	if s.compoundNeedsUTXORefresh(limit, time.Now()) {
		err = s.refreshCompoundUTXOs(limit)
		if err != nil {
			return nil, err
		}
		refreshed = true
	}

	selectedUTXOs, _, _, err := s.selectUTXOsForCompounding(feePerInput, fromAddresses)
	if err != nil && !refreshed {
		// The reused set may just have run out of coins this daemon has not already spent; only a
		// fresh one can tell that apart from there being nothing left to compound.
		err = s.refreshCompoundUTXOs(limit)
		if err != nil {
			return nil, err
		}
		selectedUTXOs, _, _, err = s.selectUTXOsForCompounding(feePerInput, fromAddresses)
	}
	if err != nil {
		return nil, err
	}

	if len(selectedUTXOs) < 2 {
		return nil, errors.Errorf("nothing to compound")
	}

	// For compounding we want to consolidate inputs into a single output.
	// Send the net amount (after fees) to the requested address and avoid creating
	// an additional change output to keep base mass low and prevent dust.
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

	return s.requireStandardMass(unsignedTransaction)
}

// compoundUTXOSetMaxAge is how long a compound reuses the wallet's UTXO set before fetching it again.
//
// A refresh asks the node for up to limit coins of every wallet address, and the node checks each one
// against virtual's UTXO set with a database read. On a wallet with many coins that takes minutes,
// while a compound spends at most targetCompoundInputs of them. Refreshing for every compound made the
// refresh, not the compound rate, set the pace. Reusing the set is safe because the coins this daemon
// spent stay in usedOutpoints and are skipped. What it costs is that coins received since the refresh
// wait for the next one, and that coins spent elsewhere are only noticed when a broadcast is refused
// over them (utxoSetIsStale).
const compoundUTXOSetMaxAge = 10 * time.Minute

// compoundNeedsUTXORefresh reports whether a compound must fetch the wallet's UTXO set again rather
// than reuse the one it has.
func (s *server) compoundNeedsUTXORefresh(limit uint32, now time.Time) bool {
	return s.utxoSetIsStale ||
		s.startTimeOfLastCompletedRefresh.IsZero() ||
		s.limitOfLastCompletedRefresh != limit ||
		now.Sub(s.startTimeOfLastCompletedRefresh) > compoundUTXOSetMaxAge
}

func (s *server) refreshCompoundUTXOs(limit uint32) error {
	err := s.refreshUTXOs(limit)
	if err != nil {
		return err
	}
	log.Infof("Fetched %d UTXO from the Node", len(s.utxosSortedByAmount))
	return nil
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
// 105138 from a P2SH one, against a 100000 limit. Going over used to push those wallets into split
// and merge transactions, whose splits paid the change address rather than the requested destination;
// now it would refuse the compound outright (requireStandardMass).
//
// Sizing by measured mass instead of by count keeps a compound a single transaction paying the
// destination directly, whatever the inputs look like, rather than depending on a constant that only
// happens to hold for one address type.
func (s *server) buildCompoundTransactionWithinStandardMass(selectedUTXOs []*libhtnwallet.UTXO,
	toAddress util.Address,
) ([]byte, []*libhtnwallet.UTXO, error) {
retrySubset:
	for {
		if len(selectedUTXOs) < 2 {
			return nil, nil, errors.Errorf("no funded, standard-mass compound of at least 2 inputs with a non-dust " +
				"output could be built from the selected coins")
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
		var unsignedTransaction []byte
		var partiallySignedTransaction *serialization.PartiallySignedTransaction
		var mass uint64
		// The first pass pays the flat fee. If the signed mass needs more (ML-DSA multisig inputs do),
		// the second pays the mass-based fee: the outputs' size does not depend on their amounts.
		for pass := 0; pass < 2; pass++ {
			if totalValue <= fee {
				// The coins cannot pay for themselves: try one fewer.
				selectedUTXOs = selectedUTXOs[:len(selectedUTXOs)-1]
				continue retrySubset
			}

			unsignedTransaction, err = libhtnwallet.CreateUnsignedTransaction(s.keysFile.ExtendedPublicKeys,
				s.keysFile.MinimumSignatures,
				[]*libhtnwallet.Payment{{Address: toAddress, Amount: totalValue - fee}}, selectedUTXOs, nil)
			if err != nil {
				return nil, nil, err
			}

			partiallySignedTransaction, err = serialization.DeserializePartiallySignedTransaction(unsignedTransaction)
			if err != nil {
				return nil, nil, err
			}
			mass, err = s.estimateMassAfterSignatures(partiallySignedTransaction)
			if err != nil {
				return nil, nil, err
			}
			if required := s.minimumRelayFee(mass); required > fee && mass <= mempool.MaximumStandardTransactionMass {
				fee = required
				continue
			}
			break
		}
		if mass <= mempool.MaximumStandardTransactionMass {
			dust, err := s.isDust(toAddress, totalValue-fee)
			if err != nil {
				return nil, nil, err
			}
			if dust {
				selectedUTXOs = selectedUTXOs[:len(selectedUTXOs)-1]
				continue retrySubset
			}
			log.Infof("Compounding %d HTN and paying %f fee", (totalValue-fee)/100_000_000, float64(fee)/float64(100_000_000))
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

		selectedUTXO, err := s.libhtnwalletUTXO(highestUTXO.Outpoint, highestUTXO.UTXOEntry, highestUTXO.address)
		if err != nil {
			return nil, 0, 0, err
		}
		selectedUTXOs = append(selectedUTXOs, selectedUTXO)
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

		selectedUTXO, err := s.libhtnwalletUTXO(utxo.Outpoint, utxo.UTXOEntry, utxo.address)
		if err != nil {
			return nil, 0, 0, err
		}
		selectedUTXOs = append(selectedUTXOs, selectedUTXO)
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

	dagInfo, err := s.rpcClient.GetBlockDAGInfo()
	if err != nil {
		return nil, err
	}

	return s.buildSendTransaction(toAddress, amount, isSendAll, fromAddresses, useExistingChangeAddress, payload,
		dagInfo.VirtualDAAScore)
}

// buildSendTransaction selects the coins for a send and builds the unsigned transaction.
//
// It walks the spendable coins largest first, and for each prefix of them works out the fee that
// prefix needs: feePerInput per input, or the relay minimum for the transaction's signed mass if that
// is more (see minimumRelayFee) - ML-DSA-44 multisig inputs weigh several times a Schnorr one. It
// stops at the first prefix that pays the amount and that fee. Change goes to the change address
// unless it would be dust; then the transaction has no change output, and what is left over (less than
// a dust output plus the fee the change output would have cost) goes to the fee. The requested amount
// is never reduced, and no output is dust. If a non-dust change would push the transaction over the
// standard mass, the send is refused rather than paying that remainder as fee.
func (s *server) buildSendTransaction(toAddress util.Address, amount uint64, isSendAll bool,
	fromAddresses []*walletAddress, useExistingChangeAddress bool, payload []byte, virtualDAAScore uint64,
) ([][]byte, error) {
	// The amount comes unchecked from the request. Near the uint64 limit, amount + fee wrapped to a
	// small number that the funds check accepted.
	if !isSendAll && amount > constants.MaxSompi {
		return nil, errors.Errorf("send amount of %d sompi exceeds the maximum of %d sompi",
			amount, uint64(constants.MaxSompi))
	}
	if !isSendAll {
		dust, err := s.isDust(toAddress, amount)
		if err != nil {
			return nil, err
		}
		if dust {
			return nil, errors.Errorf("send amount of %d sompi is below the relay dust threshold", amount)
		}
	}

	coins := s.spendableUTXOsLargestFirst(fromAddresses, virtualDAAScore)
	if len(coins) == 0 {
		return nil, errors.Errorf("couldn't find funds to spend")
	}

	var changeAddress util.Address // taken once: each new one uses up an internal address index
	var selectedUTXOs []*libhtnwallet.UTXO
	totalValue, required := uint64(0), uint64(0)
	// measure builds the transaction for the coins selected so far and returns it with its signed mass.
	measure := func(payments []*libhtnwallet.Payment) ([]byte, uint64, error) {
		unsignedTransaction, err := libhtnwallet.CreateUnsignedTransaction(s.keysFile.ExtendedPublicKeys,
			s.keysFile.MinimumSignatures, payments, selectedUTXOs, payload)
		if err != nil {
			return nil, 0, err
		}
		partiallySignedTransaction, err := serialization.DeserializePartiallySignedTransaction(unsignedTransaction)
		if err != nil {
			return nil, 0, err
		}
		mass, err := s.estimateMassAfterSignatures(partiallySignedTransaction)
		return unsignedTransaction, mass, err
	}
	feeFor := func(mass uint64) uint64 {
		return max(feePerInput*uint64(len(selectedUTXOs)), s.minimumRelayFee(mass))
	}

	for i, coin := range coins {
		selectedUTXO, err := s.libhtnwalletUTXO(coin.Outpoint, coin.UTXOEntry, coin.address)
		if err != nil {
			return nil, err
		}
		selectedUTXOs = append(selectedUTXOs, selectedUTXO)
		totalValue += coin.UTXOEntry.Amount()

		if isSendAll {
			if i < len(coins)-1 {
				continue
			}
			unsignedTransaction, mass, err := measure([]*libhtnwallet.Payment{{Address: toAddress, Amount: 1}})
			if err != nil {
				return nil, err
			}
			if mass > mempool.MaximumStandardTransactionMass {
				return s.requireStandardMass(unsignedTransaction)
			}
			fee := feeFor(mass)
			if totalValue <= fee {
				return nil, errors.Errorf("Insufficient funds for send all: %f available, while the fee is %f",
					float64(totalValue)/constants.SompiPerHoosat, float64(fee)/constants.SompiPerHoosat)
			}
			dust, err := s.isDust(toAddress, totalValue-fee)
			if err != nil {
				return nil, err
			}
			if dust {
				return nil, errors.Errorf("send all would pay %d sompi after the fee of %d sompi, which is "+
					"below the relay dust threshold", totalValue-fee, fee)
			}
			unsignedTransaction, _, err = measure([]*libhtnwallet.Payment{{Address: toAddress, Amount: totalValue - fee}})
			if err != nil {
				return nil, err
			}
			return s.requireStandardMass(unsignedTransaction)
		}

		// Every fee is at least the flat one, so a prefix that cannot pay that is not worth building.
		required = amount + feePerInput*uint64(len(selectedUTXOs))
		if totalValue < required {
			continue
		}

		if changeAddress == nil {
			changeAddress, _, err = s.changeAddress(useExistingChangeAddress, fromAddresses, allInputsAreMLDSA44(selectedUTXOs))
			if err != nil {
				return nil, err
			}
		}
		payment := &libhtnwallet.Payment{Address: toAddress, Amount: amount}

		// With change, if that leaves a coin worth sending.
		_, massWithChange, err := measure([]*libhtnwallet.Payment{payment, {Address: changeAddress, Amount: 1}})
		if err != nil {
			return nil, err
		}
		if feeWithChange := feeFor(massWithChange); totalValue >= amount+feeWithChange {
			change := totalValue - amount - feeWithChange
			dust, err := s.isDust(changeAddress, change)
			if err != nil {
				return nil, err
			}
			if !dust {
				if massWithChange > mempool.MaximumStandardTransactionMass {
					// Never pay a spendable remainder as fee because its change output does not fit.
					return nil, errors.Errorf("the change of %d sompi would take the transaction to a mass of %d, "+
						"over the standard limit of %d. Send a smaller amount, or consolidate the wallet's coins "+
						"first (for example with auto-compound)",
						change, massWithChange, uint64(mempool.MaximumStandardTransactionMass))
				}
				unsignedTransaction, _, err := measure([]*libhtnwallet.Payment{payment, {Address: changeAddress, Amount: change}})
				if err != nil {
					return nil, err
				}
				return s.requireStandardMass(unsignedTransaction)
			}
		}

		// Without change: the residual, if any, is the fee.
		unsignedTransaction, massWithoutChange, err := measure([]*libhtnwallet.Payment{payment})
		if err != nil {
			return nil, err
		}
		if massWithoutChange > mempool.MaximumStandardTransactionMass {
			// More coins only add mass.
			return s.requireStandardMass(unsignedTransaction)
		}
		required = amount + feeFor(massWithoutChange)
		if totalValue >= required {
			return s.requireStandardMass(unsignedTransaction)
		}
	}

	return nil, errors.Errorf("Insufficient funds for send: %f required, while only %f available",
		float64(required)/constants.SompiPerHoosat, float64(totalValue)/constants.SompiPerHoosat)
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

// spendableUTXOsLargestFirst returns the coins a send may spend, largest first: the ones at
// fromAddresses (all addresses if nil) that are old enough at virtualDAAScore and that this daemon has
// not already spent.
func (s *server) spendableUTXOsLargestFirst(fromAddresses []*walletAddress, virtualDAAScore uint64) []*walletUTXO {
	s.sortUTXOsByAmountDescending()

	var spendable []*walletUTXO
	for _, utxo := range s.utxosSortedByAmount {
		if (fromAddresses != nil && !walletAddressesContain(fromAddresses, utxo.address)) ||
			!s.isUTXOSpendable(utxo, virtualDAAScore) {
			continue
		}

		if broadcastTime, ok := s.usedOutpoints[*utxo.Outpoint]; ok {
			if s.usedOutpointHasExpired(broadcastTime) {
				delete(s.usedOutpoints, *utxo.Outpoint)
			} else {
				continue
			}
		}
		spendable = append(spendable, utxo)
	}
	return spendable
}

// libhtnwalletUTXO returns the libhtnwallet.UTXO for a coin held at address: its derivation path, the
// imported key that spends it for an imported wallet's coin, and for ML-DSA-44 multisig its redeem
// script, which the signers cannot rebuild themselves.
func (s *server) libhtnwalletUTXO(outpoint *externalapi.DomainOutpoint, utxoEntry externalapi.UTXOEntry,
	address *walletAddress,
) (*libhtnwallet.UTXO, error) {
	if address.imported != nil {
		return &libhtnwallet.UTXO{
			Outpoint:                  outpoint,
			UTXOEntry:                 utxoEntry,
			DerivationPath:            libhtnwallet.ImportedKeyDerivationPath,
			ImportedExtendedPublicKey: address.imported.ExtendedPublicKey,
		}, nil
	}
	redeemScript, err := s.walletAddressRedeemScript(address)
	if err != nil {
		return nil, err
	}
	return &libhtnwallet.UTXO{
		Outpoint:       outpoint,
		UTXOEntry:      utxoEntry,
		DerivationPath: s.walletAddressPath(address),
		RedeemScript:   redeemScript,
	}, nil
}

// allInputsAreMLDSA44 reports whether every selected coin is held by an ML-DSA-44 address, in which
// case the change goes to one too (see changeAddress).
func allInputsAreMLDSA44(selectedUTXOs []*libhtnwallet.UTXO) bool {
	if len(selectedUTXOs) == 0 {
		return false
	}
	for _, selectedUTXO := range selectedUTXOs {
		// A P2SH output does not say what is behind it; the carried redeem script does.
		if !libhtnwallet.IsMLDSA44Coin(selectedUTXO.UTXOEntry.ScriptPublicKey().Script, selectedUTXO.RedeemScript) {
			return false
		}
	}
	return true
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
