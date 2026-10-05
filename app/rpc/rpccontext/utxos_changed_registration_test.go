package rpccontext

import (
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/txscript"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/utxo"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
	"github.com/HoosatNetwork/HTND/v2/domain/utxoindex"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/config"
	"github.com/HoosatNetwork/HTND/v2/util"
)

// TestUTXOsChangedNotificationReachesRegisteredAddress pins that a UTXO paid to an address registered through
// NotifyUTXOsChanged is delivered to that listener. Registration keyed the address by the hex text of its script
// while the notification filter looks changes up by the script's raw String() form, so the lookup never matched
// and address-filtered UTXOsChanged notifications were silently dropped for every subscriber.
func TestUTXOsChangedNotificationReachesRegisteredAddress(t *testing.T) {
	params := &dagconfig.MainnetParams
	ctx := &Context{Config: &config.Config{Flags: &config.Flags{
		NetworkFlags: config.NetworkFlags{ActiveNetParams: params},
	}}}

	publicKey := make([]byte, 32)
	for i := range publicKey {
		publicKey[i] = byte(i + 1)
	}
	address, err := util.NewAddressPublicKey(publicKey, params.Prefix)
	if err != nil {
		t.Fatalf("NewAddressPublicKey: %+v", err)
	}
	scriptPublicKey, err := txscript.PayToAddrScript(address)
	if err != nil {
		t.Fatalf("PayToAddrScript: %+v", err)
	}

	addresses, err := ctx.ConvertAddressStringsToUTXOsChangedNotificationAddresses([]string{address.String()})
	if err != nil {
		t.Fatalf("ConvertAddressStringsToUTXOsChangedNotificationAddresses: %+v", err)
	}
	notificationManager := NewNotificationManager(params)
	listener := newNotificationListener(params)
	notificationManager.PropagateUTXOsChangedNotifications(listener, addresses)

	pair := utxoindex.UTXOPair{
		Outpoint: externalapi.DomainOutpoint{Index: 3},
		Entry:    utxo.NewUTXOEntry(1000, scriptPublicKey, true, 42),
	}
	notification, err := listener.convertUTXOChangesToUTXOsChangedNotification(&utxoindex.UTXOChanges{
		Added:   []utxoindex.UTXOPair{pair},
		Removed: []utxoindex.UTXOPair{pair},
	})
	if err != nil {
		t.Fatalf("convertUTXOChangesToUTXOsChangedNotification: %+v", err)
	}
	if len(notification.Added) != 1 || len(notification.Removed) != 1 {
		t.Fatalf("a change for the registered address was not delivered: got %d added and %d removed, want 1 and 1",
			len(notification.Added), len(notification.Removed))
	}
	if notification.Added[0].Address != address.String() {
		t.Fatalf("delivered address %q, want %q", notification.Added[0].Address, address.String())
	}
}
