package blockrelay

import (
	"errors"
	"math"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
	"github.com/HoosatNetwork/HTND/v2/app/protocol/flowcontext"
	peerpkg "github.com/HoosatNetwork/HTND/v2/app/protocol/peer"
	"github.com/HoosatNetwork/HTND/v2/domain"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/ruleerrors"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/testutils"
	"github.com/HoosatNetwork/HTND/v2/domain/miningmanager/mempool"
	"github.com/HoosatNetwork/HTND/v2/domain/prefixmanager/prefix"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/config"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database/ldb"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter/router"
)

// relayOrder records, from inside the relay flow, what happened around the block that moved the pruning point.
type relayOrder struct {
	mu sync.Mutex

	db          database.Database
	updatingKey *database.Key
	updatesRun  int // pruning point UTXO set updates that ran: each one first records its diff method

	moving, previousPruningPoint, pruningPoint *externalapi.DomainHash
	updatesRunAtMove                           int
	requestsChecked                            bool
	requestErrors                              []string
	announced, pendingWhenAnnounced            bool
	updatesRunWhenAnnounced                    int
	iterationDone                              chan struct{}
}

func (r *relayOrder) pending() bool {
	pending, err := r.db.Has(r.updatingKey)
	return err == nil && pending
}

type relayOrderDB struct {
	database.Database
	order *relayOrder
}

func (db *relayOrderDB) Put(key *database.Key, value []byte) error {
	if string(key.Suffix()) == "pruning-point-utxo-set-update-method" {
		db.order.mu.Lock()
		db.order.updatesRun++
		db.order.mu.Unlock()
	}
	return db.Database.Put(key, value)
}

// relayOrderConsensus notes the insertion that moves the pruning point. At the relay flow's next GetVirtualInfo -
// between that insertion and the announcement - it asks for the set of the new and of the previous pruning point,
// as an IBD peer could at that moment.
type relayOrderConsensus struct {
	externalapi.Consensus
	order *relayOrder
}

func (c *relayOrderConsensus) ValidateAndInsertBlock(block *externalapi.DomainBlock, updateVirtual bool, powSkip bool) error {
	before, err := c.Consensus.PruningPoint()
	if err != nil {
		return err
	}
	err = c.Consensus.ValidateAndInsertBlock(block, updateVirtual, powSkip)
	if err != nil {
		return err
	}
	after, err := c.Consensus.PruningPoint()
	if err != nil {
		return err
	}
	if !after.Equal(before) {
		c.order.mu.Lock()
		c.order.moving = consensushashing.BlockHash(block)
		c.order.previousPruningPoint, c.order.pruningPoint = before, after
		c.order.updatesRunAtMove = c.order.updatesRun
		c.order.mu.Unlock()
	}
	return nil
}

func (c *relayOrderConsensus) GetVirtualInfo() (*externalapi.VirtualInfo, error) {
	c.order.mu.Lock()
	check := c.order.moving != nil && !c.order.requestsChecked
	c.order.requestsChecked = c.order.requestsChecked || check
	requested := []*externalapi.DomainHash{c.order.pruningPoint, c.order.previousPruningPoint}
	c.order.mu.Unlock()
	if check {
		for _, hash := range requested {
			_, err := c.Consensus.GetPruningPointUTXOs(hash, nil, 1)
			if !errors.Is(err, ruleerrors.ErrWrongPruningPointHash) {
				c.order.mu.Lock()
				c.order.requestErrors = append(c.order.requestErrors,
					"GetPruningPointUTXOs("+hash.String()+") between the insertion and its announcement: got "+
						errString(err)+", want ErrWrongPruningPointHash")
				c.order.mu.Unlock()
			}
		}
		c.order.mu.Lock()
		if c.order.updatesRun != c.order.updatesRunAtMove || !c.order.pending() {
			c.order.requestErrors = append(c.order.requestErrors,
				"the pruning point UTXO set update ran between the insertion and the relay flow's GetVirtualInfo")
		}
		c.order.mu.Unlock()
	}
	return c.Consensus.GetVirtualInfo()
}

func errString(err error) string {
	if err == nil {
		return "<nil>"
	}
	return err.Error()
}

