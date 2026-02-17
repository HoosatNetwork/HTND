package utxo

import (
	"slices"

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
			Script:  slices.Clone(scriptPubKey.Script),
			Version: scriptPubKey.Version,
		},
		blockDAAScore: blockDAAScore,
		isCoinbase:    isCoinbase,
	}
}

func (u *utxoEntry) Amount() uint64 {
	return u.amount
}

func (u *utxoEntry) ScriptPublicKey() *externalapi.ScriptPublicKey {
	return &externalapi.ScriptPublicKey{
		Script:  slices.Clone(u.scriptPublicKey.Script),
		Version: u.scriptPublicKey.Version,
	}
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

	if u.Amount() != other.Amount() {
		return false
	}

	if !u.ScriptPublicKey().Equal(other.ScriptPublicKey()) {
		return false
	}

	if u.BlockDAAScore() != other.BlockDAAScore() {
		return false
	}

	if u.IsCoinbase() != other.IsCoinbase() {
		return false
	}

	return true
}
