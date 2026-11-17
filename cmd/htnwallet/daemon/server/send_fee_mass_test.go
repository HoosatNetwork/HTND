package server

import (
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/keys"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/libhtnwallet"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/libhtnwallet/serialization"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/txscript"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/utxo"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
	"github.com/HoosatNetwork/HTND/v2/util"
	"github.com/HoosatNetwork/HTND/v2/util/txmass"
)

// The tests below sign what the builders return, measure the signed transaction with the real mass
// calculator, and compare the fee with that mass using the default relay rate written out
// (1 sompi per mass unit, 603 sompi dust for a P2SH output), not with the production helpers.

const (
	// feeTestVirtualDAAScore is past the coins' DAA score (1) plus the input minimum age and its slack.
	feeTestVirtualDAAScore = 1 + inputMinAgeDAAScore + 100
	// feeTestMaxMass is mempool.MaximumStandardTransactionMass.
	feeTestMaxMass = 100_000
	// feeTestP2SHDust is the smallest non-dust P2SH output: 3 * (8+2+8+35 + 148).
	feeTestP2SHDust = 603
)

// feeWallet is a test wallet whose server signs with its own mnemonics.
type feeWallet struct {
	t         *testing.T
	s         *server
	mnemonics []string
	params    *dagconfig.Params
}

func (w *feeWallet) minSigs() int { return int(w.s.keysFile.MinimumSignatures) }

// newMLWallet is an ML-DSA-44 wallet: m-of-n multisig, or single-key when n is 1.
func newMLWallet(t *testing.T, n int, m uint32) *feeWallet {
	t.Helper()
	params := &dagconfig.MainnetParams
	w := &feeWallet{t: t, params: params}
	xpubs := make([]string, n)
	pools := make(map[string]*keys.MLDSA44KeyPool, n)
	var pool *keys.MLDSA44KeyPool
	for i := range xpubs {
		mnemonic, err := libhtnwallet.CreateMnemonic()
		if err != nil {
			t.Fatalf("CreateMnemonic: %+v", err)
		}
		w.mnemonics = append(w.mnemonics, mnemonic)
		xpubs[i], err = libhtnwallet.MasterPublicKeyFromMnemonic(params, mnemonic, n > 1)
		if err != nil {
			t.Fatalf("MasterPublicKeyFromMnemonic: %+v", err)
		}
		pool, err = keys.NewMLDSA44KeyPool(mnemonic, 3, n > 1)
		if err != nil {
			t.Fatalf("NewMLDSA44KeyPool: %+v", err)
		}
		pools[xpubs[i]] = pool
	}
	keysFile := &keys.File{ExtendedPublicKeys: xpubs, MinimumSignatures: m}
	if n > 1 {
		keysFile.MLDSA44Cosigners = pools
	} else {
		keysFile.MLDSA44 = pool
	}
	return w.finish(t, keysFile)
}

// newSchnorrWallet is a single-key Schnorr (P2PK) wallet.
func newSchnorrWallet(t *testing.T) *feeWallet {
	t.Helper()
	params := &dagconfig.MainnetParams
	mnemonic, err := libhtnwallet.CreateMnemonic()
	if err != nil {
		t.Fatalf("CreateMnemonic: %+v", err)
	}
	xpub, err := libhtnwallet.MasterPublicKeyFromMnemonic(params, mnemonic, false)
	if err != nil {
		t.Fatalf("MasterPublicKeyFromMnemonic: %+v", err)
	}
	w := &feeWallet{t: t, params: params, mnemonics: []string{mnemonic}}
	return w.finish(t, &keys.File{ExtendedPublicKeys: []string{xpub}, MinimumSignatures: 1})
}

func (w *feeWallet) finish(t *testing.T, keysFile *keys.File) *feeWallet {
	t.Helper()
	if err := keysFile.SetPath(w.params, filepath.Join(t.TempDir(), "keys.json"), true); err != nil {
		t.Fatalf("SetPath: %+v", err)
	}
	w.s = &server{
		params:           w.params,
		keysFile:         keysFile,
		addressSet:       make(walletAddressSet),
		txMassCalculator: txmass.NewCalculator(w.params.MassPerTxByte, w.params.MassPerScriptPubKeyByte, w.params.MassPerSigOp),
	}
	return w
}

