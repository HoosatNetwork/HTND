package server

import (
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/keys"
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/libhtnwallet"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
)

type walletUTXO struct {
	Outpoint  *externalapi.DomainOutpoint
	UTXOEntry externalapi.UTXOEntry
	address   *walletAddress
}

// walletAddress is an address the wallet holds the key of: one derived from its own extended public keys
// at index in keyChain, or, when imported is set, an imported key's address (and the other fields are
// zero).
type walletAddress struct {
	index         uint32
	cosignerIndex uint32
	keyChain      uint8
	imported      *keys.ImportedKey
}

// libhtnwalletUTXO returns the UTXO in the form libhtnwallet builds a transaction from: what spends it,
// and where that key is.
func (s *server) libhtnwalletUTXO(utxo *walletUTXO) *libhtnwallet.UTXO {
	if utxo.address.imported != nil {
		return &libhtnwallet.UTXO{
			Outpoint:                  utxo.Outpoint,
			UTXOEntry:                 utxo.UTXOEntry,
			DerivationPath:            libhtnwallet.ImportedKeyDerivationPath,
			ImportedExtendedPublicKey: utxo.address.imported.ExtendedPublicKey,
		}
	}
	return &libhtnwallet.UTXO{
		Outpoint:       utxo.Outpoint,
		UTXOEntry:      utxo.UTXOEntry,
		DerivationPath: s.walletAddressPath(utxo.address),
	}
}
