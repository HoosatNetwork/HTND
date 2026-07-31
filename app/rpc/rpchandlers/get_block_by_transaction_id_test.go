package rpchandlers

import (
	"testing"

	"github.com/HoosatNetwork/HTND/app/appmessage"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/domain/dagconfig"
)

func TestHandleGetBlockByTransactionID(t *testing.T) {
	// Test with a valid transaction ID from the genesis block
	genesisBlock := dagconfig.MainnetParams.GenesisBlock
	genesisTx := genesisBlock.Transactions[0] // First transaction is coinbase
	genesisTxID := consensushashing.TransactionID(genesisTx)

	// Create request message
	request := appmessage.NewGetBlockByTransactionIDRequestMessage(genesisTxID.String(), true)

	// Verify the request was created correctly
	if request.TransactionID != genesisTxID.String() {
		t.Errorf("Expected transaction ID %s, got %s", genesisTxID.String(), request.TransactionID)
	}

	if !request.IncludeTransactions {
		t.Error("Expected IncludeTransactions to be true")
	}

	// Test response message creation
	response := appmessage.NewGetBlockByTransactionIDResponseMessage()
	if response.Block != nil {
		t.Error("Expected Block to be nil initially")
	}

	if response.Error != nil {
		t.Error("Expected Error to be nil initially")
	}

	// Test command methods
	if request.Command() != appmessage.CmdGetBlockByTransactionIDRequestMessage {
		t.Errorf("Expected command %v, got %v", appmessage.CmdGetBlockByTransactionIDRequestMessage, request.Command())
	}

	if response.Command() != appmessage.CmdGetBlockByTransactionIDResponseMessage {
		t.Errorf("Expected command %v, got %v", appmessage.CmdGetBlockByTransactionIDResponseMessage, response.Command())
	}
}

func TestGetBlockByTransactionIDRequestMessage(t *testing.T) {
	// Test with different parameters
	txID := "test-transaction-id"
	includeTx := false

	request := appmessage.NewGetBlockByTransactionIDRequestMessage(txID, includeTx)

	if request.TransactionID != txID {
		t.Errorf("Expected TransactionID %s, got %s", txID, request.TransactionID)
	}

	if request.IncludeTransactions != includeTx {
		t.Errorf("Expected IncludeTransactions %v, got %v", includeTx, request.IncludeTransactions)
	}
}
