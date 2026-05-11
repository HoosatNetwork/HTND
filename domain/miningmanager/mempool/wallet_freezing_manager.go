package mempool

import (
	"bytes"
	"sync"

	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/txscript"
	"github.com/Hoosat-Oy/HTND/util"
)

// walletFreezingManager handles frozen wallet address management and checking
type walletFreezingManager struct {
	config        *Config
	frozenWallets map[string]bool
	mutex         sync.RWMutex
}

// newWalletFreezingManager creates a new wallet freezing manager
func newWalletFreezingManager(config *Config) *walletFreezingManager {
	wfm := &walletFreezingManager{
		config:        config,
		frozenWallets: make(map[string]bool),
		mutex:         sync.RWMutex{},
	}

	// Initialize with addresses from config
	wfm.mutex.Lock()
	for _, address := range config.FrozenAddresses {
		wfm.frozenWallets[address] = true
	}
	wfm.mutex.Unlock()

	return wfm
}

// extractAddressesFromTransaction extracts all addresses involved in a transaction
func (wfm *walletFreezingManager) extractAddressesFromTransaction(transaction *externalapi.DomainTransaction) []string {
	if wfm == nil || wfm.config == nil || wfm.config.DAGParams == nil || transaction == nil {
		return nil
	}

	addresses := make(map[string]bool) // Use map to avoid duplicates

	// Extract addresses from inputs (sender addresses)
	for _, input := range transaction.Inputs {
		if input.UTXOEntry == nil {
			continue
		}
		scriptPublicKey := input.UTXOEntry.ScriptPublicKey()
		if scriptPublicKey == nil {
			continue
		}

		scriptClass, extractedAddress, err := txscript.ExtractScriptPubKeyAddress(scriptPublicKey, wfm.config.DAGParams)
		if err != nil || extractedAddress == nil {
			continue
		}
		addresses[extractedAddress.EncodeAddress()] = true

		// For P2SH spends, also try to extract the underlying redeemScript address (when available)
		// so freezing either representation prevents bypass.
		if scriptClass != txscript.ScriptHashTy {
			continue
		}
		scriptHashAddr, ok := extractedAddress.(*util.AddressScriptHash)
		if !ok || scriptHashAddr == nil {
			continue
		}
		pushes, err := txscript.PushedData(input.SignatureScript)
		if err != nil || len(pushes) == 0 {
			continue
		}
		redeemScript := pushes[len(pushes)-1]
		if redeemScript == nil {
			continue
		}
		redeemHash := util.HashBlake2b(redeemScript)
		if !bytes.Equal(redeemHash, scriptHashAddr.ScriptAddress()) {
			continue
		}
		redeemSPK := &externalapi.ScriptPublicKey{Script: redeemScript, Version: scriptPublicKey.Version}
		_, innerAddr, err := txscript.ExtractScriptPubKeyAddress(redeemSPK, wfm.config.DAGParams)
		if err != nil || innerAddr == nil {
			continue
		}
		addresses[innerAddr.EncodeAddress()] = true
	}

	// Extract addresses from outputs (recipient addresses)
	for _, output := range transaction.Outputs {
		if output.ScriptPublicKey == nil {
			continue
		}
		_, extractedAddress, err := txscript.ExtractScriptPubKeyAddress(output.ScriptPublicKey, wfm.config.DAGParams)
		if err != nil || extractedAddress == nil {
			continue
		}
		addresses[extractedAddress.EncodeAddress()] = true
	}

	// Convert map keys to slice
	result := make([]string, 0, len(addresses))
	for addr := range addresses {
		result = append(result, addr)
	}
	return result
}

// isWalletFrozen checks if any address in the transaction is frozen
func (wfm *walletFreezingManager) isWalletFrozen(transaction *externalapi.DomainTransaction) (bool, []string) {
	if wfm == nil || wfm.config == nil || !wfm.config.WalletFreezingEnabled {
		return false, nil
	}

	addresses := wfm.extractAddressesFromTransaction(transaction)
	frozenAddresses := make([]string, 0)

	wfm.mutex.RLock()
	defer wfm.mutex.RUnlock()

	for _, address := range addresses {
		if wfm.frozenWallets[address] {
			frozenAddresses = append(frozenAddresses, address)
		}
	}

	return len(frozenAddresses) > 0, frozenAddresses
}

// freezeWallet adds an address to the frozen wallets list
func (wfm *walletFreezingManager) freezeWallet(address string) {
	wfm.mutex.Lock()
	defer wfm.mutex.Unlock()
	wfm.frozenWallets[address] = true
}

// unfreezeWallet removes an address from the frozen wallets list
func (wfm *walletFreezingManager) unfreezeWallet(address string) {
	wfm.mutex.Lock()
	defer wfm.mutex.Unlock()
	delete(wfm.frozenWallets, address)
}

// getFrozenWallets returns a list of all frozen wallet addresses
func (wfm *walletFreezingManager) getFrozenWallets() []string {
	wfm.mutex.RLock()
	defer wfm.mutex.RUnlock()

	addresses := make([]string, 0, len(wfm.frozenWallets))
	for address := range wfm.frozenWallets {
		addresses = append(addresses, address)
	}
	return addresses
}

// isFrozen checks if a specific address is frozen
func (wfm *walletFreezingManager) isFrozen(address string) bool {
	if !wfm.config.WalletFreezingEnabled {
		return false
	}

	wfm.mutex.RLock()
	defer wfm.mutex.RUnlock()
	return wfm.frozenWallets[address]
}