func (w *feeWallet) address(t *testing.T, walletAddr *walletAddress) util.Address {
	t.Helper()
	var address util.Address
	var err error
	if walletAddr.mldsa44 {
		address, err = w.s.mldsa44Address(walletAddr)
	} else {
		address, err = libhtnwallet.Address(w.params, w.s.keysFile.ExtendedPublicKeys, w.s.keysFile.MinimumSignatures,
			w.s.walletAddressPath(walletAddr), false)
	}
	if err != nil {
		t.Fatalf("address: %+v", err)
	}
	return address
}

// fund replaces the wallet's coins with the given values, all held at walletAddr unless the coin is
// added afterwards with add. The first coin's outpoint index is firstIndex.
type coinSpec struct {
	walletAddr *walletAddress
	value      uint64
}

func (w *feeWallet) fund(t *testing.T, coins ...coinSpec) {
	t.Helper()
	w.s.utxosSortedByAmount = nil
	for i, coin := range coins {
		scriptPublicKey, err := txscript.PayToAddrScript(w.address(t, coin.walletAddr))
		if err != nil {
			t.Fatalf("PayToAddrScript: %+v", err)
		}
		w.s.utxosSortedByAmount = append(w.s.utxosSortedByAmount, &walletUTXO{
			Outpoint: &externalapi.DomainOutpoint{
				TransactionID: *externalapi.NewDomainTransactionIDFromByteArray(&[externalapi.DomainHashSize]byte{byte(i + 1)}),
			},
			UTXOEntry: utxo.NewUTXOEntry(coin.value, scriptPublicKey, false, 1),
			address:   coin.walletAddr,
		})
	}
}

func same(walletAddr *walletAddress, values ...uint64) []coinSpec {
	var coins []coinSpec
	for _, value := range values {
		coins = append(coins, coinSpec{walletAddr, value})
	}
	return coins
}

func repeat(value uint64, n int) []uint64 {
	values := make([]uint64, n)
	for i := range values {
		values[i] = value
	}
	return values
}

// result is a returned transaction, signed and measured.
type result struct {
	inputs    int
	inputSum  uint64
	outputs   []uint64
	fee, mass uint64
}

// check signs the single returned transaction, measures it, and asserts the properties every
// returned transaction must have: fee covers the actual signed mass at 1 sompi per mass unit, the
// standard mass limit holds, the estimate used to size the fee is not below the actual mass, and no
// output is below the 603 sompi P2SH dust threshold (all outputs in these tests are P2SH).
func (w *feeWallet) check(transactions [][]byte, err error) result {
	t := w.t
	t.Helper()
	if err != nil {
		t.Fatalf("builder failed: %+v", err)
	}
	if len(transactions) != 1 {
		t.Fatalf("got %d transactions, want 1", len(transactions))
	}
	partiallySigned, err := serialization.DeserializePartiallySignedTransaction(transactions[0])
	if err != nil {
		t.Fatalf("DeserializePartiallySignedTransaction: %+v", err)
	}
	estimate, err := w.s.estimateMassAfterSignatures(partiallySigned)
	if err != nil {
		t.Fatalf("estimateMassAfterSignatures: %+v", err)
	}
	signed, err := libhtnwallet.Sign(w.params, w.mnemonics[:w.minSigs()], transactions[0], false)
	if err != nil {
		t.Fatalf("Sign: %+v", err)
	}
	tx, err := libhtnwallet.ExtractTransaction(signed, false)
	if err != nil {
		t.Fatalf("ExtractTransaction: %+v", err)
	}
	r := result{inputs: len(tx.Inputs), mass: w.s.txMassCalculator.CalculateTransactionMass(tx)}
	for _, input := range partiallySigned.PartiallySignedInputs {
		r.inputSum += input.PrevOutput.Value
	}
	var outputSum uint64
	for _, output := range tx.Outputs {
		r.outputs = append(r.outputs, output.Value)
		outputSum += output.Value
		if output.Value < feeTestP2SHDust {
			t.Errorf("output of %d sompi is dust", output.Value)
		}
	}
	r.fee = r.inputSum - outputSum
	if estimate < r.mass {
		t.Errorf("estimated mass %d is below the signed transaction's %d", estimate, r.mass)
	}
	if r.mass > feeTestMaxMass {
		t.Errorf("mass %d exceeds the standard limit", r.mass)
	}
	// No returned transaction gives away a spendable remainder: the fee exceeds what its shape needs
	// by less than a dust output plus the fee the omitted change output would have cost (1,025).
	if r.fee > max(10_000*uint64(r.inputs), r.mass)+1_025 {
		t.Errorf("pays %d sompi for mass %d with %d inputs: far more than needed", r.fee, r.mass, r.inputs)
	}
	if r.fee < r.mass {
		t.Errorf("pays %d sompi for a transaction of signed mass %d: below the relay minimum", r.fee, r.mass)
	}
	return r
}

