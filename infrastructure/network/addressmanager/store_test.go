package addressmanager

import (
	"errors"
	"net"
	"reflect"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database"
	"github.com/HoosatNetwork/HTND/v2/util/mstime"
)

func TestAddressKeySerialization(t *testing.T) {
	addressManager, teardown := newAddressManagerForTest(t, "TestAddressKeySerialization")
	defer teardown()
	addressStore := addressManager.store

	testAddress := &appmessage.NetAddress{IP: net.ParseIP("2602:100:abcd::102"), Port: 12345}
	testAddressKey := netAddressKey(testAddress)

	serializedTestAddressKey := addressStore.serializeAddressKey(testAddressKey)
	deserializedTestAddressKey := addressStore.deserializeAddressKey(serializedTestAddressKey)
	if !reflect.DeepEqual(testAddressKey, deserializedTestAddressKey) {
		t.Fatalf("testAddressKey and deserializedTestAddressKey are not equal\n"+
			"testAddressKey:%+v\ndeserializedTestAddressKey:%+v", testAddressKey, deserializedTestAddressKey)
	}
}

func TestAddressSerialization(t *testing.T) {
	addressManager, teardown := newAddressManagerForTest(t, "TestAddressSerialization")
	defer teardown()
	addressStore := addressManager.store

	testAddress := &address{
		netAddress: &appmessage.NetAddress{
			IP:        net.ParseIP("2602:100:abcd::102"),
			Port:      12345,
			Timestamp: mstime.Now(),
		},
		connectionFailedCount: 98465,
	}

	serializedTestAddress := addressStore.serializeAddress(testAddress)
	deserializedTestAddress := addressStore.deserializeAddress(serializedTestAddress)
	if !reflect.DeepEqual(testAddress, deserializedTestAddress) {
		t.Fatalf("testAddress and deserializedTestAddress are not equal\n"+
			"testAddress:%+v\ndeserializedTestAddress:%+v", testAddress, deserializedTestAddress)
	}
}

type failingPutDatabase struct {
	database.Database
}

func (failingPutDatabase) Put(*database.Key, []byte) error {
	return errors.New("put failed")
}

// A failed database write must not leave the address in the in-memory map.
func TestAddDoesNotRetainAddressWhenPutFails(t *testing.T) {
	addressManager, teardown := newAddressManagerForTest(t, "TestAddDoesNotRetainAddressWhenPutFails")
	defer teardown()
	addressStore := addressManager.store
	addressStore.database = failingPutDatabase{addressStore.database}

	netAddress := &appmessage.NetAddress{IP: net.ParseIP("8.8.8.8"), Port: 12345, Timestamp: mstime.Now()}
	if err := addressStore.add(netAddressKey(netAddress), &address{netAddress: netAddress}); err == nil {
		t.Fatal("add succeeded although put failed")
	}
	if count := addressStore.notBannedCount(); count != 0 {
		t.Fatalf("%d addresses retained after a failed put, want 0", count)
	}
}
