package server

import (
	"github.com/HoosatNetwork/HTND/v2/cmd/htnwallet/keys"
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
	// mldsa44 marks the ML-DSA-44 address at this index. It shares the derivation path with the
	// secp256k1 addresses at the same index but is a different key, so it must not compare equal to
	// them - otherwise --from-address on one would select the other's coins too.
	mldsa44 bool
}