func (w *feeWallet) send(address util.Address, amount uint64, sendAll bool, from []*walletAddress,
	useExistingChange bool, payload []byte,
) ([][]byte, error) {
	return w.s.buildSendTransaction(address, amount, sendAll, from, useExistingChange, payload, feeTestVirtualDAAScore)
}

func wantOutputs(t *testing.T, r result, want ...uint64) {
	t.Helper()
	if fmt.Sprint(r.outputs) != fmt.Sprint(want) {
		t.Errorf("outputs %v, want %v", r.outputs, want)
	}
}

func wantFee(t *testing.T, r result, inputs int, fee, mass uint64) {
	t.Helper()
	if r.inputs != inputs || r.fee != fee || r.mass != mass {
		t.Errorf("got %d inputs, fee %d, mass %d; want %d inputs, fee %d, mass %d",
			r.inputs, r.fee, r.mass, inputs, fee, mass)
	}
}

// TestMinimumRelayFeeTable pins the fee rule against literal values: the mempool's, at the default
// rate of 1000 sompi per 1000 mass, with the rate itself as the fee when the quotient is zero.
func TestMinimumRelayFeeTable(t *testing.T) {
	s := &server{params: &dagconfig.MainnetParams}
	for mass, want := range map[uint64]uint64{0: 1000, 1: 1, 999: 999, 1000: 1000, 11_608: 11_608, 100_000: 100_000, 100_001: 100_001} {
		if got := s.minimumRelayFee(mass); got != want {
			t.Errorf("minimumRelayFee(%d) = %d, want %d", mass, got, want)
		}
	}
}

// TestIsDustBoundary pins the dust threshold for a P2SH output at 603 sompi.
func TestIsDustBoundary(t *testing.T) {
	w := newMLWallet(t, 3, 2)
	address := w.address(t, &walletAddress{index: 1, keyChain: libhtnwallet.ExternalKeychain, mldsa44: true})
	for value, want := range map[uint64]bool{0: true, 602: true, 603: false, 604: false} {
		if got, err := w.s.isDust(address, value); err != nil || got != want {
			t.Errorf("isDust(%d) = %t, %v; want %t", value, got, err, want)
		}
	}
}

