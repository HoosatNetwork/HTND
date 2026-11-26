package server

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/daemon/pb"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/libhtnwallet"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/libhtnwallet/serialization"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/utils"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/txscript"
	"github.com/HoosatNetwork/HTND/v2/domain/miningmanager/mempool"
	"github.com/HoosatNetwork/HTND/v2/util"
	"github.com/pkg/errors"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
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

	send, err := s.createUnsignedTransactions(request.Address, request.Amount, request.IsSendAll,
		request.From, request.UseExistingChangeAddress, request.Payload, limit)
	if _, ok := err.(changeKeyUnavailableError); ok {
		return nil, utils.MLDSA44ChangeKeyRPCError(err)
	}
	if err != nil {
		return nil, err
	}

	if err := s.commitChange(send.change); err != nil {
		return nil, err
	}
	return &pb.CreateUnsignedTransactionsResponse{UnsignedTransactions: [][]byte{send.transaction}}, nil
}

func (s *server) CreateUnsignedCompoundTransaction(ctx context.Context, request *pb.CreateUnsignedCompoundTransactionRequest) (
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

	unsignedTransactions, err := s.createUnsignedCompoundTransaction(ctx, request.Address, request.From, limit)
	if err != nil {
		return nil, err
	}

	return &pb.CreateUnsignedCompoundTransactionResponse{UnsignedTransactions: unsignedTransactions}, nil
}

func (s *server) createUnsignedCompoundTransaction(ctx context.Context, address string, fromAddressesString []string, limit uint32) ([][]byte, error) {
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

	selectedUTXOs, skipped, err := s.selectUTXOsForCompounding(fromAddresses, toAddress)
	if err != nil && !refreshed {
		// The reused set may just have run out of coins this daemon has not already spent; only a
		// fresh one can tell that apart from there being nothing left to compound.
		err = s.refreshCompoundUTXOs(limit)
		if err != nil {
			return nil, err
		}
		selectedUTXOs, skipped, err = s.selectUTXOsForCompounding(fromAddresses, toAddress)
	}
	if err != nil {
		return nil, compoundErrorWithSkips(err, toAddress, skipped)
	}

	return s.finishCompound(ctx, selectedUTXOs, toAddress, skipped)
}

func (s *server) finishCompound(ctx context.Context, selectedUTXOs []*libhtnwallet.UTXO, toAddress util.Address, skipped int) ([][]byte, error) {
	unsignedTransaction, spentUTXOs, err := s.buildCompoundTransactionWithinStandardMass(selectedUTXOs, toAddress)
	if err != nil {
		return nil, compoundErrorWithSkips(err, toAddress, skipped)
	}
	if _, err := s.requireStandardMass(unsignedTransaction); err != nil {
		return nil, compoundErrorWithSkips(err, toAddress, skipped)
	}

	if err := s.trackCompoundDestination(toAddress); err != nil {
		return nil, compoundErrorWithSkips(err, toAddress, skipped)
	}

	// Reserve only the inputs that survived the mass and dust checks.
	for _, spentUTXO := range spentUTXOs {
		s.usedOutpoints[*spentUTXO.Outpoint] = time.Now()
	}

	if skipped > 0 {
		notice := compoundSkipNotice(toAddress, skipped)
		log.Info(notice)
		// Older clients ignore this optional trailer.
		_ = grpc.SetTrailer(ctx, metadata.Pairs(utils.CompoundSkipTrailer, notice))
	}
	return [][]byte{unsignedTransaction}, nil
}

func compoundErrorWithSkips(err error, destination util.Address, skipped int) error {
	if skipped == 0 {
		return err
	}
	return errors.Wrap(err, compoundSkipNotice(destination, skipped))
}

