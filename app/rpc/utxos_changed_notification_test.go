package rpc

import (
	"testing"

	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
	"github.com/HoosatNetwork/HTND/v2/app/rpc/rpccontext"
	"github.com/HoosatNetwork/HTND/v2/domain"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	consensusutxo "github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/utxo"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
	"github.com/HoosatNetwork/HTND/v2/domain/utxoindex"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database/pebble"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter/router"
)

// emptyVirtualConsensus is a consensus whose virtual has one parent and no UTXOs, which is all utxoindex.New asks of it.
type emptyVirtualConsensus struct {
	externalapi.Consensus
}

func (emptyVirtualConsensus) GetVirtualInfo() (*externalapi.VirtualInfo, error) {
	return &externalapi.VirtualInfo{ParentHashes: []*externalapi.DomainHash{testHash(1)}}, nil
}

func (emptyVirtualConsensus) GetVirtualUTXOs([]*externalapi.DomainHash, *externalapi.DomainOutpoint, int,
) ([]*externalapi.OutpointAndUTXOEntryPair, error) {
	return nil, nil
}

type emptyVirtualDomain struct {
	domain.Domain
}

func (emptyVirtualDomain) Consensus() externalapi.Consensus { return emptyVirtualConsensus{} }

func testHash(b byte) *externalapi.DomainHash {
	return externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{b})
}

// TestNotifyUTXOsChangedOnlyForSubscribers pins how Manager.notifyUTXOsChanged uses the UTXO index's lazily built change
// list. With nobody subscribed to UTXO changes, the index is still updated but nothing is notified, and a client that
// subscribes afterwards is not sent the change it missed. With a subscriber, it receives exactly the notification it
// received before the change list became optional.
func TestNotifyUTXOsChangedOnlyForSubscribers(t *testing.T) {
	db, err := pebble.NewPebbleDB(t.TempDir(), 8)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	utxoIndex, err := utxoindex.New(emptyVirtualDomain{}, db)
	if err != nil {
		t.Fatalf("utxoindex.New: %+v", err)
	}

	notificationManager := rpccontext.NewNotificationManager(&dagconfig.MainnetParams)
	manager := &Manager{context: &rpccontext.Context{NotificationManager: notificationManager, UTXOIndex: utxoIndex}}

	script := &externalapi.ScriptPublicKey{Script: []byte{0x51, 0x2f, 0x52}, Version: 0}
	outpoint := func(index uint32) externalapi.DomainOutpoint {
		return externalapi.DomainOutpoint{
			TransactionID: *externalapi.NewDomainTransactionIDFromByteArray(&[externalapi.DomainHashSize]byte{7}),
			Index:         index,
		}
	}
	addUTXO := func(index uint32, parent byte) *externalapi.VirtualChangeSet {
		added := consensusutxo.NewUTXOCollection(map[externalapi.DomainOutpoint]externalapi.UTXOEntry{
			outpoint(index): consensusutxo.NewUTXOEntry(1000+uint64(index), script, false, 100),
		})
		diff, err := consensusutxo.NewUTXODiffFromCollections(added, consensusutxo.NewUTXOCollection(nil))
		if err != nil {
			t.Fatalf("NewUTXODiffFromCollections: %+v", err)
		}
		return &externalapi.VirtualChangeSet{VirtualUTXODiff: diff, VirtualParents: []*externalapi.DomainHash{testHash(parent)}}
	}
	hasUTXOs := func() bool {
		has, err := utxoIndex.HasUTXOs(script)
		if err != nil {
			t.Fatalf("HasUTXOs: %+v", err)
		}
		return has
	}

	listenerRouter := router.NewRouter("utxos changed notification test")
	notificationManager.AddListener(listenerRouter)
	listener, err := notificationManager.Listener(listenerRouter)
	if err != nil {
		t.Fatalf("Listener: %+v", err)
	}
	// A subscriber of another kind of notification, so that the lack of UTXO notifications is not a lack of listeners.
	listener.PropagateBlockAddedNotifications()
	if notificationManager.HasUTXOsChangedListeners() {
		t.Fatal("the notification manager reports a UTXOs changed listener before any subscribed")
	}

	if err := manager.notifyUTXOsChanged(addUTXO(1, 2)); err != nil {
		t.Fatalf("notifyUTXOsChanged without subscribers: %+v", err)
	}
	if !hasUTXOs() {
		t.Fatal("the index was not updated when nobody was subscribed")
	}
	if n := listenerRouter.OutgoingRoute().Length(); n != 0 {
		t.Fatalf("%d notification(s) queued with nobody subscribed to UTXO changes", n)
	}

	notificationManager.PropagateUTXOsChangedNotifications(listener, []*rpccontext.UTXOsChangedNotificationAddress{
		{Address: "test-address", ScriptPublicKeyString: utxoindex.ScriptPublicKeyString(script.String())},
	})
	if n := listenerRouter.OutgoingRoute().Length(); n != 0 {
		t.Fatalf("subscribing replayed %d skipped notification(s)", n)
	}

	if err := manager.notifyUTXOsChanged(addUTXO(2, 3)); err != nil {
		t.Fatalf("notifyUTXOsChanged with a subscriber: %+v", err)
	}
	if n := listenerRouter.OutgoingRoute().Length(); n != 1 {
		t.Fatalf("%d notification(s) queued for the subscriber, want 1", n)
	}
	message, err := listenerRouter.OutgoingRoute().Dequeue()
	if err != nil {
		t.Fatalf("Dequeue: %+v", err)
	}
	notification, ok := message.(*appmessage.UTXOsChangedNotificationMessage)
	if !ok {
		t.Fatalf("got %T, want a UTXOsChangedNotificationMessage", message)
	}
	// Only the second change: the subscriber was not subscribed for the first.
	if len(notification.Removed) != 0 || len(notification.Added) != 1 {
		t.Fatalf("notification has %d added and %d removed entries, want 1 and 0", len(notification.Added), len(notification.Removed))
	}
	if entry := notification.Added[0]; entry.Address != "test-address" || entry.Outpoint.Index != 2 || entry.UTXOEntry.Amount != 1002 {
		t.Fatalf("unexpected notification entry: %+v", entry)
	}
}