type relayOrderDomain struct {
	domain.Domain
	consensus externalapi.Consensus
}

func (d *relayOrderDomain) Consensus() externalapi.Consensus { return d.consensus }

// relayOrderContext is the node's real flow context, seen through the recording domain, with the announcement and the
// trailing pruning point update observed.
type relayOrderContext struct {
	*flowcontext.FlowContext
	domain domain.Domain
	order  *relayOrder
}

func (c *relayOrderContext) Domain() domain.Domain { return c.domain }

func (c *relayOrderContext) BroadcastExcept(message appmessage.Message, excluded *peerpkg.Peer) error {
	if inv, ok := message.(*appmessage.MsgInvRelayBlock); ok {
		c.order.mu.Lock()
		if c.order.moving != nil && inv.Hash.Equal(c.order.moving) {
			c.order.announced = true
			c.order.pendingWhenAnnounced = c.order.pending()
			c.order.updatesRunWhenAnnounced = c.order.updatesRun
		}
		c.order.mu.Unlock()
	}
	return c.FlowContext.BroadcastExcept(message, excluded)
}

func (c *relayOrderContext) UpdatePruningPointIfRequired() {
	c.FlowContext.UpdatePruningPointIfRequired()
	c.order.iterationDone <- struct{}{}
}

// TestRelayFlowAnnouncesBeforeTheDeferredPruningPointUpdate runs the real relay flow of a node with
// DeferPruningPointUTXOSetUpdate on, as the node's app sets it, and feeds it blocks until one moves the pruning point.
// Between that block's insertion and the flow's GetVirtualInfo, requests for the set of the new and of the previous
// pruning point must be refused without starting the update. The flow must then announce the block while the update is
// still pending, and only after that run it.
func TestRelayFlowAnnouncesBeforeTheDeferredPruningPointUpdate(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		defer constants.ForceSetBlockVersion(1)

		consensusConfig.POWScores = []uint64{math.MaxUint64}
		consensusConfig.K = append([]externalapi.KType(nil), consensusConfig.K...)
		consensusConfig.K[0] = 0
		consensusConfig.FinalityDuration = []time.Duration{30 * consensusConfig.TargetTimePerBlock[0]}
		consensusConfig.PruningProofM = 1
		window := make([]int, len(consensusConfig.DifficultyAdjustmentWindowSize))
		for i := range window {
			window[i] = 10
		}
		consensusConfig.DifficultyAdjustmentWindowSize = window
		consensusConfig.DeferPruningPointUTXOSetUpdate = true

		ldbDB, err := ldb.NewLevelDB(t.TempDir(), 8)
		if err != nil {
			t.Fatalf("NewLevelDB: %+v", err)
		}
		defer ldbDB.Close()
		order := &relayOrder{
			db:            ldbDB,
			updatingKey:   database.MakeBucket((&prefix.Prefix{}).Serialize()).Key([]byte("updating-pruning-point-utxo-set")),
			iterationDone: make(chan struct{}, 1),
		}
		domainInstance, err := domain.New(consensusConfig, mempool.DefaultConfig(&consensusConfig.Params),
			&relayOrderDB{Database: ldbDB, order: order})
		if err != nil {
			t.Fatalf("domain.New: %+v", err)
		}
		defer domainInstance.Consensus().Close()
		cfg := config.DefaultConfig()
		cfg.ActiveNetParams = &consensusConfig.Params
		cfg.AllowSubmitBlockWhenNotSynced = true
		context := &relayOrderContext{
			FlowContext: flowcontext.New(cfg, domainInstance, nil, &netadapter.NetAdapter{}, nil),
			domain: &relayOrderDomain{Domain: domainInstance,
				consensus: &relayOrderConsensus{Consensus: domainInstance.Consensus(), order: order}},
			order: order,
		}

		netConnection := loopbackNetConnection(t)
		incomingRoute := router.NewRoute("incoming")
		outgoingRoute := router.NewRoute("outgoing")
		flowDone := make(chan error, 1)
		go func() {
			flowDone <- HandleRelayInvs(context, nil, netConnection, incomingRoute, outgoingRoute,
				peerpkg.New(netConnection))
		}()

		scriptPublicKey, _ := testutils.OpTrueScript()
		coinbaseData := &externalapi.DomainCoinbaseData{ScriptPublicKey: scriptPublicKey, ExtraData: []byte{}}
		relay := func() {
			block, err := domainInstance.Consensus().BuildBlock(coinbaseData, nil)
			if err != nil {
				t.Fatalf("BuildBlock: %+v", err)
			}
			for _, transaction := range block.Transactions {
				for _, input := range transaction.Inputs {
					input.UTXOEntry = nil
				}
			}
			block.PoWHash = "relayed" // the flow only announces blocks that carry one
			hash := consensushashing.BlockHash(block)
			err = incomingRoute.Enqueue(appmessage.NewMsgInvBlock(hash))
			if err != nil {
				t.Fatalf("Enqueue inv: %+v", err)
			}
			request, err := outgoingRoute.DequeueWithTimeout(30 * time.Second)
			if err != nil {
				t.Fatalf("the relay flow did not request block %s: %+v", hash, err)
			}
			if _, ok := request.(*appmessage.MsgRequestRelayBlocks); !ok {
				t.Fatalf("the relay flow sent %s, want %s", request.Command(), appmessage.CmdRequestRelayBlocks)
			}
			err = incomingRoute.Enqueue(appmessage.DomainBlockToMsgBlock(block))
			if err != nil {
				t.Fatalf("Enqueue block: %+v", err)
			}
			select {
			case <-order.iterationDone:
			case err := <-flowDone:
				t.Fatalf("the relay flow stopped: %+v", err)
			case <-time.After(30 * time.Second):
				t.Fatalf("the relay flow did not finish block %s", hash)
			}
		}

		for i := 0; i < 300; i++ {
			relay()
			order.mu.Lock()
			moved := order.moving != nil
			order.mu.Unlock()
			if moved {
				break
			}
		}
		incomingRoute.Close()
		select {
		case <-flowDone:
		case <-time.After(30 * time.Second):
			t.Fatalf("the relay flow did not stop after its route closed")
		}

		if order.moving == nil {
			t.Fatalf("setup: the pruning point never moved")
		}
		for _, message := range order.requestErrors {
			t.Errorf("%s", message)
		}
		if !order.announced {
			t.Fatalf("the relay flow did not announce the block that moved the pruning point")
		}
		if !order.pendingWhenAnnounced || order.updatesRunWhenAnnounced != order.updatesRunAtMove {
			t.Errorf("the pruning point UTXO set update ran before the relay flow announced the block that moved " +
				"the pruning point")
		}
		// The update's last step, the check of the set against its commitment, runs in the background.
		for deadline := time.Now().Add(30 * time.Second); order.pending(); time.Sleep(time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("the relay flow did not run the pending pruning point UTXO set update after announcing " +
					"the block")
			}
		}
	})
}