// TestSendFeeCoversSignedMass signs what a send returns and checks it against its actual mass, for
// every address form: a spend whose flat fee (10,000 per input) covers its mass keeps that fee, and
// one that does not pays exactly its mass. An ML-DSA-44 2-of-3 spend weighs about 11.6k mass for one
// input and 54k for five, so the flat fee was refused by nodes.
func TestSendFeeCoversSignedMass(t *testing.T) {
	const coin = 1_000_000_000
	ml := &walletAddress{index: 1, keyChain: libhtnwallet.ExternalKeychain, mldsa44: true}
	p2pkh := &walletAddress{index: 1, keyChain: libhtnwallet.ExternalKeychain, mldsa44: true, mldsa44Form: libhtnwallet.MLDSA44AddressFormP2PKH}
	p2sh := &walletAddress{index: 1, keyChain: libhtnwallet.ExternalKeychain, mldsa44: true, mldsa44Form: libhtnwallet.MLDSA44AddressFormP2SH}
	schnorr := &walletAddress{index: 1, keyChain: libhtnwallet.ExternalKeychain}

	for _, test := range []struct {
		name   string
		wallet func(*testing.T) *feeWallet
		addr   *walletAddress
		// wantFlat is whether the flat fee covers the mass (for multisig, one input is the case that must not).
		wantFlat bool
	}{
		{"ML-DSA-44 2-of-2", func(t *testing.T) *feeWallet { return newMLWallet(t, 2, 2) }, ml, false},
		{"ML-DSA-44 2-of-3", func(t *testing.T) *feeWallet { return newMLWallet(t, 3, 2) }, ml, false},
		{"ML-DSA-44 2-of-11", func(t *testing.T) *feeWallet { return newMLWallet(t, 11, 2) }, ml, false},
		{"ML-DSA-44 single-key P2PKH", func(t *testing.T) *feeWallet { return newMLWallet(t, 1, 1) }, p2pkh, true},
		{"ML-DSA-44 single-key P2SH", func(t *testing.T) *feeWallet { return newMLWallet(t, 1, 1) }, p2sh, true},
		{"Schnorr P2PK", newSchnorrWallet, schnorr, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			w := test.wallet(t)
			for _, inputCount := range []int{1, 5} {
				w.fund(t, same(test.addr, repeat(coin, inputCount)...)...)
				address := w.address(t, test.addr)
				// Pay all but 50,000 sompi per input, so every coin is spent and change is ample.
				amount := uint64(inputCount)*coin - 50_000*uint64(inputCount)
				r := w.check(w.send(address, amount, false, nil, true, nil))
				if r.inputs != inputCount {
					t.Fatalf("spends %d inputs, want %d", r.inputs, inputCount)
				}
				flat := 10_000 * uint64(inputCount)
				if (test.wantFlat && flat < r.mass) || (!test.wantFlat && inputCount == 1 && flat >= r.mass) {
					t.Fatalf("flat fee %d against mass %d: fixture does not test what it should", flat, r.mass)
				}
				if want := max(flat, r.mass); r.fee != want {
					t.Errorf("%d input(s): fee %d, want max(flat %d, mass %d) = %d", inputCount, r.fee, flat, r.mass, want)
				}
				t.Logf("%d input(s): signed mass %d, fee %d", inputCount, r.mass, r.fee)
			}
		})
	}

	t.Run("send all", func(t *testing.T) {
		w := newMLWallet(t, 3, 2)
		w.fund(t, same(ml, coin, coin)...)
		r := w.check(w.send(w.address(t, ml), 0, true, nil, true, nil))
		wantFee(t, r, 2, 21_853, 21_853) // one output: 2 x 10,668 + 94 + 423
		wantOutputs(t, r, 2*coin-21_853)
	})
}

// TestSendFeeSelectionFixtures covers NXW-1: the fee is worked out per candidate input set, so a send
// that needs another coin neither fails nor overpays.
func TestSendFeeSelectionFixtures(t *testing.T) {
	ml := &walletAddress{index: 1, keyChain: libhtnwallet.ExternalKeychain, mldsa44: true}
	w := newMLWallet(t, 3, 2)
	address := w.address(t, ml)

	t.Run("second coin pays for itself", func(t *testing.T) {
		w.t = t
		w.fund(t, same(ml, 1_000_000, 12_000)...)
		r := w.check(w.send(address, 989_000, false, nil, true, nil))
		wantFee(t, r, 2, 22_276, 22_276)
		wantOutputs(t, r, 989_000, 724)
	})

	t.Run("no overcharge when the second coin is large", func(t *testing.T) {
		w.t = t
		w.fund(t, same(ml, 1_000_000, 100_000)...)
		r := w.check(w.send(address, 989_000, false, nil, true, nil))
		wantFee(t, r, 2, 22_276, 22_276)
		wantOutputs(t, r, 989_000, 88_724)
	})

	t.Run("small coins do not run into the mass limit", func(t *testing.T) {
		w.t = t
		w.fund(t, same(ml, append([]uint64{1_000_000}, repeat(11_677, 9)...)...)...)
		r := w.check(w.send(address, 989_000, false, nil, true, nil))
		if r.inputs > 3 {
			t.Errorf("spends %d inputs; a standard transaction with at most 3 exists", r.inputs)
		}
		// Two inputs leave 824 over the one-output fee of 21,853; the change would be 401 (dust), so
		// there is no change output and the residual is the fee.
		wantFee(t, r, 2, 22_677, 21_853)
		wantOutputs(t, r, 989_000)
	})

	t.Run("from-addresses keep a richer coin out", func(t *testing.T) {
		w.t = t
		other := &walletAddress{index: 2, keyChain: libhtnwallet.ExternalKeychain, mldsa44: true}
		w.fund(t, coinSpec{ml, 1_000_000}, coinSpec{ml, 12_000}, coinSpec{other, 50_000_000})
		r := w.check(w.send(address, 989_000, false, []*walletAddress{ml}, true, nil))
		wantFee(t, r, 2, 22_276, 22_276)
		wantOutputs(t, r, 989_000, 724)

		// Without the rich coin in reach the send is refused rather than reaching for it.
		_, err := w.send(address, 5_000_000, false, []*walletAddress{ml}, true, nil)
		if err == nil || !strings.Contains(err.Error(), "Insufficient funds") {
			t.Errorf("got %v, want insufficient funds", err)
		}
	})
}