func compoundSkipNotice(destination util.Address, skipped int) string {
	reason := fmt.Sprintf("%s is a classical address", destination)
	if _, ok := destination.(*util.AddressScriptHash); ok {
		reason = fmt.Sprintf("the P2SH destination %s could not be verified as an ML-DSA-44 address", destination)
	}
	return fmt.Sprintf("%d ML-DSA-44 coins were left out because %s; "+
		"compound to an ML-DSA-44 address, or name the addresses to compound from, to include them", skipped, reason)
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

func (s *server) selectUTXOsForCompounding(fromAddresses []*walletAddress, toAddress util.Address) ([]*libhtnwallet.UTXO, int, error) {
	dagInfo, err := s.getBlockDAGInfo(context.Background())
	if err != nil {
		return nil, 0, errors.Wrap(err, "failed to get DAG info")
	}
	return s.selectUTXOsForCompoundingAt(dagInfo.VirtualDAAScore, fromAddresses, toAddress)
}

// indexCompoundP2SHAddresses derives the P2SH destinations covered by the wallet's public key
// pools once, before starting sync or serving requests. The pools do not change while it runs.
func (s *server) indexCompoundP2SHAddresses() {
	s.compoundP2SHAddresses = make(map[[32]byte]walletAddress)
	for _, keychain := range keyChains {
		candidate := walletAddress{keyChain: keychain, mldsa44: true}
		if !s.isMultisig() {
			candidate.mldsa44Form = libhtnwallet.MLDSA44AddressFormP2SH
		}
		for candidate.index = 0; candidate.index < scanIndexLimit; candidate.index++ {
			derived, err := s.mldsa44Address(&candidate)
			if err != nil {
				break // The shortest key pool determines the covered range.
			}
			s.compoundP2SHAddresses[[32]byte(derived.ScriptAddress())] = candidate
		}
	}
}

func (s *server) ownedCompoundP2SH(address util.Address) (walletAddress, bool) {
	if p2sh, ok := address.(*util.AddressScriptHash); ok {
		path, owned := s.compoundP2SHAddresses[[32]byte(p2sh.ScriptAddress())]
		return path, owned
	}
	return walletAddress{}, false
}

func (s *server) trackCompoundDestination(address util.Address) error {
	path, owned := s.ownedCompoundP2SH(address)
	if !owned {
		return nil
	}

	// A cosigner can supply an empty address past this daemon's last-used index. Record it
	// now so recent or old address scans can find the output even after a restart.
	external, internal := s.lastUsedIndexes()
	if path.keyChain == libhtnwallet.ExternalKeychain && path.index > external {
		if err := s.keysFile.SetLastUsedExternalIndex(path.index); err != nil {
			return err
		}
	}
	if path.keyChain == libhtnwallet.InternalKeychain && path.index > internal {
		if err := s.keysFile.SetLastUsedInternalIndex(path.index); err != nil {
			return err
		}
	}
	s.trackAddress(address.String(), &path)
	return nil
}

// selectUTXOsForCompoundingAt includes ML-DSA-44 inputs when the destination is verified or the
// caller explicitly named source addresses. It reports the number excluded by that policy alone.
func (s *server) selectUTXOsForCompoundingAt(virtualDAAScore uint64, fromAddresses []*walletAddress,
	toAddress util.Address,
) ([]*libhtnwallet.UTXO, int, error) {
	eligible := func(coin *walletUTXO) bool {
		if (len(fromAddresses) != 0 && !walletAddressesContain(fromAddresses, coin.address)) ||
			!s.isUTXOSpendable(coin, virtualDAAScore) {
			return false
		}
		if broadcastTime, ok := s.usedOutpoints[*coin.Outpoint]; ok {
			if !s.usedOutpointHasExpired(broadcastTime) {
				return false
			}
			delete(s.usedOutpoints, *coin.Outpoint)
		}
		return true
	}
	var skipped int
	_, nativeMLDSA44 := toAddress.(*util.AddressPublicKeyHashMLDSA44)
	spendMLDSA44 := len(fromAddresses) != 0 || nativeMLDSA44
	if !spendMLDSA44 {
		for _, coin := range s.utxosSortedByAmount {
			if coin.address.mldsa44 && eligible(coin) {
				skipped++
			}
		}
		if skipped != 0 {
			_, spendMLDSA44 = s.ownedCompoundP2SH(toAddress)
			if spendMLDSA44 {
				skipped = 0
			}
		}
	}

	selectedUTXOs := make([]*libhtnwallet.UTXO, 0, targetCompoundInputs)
	var totalValue uint64
	s.sortUTXOsByAmountDescending()
	for _, coin := range s.utxosSortedByAmount {
		if (!spendMLDSA44 && coin.address.mldsa44) || !eligible(coin) {
			continue
		}
		selectedUTXO, err := s.libhtnwalletUTXO(coin.Outpoint, coin.UTXOEntry, coin.address)
		if err != nil {
			return nil, skipped, err
		}
		selectedUTXOs = append(selectedUTXOs, selectedUTXO)
		totalValue += coin.UTXOEntry.Amount()
		break
	}

	s.sortUTXOsByAmountAscending()
	for _, coin := range s.utxosSortedByAmount {
		if len(selectedUTXOs) >= targetCompoundInputs {
			break
		}
		if (!spendMLDSA44 && coin.address.mldsa44) || !eligible(coin) || selectedUTXOsContain(selectedUTXOs, coin) {
			continue
		}
		selectedUTXO, err := s.libhtnwalletUTXO(coin.Outpoint, coin.UTXOEntry, coin.address)
		if err != nil {
			return nil, skipped, err
		}
		selectedUTXOs = append(selectedUTXOs, selectedUTXO)
		totalValue += coin.UTXOEntry.Amount()
	}

	if len(selectedUTXOs) == 0 {
		return nil, skipped, errors.New("no spendable UTXOs for compounding")
	}
	if len(selectedUTXOs) < 2 {
		return nil, skipped, errors.New("need at least 2 UTXOs to compound")
	}
	fee := uint64(len(selectedUTXOs)) * feePerInput
	if totalValue <= fee {
		return nil, skipped, errors.Errorf("not enough funds: total %d sompi < fee %d sompi", totalValue, fee)
	}
	return selectedUTXOs, skipped, nil
}

func (s *server) createUnsignedTransactions(address string, amount uint64, isSendAll bool, fromAddressesString []string, useExistingChangeAddress bool, payload []byte, limit uint32) (*preparedSend, error) {
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

	dagInfo, err := s.getBlockDAGInfo(context.Background())
	if err != nil {
		return nil, err
	}

	return s.buildSendTransaction(toAddress, amount, isSendAll, fromAddressesString, useExistingChangeAddress, payload,
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
//
// The change address is an ML-DSA-44 one as soon as any coin of the prefix is ML-DSA-44. The change
// of such a send carries ML-DSA-44 value, and a secp256k1 change address would move it out of
// post-quantum protection without the user having asked for that. The only exception is a change
// address the user named: with useExistingChangeAddress, the first eligible address in from.
func (s *server) buildSendTransaction(toAddress util.Address, amount uint64, isSendAll bool,
	from []string, useExistingChangeAddress bool, payload []byte, virtualDAAScore uint64,
) (*preparedSend, error) {
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

	var fromAddresses []*walletAddress
	for _, source := range from {
		fromAddress, exists := s.addressSet[source]
		if !exists {
			return nil, errors.Errorf("specified from address %s does not exists", source)
		}
		fromAddresses = append(fromAddresses, fromAddress)
	}

	var namedChange util.Address
	var namedChangePath *walletAddress
	if useExistingChangeAddress {
		// Change follows the first eligible source and keeps the encoding the caller supplied.
		for i, source := range fromAddresses {
			if !s.isAddressSpendableAt(source, virtualDAAScore) {
				continue
			}
			var err error
			namedChange, err = util.DecodeAddress(from[i], s.params.Prefix)
			if err != nil {
				return nil, err
			}
			namedChangePath = source
			break
		}
	}

	coins := s.spendableUTXOsLargestFirst(fromAddresses, virtualDAAScore)
	transactions, err := s.buildSendWithCoins(toAddress, amount, isSendAll, useExistingChangeAddress,
		namedChange, namedChangePath, payload, coins)
	if isSendAll || len(from) != 0 {
		return transactions, err
	}
	switch err.(type) {
	case changeKeyUnavailableError, standardMassError:
	default:
		return transactions, err
	}

	// A lighter, entirely classical payment may fit without needing an ML-DSA-44 change key.
	var classical []*walletUTXO
	for _, coin := range coins {
		if !coin.address.mldsa44 {
			classical = append(classical, coin)
		}
	}
	if len(classical) != 0 && len(classical) < len(coins) {
		retry, retryErr := s.buildSendWithCoins(toAddress, amount, false, useExistingChangeAddress,
			nil, nil, payload, classical)
		switch retryErr.(type) {
		case sendFundingError, standardMassError:
			// There is no standard payment using this selection. Keep the original diagnosis.
		default:
			return retry, retryErr
		}
	}

	if keyErr, ok := err.(changeKeyUnavailableError); ok {
		// Try one alternate prefix on key exhaustion. Swapping its smallest coin for the
		// next smaller coin may cover the payment and fee without requiring change.
		if keyErr.inputCount < len(coins) {
			alternate := slices.Clone(coins[:keyErr.inputCount])
			alternate[len(alternate)-1] = coins[keyErr.inputCount]
			retry, retryErr := s.buildSendWithCoins(toAddress, amount, false, useExistingChangeAddress,
				nil, nil, payload, alternate)
			switch retryErr.(type) {
			case sendFundingError, standardMassError, changeKeyUnavailableError:
			default:
				return retry, retryErr
			}
		}
		keyErr.error = errors.Errorf("%s; to try a different set of inputs, specify source addresses with --from-address", keyErr.error)
		return nil, keyErr
	}
	return transactions, err
}

type changeKeyUnavailableError struct {
	error
	inputCount int
}

type sendFundingError struct{ error }

func (s *server) buildSendWithCoins(toAddress util.Address, amount uint64, isSendAll, useExistingChangeAddress bool,
	namedChange util.Address, namedChangePath *walletAddress, payload []byte, coins []*walletUTXO,
) (*preparedSend, error) {
	if len(coins) == 0 {
		return nil, sendFundingError{errors.New("couldn't find funds to spend")}
	}
	if isSendAll {
		amount = 1 // Measure with a placeholder until the actual fee is known.
	}
	var selectedUTXOs []*libhtnwallet.UTXO
	totalValue, required := uint64(0), uint64(0)
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

		// Every fee is at least the flat one, so an unfunded prefix is not worth building.
		required = amount + feePerInput*uint64(len(selectedUTXOs))
		if (isSendAll && i < len(coins)-1) || (!isSendAll && totalValue < required) {
			continue
		}

		payment := &libhtnwallet.Payment{Address: toAddress, Amount: amount}
		unsignedTransaction, err := libhtnwallet.CreateUnsignedTransaction(s.keysFile.ExtendedPublicKeys,
			s.keysFile.MinimumSignatures, []*libhtnwallet.Payment{payment}, selectedUTXOs, payload)
		if err != nil {
			return nil, err
		}
		transaction, err := serialization.DeserializePartiallySignedTransaction(unsignedTransaction)
		if err != nil {
			return nil, err
		}
		massWithoutChange, err := s.estimateMassAfterSignatures(transaction)
		if err != nil {
			return nil, err
		}
		if err := checkStandardMass(len(selectedUTXOs), massWithoutChange); err != nil {
			return nil, err
		}

		fee := feeFor(massWithoutChange)
		if isSendAll {
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
			transaction.Tx.Outputs[0].Value = totalValue - fee
			return finalizeSend(transaction, nil)
		}

		required = amount + fee
		if totalValue < required {
			continue
		}
		automaticMLDSA44 := namedChange == nil && anyInputIsMLDSA44(selectedUTXOs)
		if totalValue == required {
			return s.finalizePaymentOnly(transaction, namedChange, namedChangePath, useExistingChangeAddress, automaticMLDSA44)
		}

		changeAddress, changeWalletAddress := namedChange, namedChangePath
		if automaticMLDSA44 {
			// Measure the script type before deriving the real address from the public key pool.
			var zeroHash [32]byte
			if s.isMultisig() {
				changeAddress, err = util.NewAddressScriptHashFromHash(zeroHash[:], s.params.Prefix)
			} else {
				changeAddress, err = libhtnwallet.MLDSA44Address(s.params, zeroHash[:])
			}
		} else if namedChange == nil {
			changeAddress, changeWalletAddress, err = s.deriveChangeAddress(useExistingChangeAddress, false)
		}
		if err != nil {
			return nil, err
		}

		changeScript, err := txscript.PayToAddrScript(changeAddress)
		if err != nil {
			return nil, err
		}
		changeTxOutput := &externalapi.DomainTransactionOutput{ScriptPublicKey: changeScript}
		transaction.Tx.Outputs = append(transaction.Tx.Outputs, changeTxOutput)
		massWithChange, err := s.estimateMassAfterSignatures(transaction)
		if err != nil {
			return nil, err
		}
		feeWithChange := feeFor(massWithChange)
		if totalValue < amount+feeWithChange {
			transaction.Tx.Outputs = transaction.Tx.Outputs[:1]
			return s.finalizePaymentOnly(transaction, changeAddress, changeWalletAddress, useExistingChangeAddress, automaticMLDSA44)
		}
		change := totalValue - amount - feeWithChange
		dust, err := s.isDust(changeAddress, change)
		if err != nil {
			return nil, err
		}
		if dust {
			transaction.Tx.Outputs = transaction.Tx.Outputs[:1]
			return s.finalizePaymentOnly(transaction, changeAddress, changeWalletAddress, useExistingChangeAddress, automaticMLDSA44)
		}
		if massWithChange > mempool.MaximumStandardTransactionMass {
			// Never pay a spendable remainder as fee because its change output does not fit.
			return nil, standardMassError{errors.Errorf("the change of %d sompi would take the transaction to a mass of %d, "+
				"over the standard limit of %d. Send a smaller amount, or consolidate the wallet's coins "+
				"first (for example with auto-compound)",
				change, massWithChange, uint64(mempool.MaximumStandardTransactionMass))}
		}
		if automaticMLDSA44 {
			changeAddress, changeWalletAddress, err = s.deriveChangeAddress(useExistingChangeAddress, true)
			if err != nil {
				return nil, changeKeyUnavailableError{
					error: errors.Wrap(err, utils.MLDSA44ChangeKeyUnavailable), inputCount: len(selectedUTXOs),
				}
			}
			// The derived script has the same size as the script just measured.
			changeTxOutput.ScriptPublicKey, err = txscript.PayToAddrScript(changeAddress)
			if err != nil {
				return nil, err
			}
		}
		changeTxOutput.Value = change
		return finalizeSend(transaction, &changeOutput{
			address: changeAddress, walletAddr: changeWalletAddress, fresh: !useExistingChangeAddress,
		})
	}

	return nil, sendFundingError{errors.Errorf("Insufficient funds for send: %f required, while only %f available",
		float64(required)/constants.SompiPerHoosat, float64(totalValue)/constants.SompiPerHoosat)}
}

// A recipient-only payment can still pay the candidate's script. Resolve it if
// available, without requiring a change key for an exact or dust-remainder payment.
func (s *server) finalizePaymentOnly(transaction *serialization.PartiallySignedTransaction,
	address util.Address, walletAddr *walletAddress, useExisting, mldsa44 bool,
) (*preparedSend, error) {
	if walletAddr == nil {
		var err error
		address, walletAddr, err = s.deriveChangeAddress(useExisting, mldsa44)
		if err != nil {
			return finalizeSend(transaction, nil)
		}
	}
	return finalizeSend(transaction, &changeOutput{address: address, walletAddr: walletAddr, fresh: !useExisting})
}

type preparedSend struct {
	transaction []byte
	change      *changeOutput
}

func finalizeSend(transaction *serialization.PartiallySignedTransaction, candidate *changeOutput) (*preparedSend, error) {
	unsigned, err := serialization.SerializePartiallySignedTransaction(transaction)
	if err != nil {
		return nil, err
	}
	send := &preparedSend{transaction: unsigned}
	if candidate != nil {
		script, err := txscript.PayToAddrScript(candidate.address)
		if err != nil {
			return nil, err
		}
		// The payment itself can use the candidate's script, even when no change output was added.
		if slices.ContainsFunc(transaction.Tx.Outputs, func(output *externalapi.DomainTransactionOutput) bool {
			return output.ScriptPublicKey.Equal(script)
		}) {
			send.change = candidate
		}
	}
	return send, nil
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

// anyInputIsMLDSA44 reports whether any selected coin is held by an ML-DSA-44 address, in which case
// the change goes to one too.
func anyInputIsMLDSA44(selectedUTXOs []*libhtnwallet.UTXO) bool {
	for _, selectedUTXO := range selectedUTXOs {
		// A P2SH output does not say what is behind it; the carried redeem script does.
		if libhtnwallet.IsMLDSA44Coin(selectedUTXO.UTXOEntry.ScriptPublicKey().Script, selectedUTXO.RedeemScript) {
			return true
		}
	}
	return false
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
