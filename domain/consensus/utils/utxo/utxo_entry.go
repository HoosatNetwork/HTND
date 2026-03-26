package utxo

import (
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
)

type utxoEntry struct {
	amount          uint64
	scriptPublicKey *externalapi.ScriptPublicKey
	blockDAAScore   uint64
	isCoinbase      bool
}

// NewUTXOEntry creates a new utxoEntry representing the given txOut
func NewUTXOEntry(
	amount uint64,
	scriptPubKey *externalapi.ScriptPublicKey,
	isCoinbase bool,
	blockDAAScore uint64,
) externalapi.UTXOEntry {
	return &utxoEntry{
		amount: amount,
		scriptPublicKey: &externalapi.ScriptPublicKey{
			Script:  scriptPubKey.Script,
			Version: scriptPubKey.Version,
		},
		blockDAAScore: blockDAAScore,
		isCoinbase:    isCoinbase,
	}
}

func (u *utxoEntry) Amount() uint64 {
	return u.amount
}

// ScriptPublicKey returns the script public key of this UTXO entry.
// The returned value MUST NOT be mutated by callers.
func (u *utxoEntry) ScriptPublicKey() *externalapi.ScriptPublicKey {
	return u.scriptPublicKey
}

func (u *utxoEntry) BlockDAAScore() uint64 {
	return u.blockDAAScore
}

func (u *utxoEntry) IsCoinbase() bool {
	return u.isCoinbase
}

// Equal returns whether entry equals to other
func (u *utxoEntry) Equal(other externalapi.UTXOEntry) bool {
	if u == nil || other == nil {
		return u == other
	}

	// If only the underlying value of other is nil it'll
	// make `other == nil` return false, so we check it
	// explicitly.
	downcastedOther := other.(*utxoEntry)
	if u == nil || downcastedOther == nil {
		return u == downcastedOther
	}

	if u.amount != downcastedOther.amount {
		return false
	}

	if !u.scriptPublicKey.Equal(downcastedOther.scriptPublicKey) {
		return false
	}

	if u.blockDAAScore != downcastedOther.blockDAAScore {
		return false
	}

	if u.isCoinbase != downcastedOther.isCoinbase {
		return false
	}

	return true
}
