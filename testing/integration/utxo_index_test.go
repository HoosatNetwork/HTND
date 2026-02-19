package integration

import (
	"encoding/hex"
	"testing"

	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/utxo"

	"github.com/Hoosat-Oy/HTND/app/appmessage"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/consensushashing"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/constants"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/transactionid"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/txscript"
	"github.com/Hoosat-Oy/HTND/util"
	"github.com/kaspanet/go-secp256k1"
)

func TestUTXOIndex(t *testing.T) {
	// Setup a single htnd instance
	harnessParams := &harnessParams{
		p2pAddress:              p2pAddress1,
		rpcAddress:              rpcAddress1,
		miningAddress:           miningAddress1,
		miningAddressPrivateKey: miningAddress1PrivateKey,
		utxoIndex:               true,
	}
	htnd, teardown := setupHarness(t, harnessParams)
	defer teardown()

	// skip the first block because it's paying to genesis script,
	// which contains no outputs
	mineNextBlock(t, htnd)

	coinSupplyBefore, err := htnd.rpcClient.GetCoinSupply()
	if err != nil {
		t.Fatalf("Error retrieving coin supply before mining: %s", err)
	}
	blockCountBefore, err := htnd.rpcClient.GetBlockCount()
	if err != nil {
		t.Fatalf("Error retrieving block count before mining: %s", err)
	}

	// Register for UTXO changes
	const blockAmountToMine = 100
	onUTXOsChangedChan := make(chan *appmessage.UTXOsChangedNotificationMessage, blockAmountToMine)
	err = htnd.rpcClient.RegisterForUTXOsChangedNotifications([]string{miningAddress1}, func(
		notification *appmessage.UTXOsChangedNotificationMessage) {

		onUTXOsChangedChan <- notification
	})
	if err != nil {
		t.Fatalf("Failed to register for UTXO change notifications: %s", err)
	}

	// Mine some blocks
	for range blockAmountToMine {
		mineNextBlock(t, htnd)
	}

	coinSupplyAfter, err := htnd.rpcClient.GetCoinSupply()
	if err != nil {
		t.Fatalf("Error retrieving coin supply after mining: %s", err)
	}
	blockCountAfter, err := htnd.rpcClient.GetBlockCount()
	if err != nil {
		t.Fatalf("Error retrieving block count after mining: %s", err)
	}
	if blockCountAfter.BlockCount-blockCountBefore.BlockCount != blockAmountToMine {
		t.Fatalf("Unexpected block count delta. Want: %d, got: %d",
			blockAmountToMine, blockCountAfter.BlockCount-blockCountBefore.BlockCount)
	}

	// Collect the UTXO and make sure there's nothing in Removed
	// Note that we expect blockAmountToMine-1 messages because
	// the last block won't be accepted until the next block is
	// mined
	var notificationEntries []*appmessage.UTXOsByAddressesEntry
	var sumAddedSompi uint64
	for range blockAmountToMine {
		notification := <-onUTXOsChangedChan
		if len(notification.Removed) > 0 {
			t.Fatalf("Unexpectedly received that a UTXO has been removed")
		}
		notificationEntries = append(notificationEntries, notification.Added...)
		for _, added := range notification.Added {
			sumAddedSompi += added.UTXOEntry.Amount
		}
	}

	if coinSupplyAfter.CirculatingSompi <= coinSupplyBefore.CirculatingSompi {
		t.Fatalf("Expected circulating supply to increase after mining. Before: %d, after: %d",
			coinSupplyBefore.CirculatingSompi, coinSupplyAfter.CirculatingSompi)
	}
	deltaCirculatingSompi := coinSupplyAfter.CirculatingSompi - coinSupplyBefore.CirculatingSompi
	if deltaCirculatingSompi < sumAddedSompi {
		t.Fatalf("Coin supply delta is smaller than mined UTXOs observed via notifications. Delta: %d, mined-to-address: %d",
			deltaCirculatingSompi, sumAddedSompi)
	}

	// Submit a few transactions that spends some UTXOs
	const transactionAmountToSpend = 5
	for i := range transactionAmountToSpend {
		rpcTransaction, transactionID := buildTransactionForUTXOIndexTest(t, notificationEntries[i])
		_, err = htnd.rpcClient.SubmitTransaction(rpcTransaction, transactionID, false)
		if err != nil {
			t.Fatalf("Error submitting transaction: %s", err)
		}
	}

	// Mine a block to include the above transactions
	mineNextBlock(t, htnd)

	// Make sure this block removed the UTXOs we spent
	notification := <-onUTXOsChangedChan
	if len(notification.Removed) != transactionAmountToSpend {
		t.Fatalf("Unexpected amount of removed UTXOs. Want: %d, got: %d",
			transactionAmountToSpend, len(notification.Removed))
	}
	for i := range transactionAmountToSpend {
		entry := notificationEntries[i]

		found := false
		for _, removed := range notification.Removed {
			if *removed.Outpoint == *entry.Outpoint {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("Missing entry amongst removed UTXOs: %s:%d",
				entry.Outpoint.TransactionID, entry.Outpoint.Index)
		}
	}
	notificationEntries = append(notificationEntries, notification.Added...)

	// Remove the UTXOs we spent from `notificationEntries`
	notificationEntries = notificationEntries[transactionAmountToSpend:]

	// Get all the UTXOs and make sure the response is equivalent
	// to the data collected via notifications
	utxosByAddressesResponse, err := htnd.rpcClient.GetUTXOsByAddresses([]string{miningAddress1})
	if err != nil {
		t.Fatalf("Failed to get UTXOs: %s", err)
	}
	if len(notificationEntries) != len(utxosByAddressesResponse.Entries) {
		t.Fatalf("Unexpected amount of UTXOs. Want: %d, got: %d",
			len(notificationEntries), len(utxosByAddressesResponse.Entries))
	}
	for _, notificationEntry := range notificationEntries {
		var foundResponseEntry *appmessage.UTXOsByAddressesEntry
		for _, responseEntry := range utxosByAddressesResponse.Entries {
			if *notificationEntry.Outpoint == *responseEntry.Outpoint {
				foundResponseEntry = responseEntry
				break
			}
		}
		if foundResponseEntry == nil {
			t.Fatalf("Missing entry in UTXOs response: %s:%d",
				notificationEntry.Outpoint.TransactionID, notificationEntry.Outpoint.Index)
		}
		if notificationEntry.UTXOEntry.Amount != foundResponseEntry.UTXOEntry.Amount {
			t.Fatalf("Unexpected UTXOEntry for outpoint %s:%d. Want: %+v, got: %+v",
				notificationEntry.Outpoint.TransactionID, notificationEntry.Outpoint.Index,
				notificationEntry.UTXOEntry, foundResponseEntry.UTXOEntry)
		}
		if notificationEntry.UTXOEntry.BlockDAAScore != foundResponseEntry.UTXOEntry.BlockDAAScore {
			t.Fatalf("Unexpected UTXOEntry for outpoint %s:%d. Want: %+v, got: %+v",
				notificationEntry.Outpoint.TransactionID, notificationEntry.Outpoint.Index,
				notificationEntry.UTXOEntry, foundResponseEntry.UTXOEntry)
		}
		if notificationEntry.UTXOEntry.IsCoinbase != foundResponseEntry.UTXOEntry.IsCoinbase {
			t.Fatalf("Unexpected UTXOEntry for outpoint %s:%d. Want: %+v, got: %+v",
				notificationEntry.Outpoint.TransactionID, notificationEntry.Outpoint.Index,
				notificationEntry.UTXOEntry, foundResponseEntry.UTXOEntry)
		}
		if *notificationEntry.UTXOEntry.ScriptPublicKey != *foundResponseEntry.UTXOEntry.ScriptPublicKey {
			t.Fatalf("Unexpected UTXOEntry for outpoint %s:%d. Want: %+v, got: %+v",
				notificationEntry.Outpoint.TransactionID, notificationEntry.Outpoint.Index,
				notificationEntry.UTXOEntry, foundResponseEntry.UTXOEntry)
		}
	}
}

func buildTransactionForUTXOIndexTest(t *testing.T, entry *appmessage.UTXOsByAddressesEntry) (*appmessage.RPCTransaction, string) {
	transactionIDBytes, err := hex.DecodeString(entry.Outpoint.TransactionID)
	if err != nil {
		t.Fatalf("Error decoding transaction ID: %s", err)
	}
	transactionID, err := transactionid.FromBytes(transactionIDBytes)
	if err != nil {
		t.Fatalf("Error decoding transaction ID: %s", err)
	}

	txIns := make([]*appmessage.TxIn, 1)
	txIns[0] = appmessage.NewTxIn(appmessage.NewOutpoint(transactionID, entry.Outpoint.Index), []byte{}, 0, 1)

	payeeAddress, err := util.DecodeAddress(miningAddress1, util.Bech32PrefixHoosatSim)
	if err != nil {
		t.Fatalf("Error decoding payeeAddress: %+v", err)
	}
	toScript, err := txscript.PayToAddrScript(payeeAddress)
	if err != nil {
		t.Fatalf("Error generating script: %+v", err)
	}

	txOuts := []*appmessage.TxOut{appmessage.NewTxOut(entry.UTXOEntry.Amount-1000, toScript)}

	fromScriptCode, err := hex.DecodeString(entry.UTXOEntry.ScriptPublicKey.Script)
	if err != nil {
		t.Fatalf("Error decoding script public key: %s", err)
	}
	fromScript := &externalapi.ScriptPublicKey{Script: fromScriptCode, Version: 0}
	fromAmount := entry.UTXOEntry.Amount

	msgTx := appmessage.NewNativeMsgTx(constants.MaxTransactionVersion, txIns, txOuts)

	privateKeyBytes, err := hex.DecodeString(miningAddress1PrivateKey)
	if err != nil {
		t.Fatalf("Error decoding private key: %+v", err)
	}
	privateKey, err := secp256k1.DeserializeSchnorrPrivateKeyFromSlice(privateKeyBytes)
	if err != nil {
		t.Fatalf("Error deserializing private key: %+v", err)
	}

	tx := appmessage.MsgTxToDomainTransaction(msgTx)
	tx.Inputs[0].UTXOEntry = utxo.NewUTXOEntry(fromAmount, fromScript, false, 500)

	signatureScript, err := txscript.SignatureScript(tx, 0, consensushashing.SigHashAll, privateKey,
		&consensushashing.SighashReusedValues{})
	if err != nil {
		t.Fatalf("Error signing transaction: %+v", err)
	}
	msgTx.TxIn[0].SignatureScript = signatureScript

	domainTransaction := appmessage.MsgTxToDomainTransaction(msgTx)
	return appmessage.DomainTransactionToRPCTransaction(domainTransaction), consensushashing.TransactionID(domainTransaction).String()
}
