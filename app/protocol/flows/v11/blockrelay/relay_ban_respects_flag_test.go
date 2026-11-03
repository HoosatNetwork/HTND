package blockrelay

import (
	"fmt"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/infrastructure/config"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database/ldb"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/addressmanager"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/connmanager"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter"
)

type relayBanTestContext struct {
	RelayInvsContext
	cfg *config.Config
}

func (c relayBanTestContext) Config() *config.Config { return c.cfg }

// TestRelayOffensesRespectEnableBanning checks that the relay flow bans a peer for repeated
// offences (for example invs of blocks this node stores as invalid) only when --enablebanning is
// set. Without it a node that disagrees with its peers about a block's validity must not ban
// them, which would isolate it for the ban duration, across restarts.
func TestRelayOffensesRespectEnableBanning(t *testing.T) {
	for _, enableBanning := range []bool{false, true} {
		t.Run(fmt.Sprintf("enablebanning=%t", enableBanning), func(t *testing.T) {
			cfg := config.DefaultConfig()
			cfg.EnableBanning = enableBanning
			netConnection := loopbackNetConnection(t)

			db, err := ldb.NewLevelDB(t.TempDir(), 8)
			if err != nil {
				t.Fatalf("NewLevelDB: %+v", err)
			}
			defer db.Close()
			addressManager, err := addressmanager.New(addressmanager.NewConfig(cfg), db)
			if err != nil {
				t.Fatalf("addressmanager.New: %+v", err)
			}
			connectionManager, err := connmanager.New(cfg, &netadapter.NetAdapter{}, addressManager)
			if err != nil {
				t.Fatalf("connmanager.New: %+v", err)
			}

			flow := &handleRelayInvsFlow{
				RelayInvsContext:  relayBanTestContext{cfg: cfg},
				connectionManager: connectionManager,
				netConnection:     netConnection,
			}
			defer func() {
				offenseTrackerLock.Lock()
				delete(offenseTracker, netConnection.Address())
				offenseTrackerLock.Unlock()
			}()
			for range maxOffenses {
				flow.banConnection(false)
			}

			// BannedAddresses also lists the hardcoded bans, so look for this peer's IP.
			banned := false
			for _, address := range addressManager.BannedAddresses() {
				if address.IP.Equal(netConnection.NetAddress().IP) {
					banned = true
				}
			}
			if banned != enableBanning {
				t.Fatalf("after %d relay offences: banned=%t, want %t (--enablebanning=%t)",
					maxOffenses, banned, enableBanning, enableBanning)
			}
		})
	}
}
