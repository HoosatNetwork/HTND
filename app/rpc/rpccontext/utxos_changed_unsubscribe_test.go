package rpccontext

import (
	"testing"

	"github.com/HoosatNetwork/HTND/domain/dagconfig"
	"github.com/HoosatNetwork/HTND/domain/utxoindex"
)

// TestStopPropagatingLastUTXOsChangedAddress pins what removing addresses does to a UTXOsChanged
// subscription. An empty address set means "every change", so removing a listener's last address
// used to turn a subscription for a few addresses into one for the whole network.
func TestStopPropagatingLastUTXOsChangedAddress(t *testing.T) {
	nm := NewNotificationManager(&dagconfig.MainnetParams)
	newListener := func() *NotificationListener {
		return &NotificationListener{params: &dagconfig.MainnetParams}
	}
	address := func(name string) *UTXOsChangedNotificationAddress {
		return &UTXOsChangedNotificationAddress{Address: name, ScriptPublicKeyString: utxoindex.ScriptPublicKeyString(name)}
	}

	t.Run("removing the last address stops the subscription", func(t *testing.T) {
		listener := newListener()
		a := address("a")
		nm.PropagateUTXOsChangedNotifications(listener, []*UTXOsChangedNotificationAddress{a})
		nm.StopPropagatingUTXOsChangedNotifications(listener, []*UTXOsChangedNotificationAddress{a})

		if listener.propagateUTXOsChangedNotifications {
			t.Fatalf("listener with no addresses left should no longer receive UTXOsChanged notifications")
		}
	})

	t.Run("removing some addresses keeps filtering by the rest", func(t *testing.T) {
		listener := newListener()
		a, b := address("a"), address("b")
		nm.PropagateUTXOsChangedNotifications(listener, []*UTXOsChangedNotificationAddress{a, b})
		nm.StopPropagatingUTXOsChangedNotifications(listener, []*UTXOsChangedNotificationAddress{a})

		if !listener.propagateUTXOsChangedNotifications {
			t.Fatalf("listener should still receive notifications for its remaining address")
		}
		if len(listener.propagateUTXOsChangedNotificationAddresses) != 1 {
			t.Fatalf("expected 1 remaining address, got %d", len(listener.propagateUTXOsChangedNotificationAddresses))
		}
	})

	t.Run("an explicit subscription without addresses stays a subscription to everything", func(t *testing.T) {
		listener := newListener()
		nm.PropagateUTXOsChangedNotifications(listener, nil)
		nm.StopPropagatingUTXOsChangedNotifications(listener, nil)

		if !listener.propagateUTXOsChangedNotifications {
			t.Fatalf("subscribing without addresses should keep receiving every change")
		}
	})
}
