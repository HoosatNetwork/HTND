package rpccontext

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/txscript"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/utxo"
	"github.com/HoosatNetwork/HTND/domain/dagconfig"
	"github.com/HoosatNetwork/HTND/domain/utxoindex"
)

// TestBroadcastUTXOsChangedHandlesScriptsWithoutAddress pins that a listener receiving every UTXO
// change (subscribed without addresses) can be sent changes whose script has no single address. A
// bare multisig output classifies as standard but extracts to a nil address, and an output script
// that does not parse fails extraction outright; either one used to take the node down, because the
// conversion runs in the consensus events handler, which panics on errors and on panics alike.
func TestBroadcastUTXOsChangedHandlesScriptsWithoutAddress(t *testing.T) {
	bareMultiSig := append([]byte{txscript.Op2, txscript.OpData32}, bytes.Repeat([]byte{0x01}, 32)...)
	bareMultiSig = append(bareMultiSig, txscript.OpData32)
	bareMultiSig = append(bareMultiSig, bytes.Repeat([]byte{0x02}, 32)...)
	bareMultiSig = append(bareMultiSig, txscript.Op2, txscript.OpCheckMultiSig)
	if class := txscript.GetScriptClass(bareMultiSig); class != txscript.MultiSigTy {
		t.Fatalf("test script should classify as %s, got %s", txscript.MultiSigTy, class)
	}

	tests := []struct {
		name   string
		script []byte
	}{
		{name: "bare multisig", script: bareMultiSig},
		{name: "unparseable script", script: []byte{txscript.OpData32, 0x01}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			listener := &NotificationListener{
				params:                             &dagconfig.MainnetParams,
				propagateUTXOsChangedNotifications: true,
				propagateUTXOsChangedNotificationAddresses: map[utxoindex.ScriptPublicKeyString]*UTXOsChangedNotificationAddress{},
			}
			changes := &utxoindex.UTXOChanges{
				Added: []utxoindex.UTXOPair{{
					Outpoint: externalapi.DomainOutpoint{Index: 0},
					Entry:    utxo.NewUTXOEntry(1000, &externalapi.ScriptPublicKey{Script: test.script}, false, 1),
				}},
			}

			var err error
			func() {
				defer func() {
					if recovered := recover(); recovered != nil {
						err = fmt.Errorf("panic: %v", recovered)
					}
				}()
				_, err = listener.convertUTXOChangesToUTXOsChangedNotification(changes)
			}()
			if err != nil {
				t.Fatalf("converting a change without a single address failed: %v", err)
			}
		})
	}
}