// loopbackNetConnection connects two net adapters over loopback and returns the connecting side's connection. The
// relay flow only uses it for its address.
func loopbackNetConnection(t *testing.T) *netadapter.NetConnection {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %+v", err)
	}
	address := listener.Addr().String()
	_ = listener.Close()

	noRoutes := func(*router.Router, *netadapter.NetConnection) {}
	listening := config.DefaultConfig()
	listening.Listeners = []string{address}
	connecting := config.DefaultConfig()
	var adapters []*netadapter.NetAdapter
	for _, cfg := range []*config.Config{listening, connecting} {
		adapter, err := netadapter.NewNetAdapter(cfg)
		if err != nil {
			t.Fatalf("NewNetAdapter: %+v", err)
		}
		adapter.SetP2PRouterInitializer(noRoutes)
		adapter.SetRPCRouterInitializer(noRoutes)
		err = adapter.Start()
		if err != nil {
			t.Fatalf("Start: %+v", err)
		}
		t.Cleanup(func() { _ = adapter.Stop() })
		adapters = append(adapters, adapter)
	}
	err = adapters[1].P2PConnect(address)
	if err != nil {
		t.Fatalf("P2PConnect %s: %+v", address, err)
	}
	connections := adapters[1].P2PConnections()
	if len(connections) != 1 {
		t.Fatalf("got %d loopback connections, want 1", len(connections))
	}
	return connections[0]
}