// TestSendMixedInputsNeedMoreThanFiveCandidates covers NXW-2: a Schnorr 2-of-11 coin plus ML-DSA-44
// 2-of-11 coins needs four or five inputs, which the old five-attempt cap could not reach.
func TestSendMixedInputsNeedMoreThanFiveCandidates(t *testing.T) {
	w := newMLWallet(t, 11, 2)
	ml := &walletAddress{index: 1, keyChain: libhtnwallet.InternalKeychain, mldsa44: true}
	schnorr := &walletAddress{index: 1, keyChain: libhtnwallet.ExternalKeychain}
	w.fund(t, coinSpec{schnorr, 1_000_000}, coinSpec{ml, 19_700}, coinSpec{ml, 19_700}, coinSpec{ml, 19_700}, coinSpec{ml, 19_700})
	to := w.address(t, ml)
	indexBefore := w.s.keysFile.LastUsedInternalIndex()

	r := w.check(w.send(to, 989_900, false, []*walletAddress{ml, schnorr}, true, nil))
	if r.inputs > 5 {
		t.Errorf("spends %d inputs, want at most 5", r.inputs)
	}
	// Four inputs without change: mass 68,788, residual 412 over the fee 68,788 goes to the fee.
	wantFee(t, r, 4, 69_200, 68_788)
	wantOutputs(t, r, 989_900)
	if w.s.keysFile.LastUsedInternalIndex() != indexBefore {
		t.Errorf("an existing change address was requested, but the internal index moved")
	}
}

// TestSendDustAndChange covers NXW-3: no output is dust, a dust change becomes fee, the requested
// payment is never reduced, and a fresh change address is taken at most once.
func TestSendDustAndChange(t *testing.T) {
	ml := &walletAddress{index: 1, keyChain: libhtnwallet.ExternalKeychain, mldsa44: true}
	w := newMLWallet(t, 3, 2)
	address := w.address(t, ml)

	// One coin of 1,000,000: with change the fee is 11,608 (mass 11,608), without it 11,185 (mass).
	// change = 1,000,000 - amount - 11,608.
	for _, test := range []struct {
		change     uint64
		wantChange bool
	}{{0, false}, {602, false}, {603, true}, {604, true}} {
		t.Run(fmt.Sprintf("change %d", test.change), func(t *testing.T) {
			w.t = t
			w.fund(t, same(ml, 1_000_000)...)
			amount := 1_000_000 - 11_608 - test.change
			r := w.check(w.send(address, amount, false, nil, true, nil))
			if test.wantChange {
				wantFee(t, r, 1, 11_608, 11_608)
				wantOutputs(t, r, amount, test.change)
			} else {
				wantFee(t, r, 1, 11_608+test.change, 11_185)
				wantOutputs(t, r, amount)
			}
		})
	}

	t.Run("the 988,000 fixture pays the residual as fee", func(t *testing.T) {
		w.t = t
		w.fund(t, same(ml, 1_000_000)...)
		r := w.check(w.send(address, 988_000, false, nil, true, nil))
		wantFee(t, r, 1, 12_000, 11_185)
		wantOutputs(t, r, 988_000)
	})

	t.Run("send-all refuses a dust payment", func(t *testing.T) {
		w.t = t
		w.fund(t, same(ml, 11_500)...)
		_, err := w.send(address, 0, true, nil, true, nil)
		if err == nil || !strings.Contains(err.Error(), "dust") {
			t.Errorf("got %v, want a dust refusal", err)
		}
	})

	t.Run("a dust payment is refused, not sent", func(t *testing.T) {
		w.t = t
		w.fund(t, same(ml, 1_000_000)...)
		if _, err := w.send(address, 602, false, nil, true, nil); err == nil || !strings.Contains(err.Error(), "dust") {
			t.Errorf("got %v, want a dust refusal", err)
		}
	})

	t.Run("the payment is never reduced to fit", func(t *testing.T) {
		w.t = t
		// 11,000 over the amount cannot pay either shape's fee (11,608 / 11,185).
		w.fund(t, same(ml, 1_000_000)...)
		if _, err := w.send(address, 989_000, false, nil, true, nil); err == nil || !strings.Contains(err.Error(), "Insufficient funds") {
			t.Errorf("got %v, want insufficient funds", err)
		}
	})

	t.Run("a fresh change address is taken once across candidates", func(t *testing.T) {
		w.t = t
		w.fund(t, same(ml, append([]uint64{1_000_000}, repeat(11_677, 9)...)...)...)
		before := w.s.keysFile.LastUsedInternalIndex()
		w.check(w.send(address, 989_000, false, nil, false, nil))
		if got := w.s.keysFile.LastUsedInternalIndex(); got != before+1 {
			t.Errorf("internal index went from %d to %d, want one new address", before, got)
		}
	})
}

// TestSendMassLimitBoundary pins the standard mass limit at 100,000 versus 100,001, reached exactly
// with a payload, since every payload byte adds one mass unit.
func TestSendMassLimitBoundary(t *testing.T) {
	ml := &walletAddress{index: 1, keyChain: libhtnwallet.ExternalKeychain, mldsa44: true}
	w := newMLWallet(t, 3, 2)
	address := w.address(t, ml)
	w.fund(t, same(ml, repeat(1_000_000_000, 9)...)...)

	base := w.check(w.send(address, 0, true, nil, true, nil))
	payloadLen := int(feeTestMaxMass - base.mass)

	at := w.check(w.send(address, 0, true, nil, true, make([]byte, payloadLen)))
	if at.mass != feeTestMaxMass || at.fee < feeTestMaxMass {
		t.Errorf("at the limit: mass %d, fee %d; want mass 100000 and fee at least 100000", at.mass, at.fee)
	}
	_, err := w.send(address, 0, true, nil, true, make([]byte, payloadLen+1))
	if err == nil || !strings.Contains(err.Error(), "standard limit") {
		t.Errorf("mass 100,001: got %v, want the standard-limit refusal", err)
	}
}

// TestSendAmountGuards keeps the arithmetic guards of the coin selection this builder replaced: an
// amount near the uint64 limit and a send-all over coins worth less than their fee both used to wrap.
func TestSendAmountGuards(t *testing.T) {
	schnorr := &walletAddress{index: 1, keyChain: libhtnwallet.ExternalKeychain}
	w := newSchnorrWallet(t)
	address := w.address(t, schnorr)

	w.fund(t, same(schnorr, 5_000_000, 3_000_000)...)
	if _, err := w.send(address, math.MaxUint64-feePerInput+1, false, nil, true, nil); err == nil {
		t.Errorf("an amount near the uint64 limit was accepted")
	}
	r := w.check(w.send(address, 4_000_000, false, nil, true, nil))
	wantOutputs(t, r, 4_000_000, 1_000_000-10_000)

	w.fund(t, same(schnorr, 5_000, 2_500)...)
	if _, err := w.send(address, 0, true, nil, true, nil); err == nil {
		t.Errorf("a send-all over coins worth less than the fee was accepted")
	}
}

// TestCompoundFee covers NXW-3 and NXW-4 for the compound builder.
func TestCompoundFee(t *testing.T) {
	ml := &walletAddress{index: 1, keyChain: libhtnwallet.ExternalKeychain, mldsa44: true}
	w := newMLWallet(t, 3, 2)
	address := w.address(t, ml)

	compound := func(t *testing.T, values ...uint64) (result, []*libhtnwallet.UTXO, error) {
		t.Helper()
		w.t = t
		w.fund(t, same(ml, values...)...)
		var coins []*libhtnwallet.UTXO
		for _, c := range w.s.utxosSortedByAmount {
			coin, err := w.s.libhtnwalletUTXO(c.Outpoint, c.UTXOEntry, c.address)
			if err != nil {
				t.Fatalf("libhtnwalletUTXO: %+v", err)
			}
			coins = append(coins, coin)
		}
		transaction, spent, err := w.s.buildCompoundTransactionWithinStandardMass(coins, address)
		if err != nil {
			return result{}, nil, err
		}
		return w.check([][]byte{transaction}, nil), spent, nil
	}

	t.Run("three inputs", func(t *testing.T) {
		r, spent, err := compound(t, 1_000_000_000, 1_000_000_000, 1_000_000_000)
		if err != nil || len(spent) != 3 {
			t.Fatalf("got %d inputs, %v", len(spent), err)
		}
		wantFee(t, r, 3, 32_521, 32_521)
	})

	t.Run("a failed top-up drops inputs until the rest is funded", func(t *testing.T) {
		// Nine inputs pass the flat fee (94,000 > 90,000) but not the relay fee (96,529); eight leave a
		// dust payment (139); seven leave 2,807.
		r, spent, err := compound(t, append([]uint64{30_000}, repeat(8_000, 8)...)...)
		if err != nil {
			t.Fatalf("compound: %+v", err)
		}
		wantFee(t, r, 7, 75_193, 75_193)
		wantOutputs(t, r, 2_807)
		// The caller marks exactly these as used; the two dropped coins are not among them.
		if len(spent) != 7 {
			t.Errorf("returned %d spent coins, want 7", len(spent))
		}
	})

	t.Run("no viable subset", func(t *testing.T) {
		// 2 x 11,100: the payment would be 347 (dust); one input is not a compound.
		_, spent, err := compound(t, 11_100, 11_100)
		if err == nil || !strings.Contains(err.Error(), "no funded") || spent != nil {
			t.Errorf("got spent %v, error %v; want the no-viable-compound error", spent, err)
		}
	})
}

// TestSendRefusesChangeThatDoesNotFit covers the case where the payment alone fits the standard mass
// but a non-dust change output would not: the remainder must not be paid as fee.
func TestSendRefusesChangeThatDoesNotFit(t *testing.T) {
	const coin = 1_000_000_000

	t.Run("mixed single-key ML-DSA-44 and Schnorr coins", func(t *testing.T) {
		w := newMLWallet(t, 1, 1)
		p2pkh := &walletAddress{index: 1, keyChain: libhtnwallet.ExternalKeychain, mldsa44: true, mldsa44Form: libhtnwallet.MLDSA44AddressFormP2PKH}
		p2sh := &walletAddress{index: 1, keyChain: libhtnwallet.ExternalKeychain, mldsa44: true, mldsa44Form: libhtnwallet.MLDSA44AddressFormP2SH}
		schnorr := &walletAddress{index: 1, keyChain: libhtnwallet.ExternalKeychain}
		coins := same(p2pkh, repeat(coin, 20)...)
		coins = append(coins, same(schnorr, repeat(coin, 3)...)...)
		w.fund(t, coins...)
		_, err := w.send(w.address(t, p2sh), 22*coin, false, nil, true, nil)
		if err == nil || !strings.Contains(err.Error(), "standard limit") {
			t.Fatalf("got %v, want a refusal naming the standard mass limit", err)
		}
	})

	t.Run("payload takes the payment-only mass to exactly 100,000", func(t *testing.T) {
		ml := &walletAddress{index: 1, keyChain: libhtnwallet.ExternalKeychain, mldsa44: true}
		w := newMLWallet(t, 3, 2)
		address := w.address(t, ml)
		w.fund(t, same(ml, repeat(coin, 9)...)...)
		base := w.check(w.send(address, 0, true, nil, true, nil)) // one output
		payload := make([]byte, int(feeTestMaxMass-base.mass))
		_, err := w.send(address, 8*coin, false, nil, true, payload)
		if err == nil || !strings.Contains(err.Error(), "standard limit") {
			t.Fatalf("got %v, want a refusal naming the standard mass limit", err)
		}
		// With a remainder that is dust as change, the payment-only shape is still allowed.
		r := w.check(w.send(address, 9*coin-feeTestMaxMass-500, false, nil, true, payload))
		wantFee(t, r, 9, feeTestMaxMass+500, feeTestMaxMass)
	})
}
