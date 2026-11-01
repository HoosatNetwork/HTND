package blockrelay

import (
	"math"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
	"github.com/HoosatNetwork/HTND/v2/app/protocol/flowcontext"
	peerpkg "github.com/HoosatNetwork/HTND/v2/app/protocol/peer"
	"github.com/HoosatNetwork/HTND/v2/domain"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/testutils"
	"github.com/HoosatNetwork/HTND/v2/domain/miningmanager/mempool"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/config"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database/ldb"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter/router"
)

type relayedInv struct {
	hash     *externalapi.DomainHash
	excluded *peerpkg.Peer
}

// relayRecipientsContext is the node's real flow context, seen through domain, with its block announcements
// recorded instead of sent.
type relayRecipientsContext struct {
	*flowcontext.FlowContext
	domain  domain.Domain
	relayed chan relayedInv
}

func (c *relayRecipientsContext) Domain() domain.Domain { return c.domain }

func (c *relayRecipientsContext) Broadcast(message appmessage.Message) error {
	return c.BroadcastExcept(message, nil)
}

func (c *relayRecipientsContext) BroadcastExcept(message appmessage.Message, excluded *peerpkg.Peer) error {
	if inv, ok := message.(*appmessage.MsgInvRelayBlock); ok {
		c.relayed <- relayedInv{hash: inv.Hash, excluded: excluded}
	}
	return nil
}

// siblingConsensus inserts sibling right after the first block inserted through it, as another peer's flow can
// between the relay flow's two looks at the virtual.
type siblingConsensus struct {
	externalapi.Consensus
	sibling *externalapi.DomainBlock
}

func (c *siblingConsensus) ValidateAndInsertBlock(block *externalapi.DomainBlock, updateVirtual bool, powSkip bool) error {
	err := c.Consensus.ValidateAndInsertBlock(block, updateVirtual, powSkip)
	if err != nil || c.sibling == nil {
		return err
	}
	sibling := c.sibling
	c.sibling = nil
	return c.Consensus.ValidateAndInsertBlock(sibling, updateVirtual, powSkip)
}

// TestRelayFlowDoesNotSendBlockBackToSender runs the real relay flow and has its peer announce and deliver a block,
// while a sibling from elsewhere is inserted at the same time. Both become new virtual parents. The flow must
// announce the peer's block to every peer except that one, and the sibling to every peer.
func TestRelayFlowDoesNotSendBlockBackToSender(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		defer constants.ForceSetBlockVersion(1)
		consensusConfig.POWScores = []uint64{math.MaxUint64}

		ldbDB, err := ldb.NewLevelDB(t.TempDir(), 8)
		if err != nil {
			t.Fatalf("NewLevelDB: %+v", err)
		}
		defer ldbDB.Close()
		domainInstance, err := domain.New(consensusConfig, mempool.DefaultConfig(&consensusConfig.Params), ldbDB)
		if err != nil {
			t.Fatalf("domain.New: %+v", err)
		}
		cfg := config.DefaultConfig()
		cfg.ActiveNetParams = &consensusConfig.Params
		cfg.AllowSubmitBlockWhenNotSynced = true
		consensusWithSibling := &siblingConsensus{Consensus: domainInstance.Consensus()}
		context := &relayRecipientsContext{
			FlowContext: flowcontext.New(cfg, domainInstance, nil, &netadapter.NetAdapter{}, nil),
			domain:      &relayOrderDomain{Domain: domainInstance, consensus: consensusWithSibling},
			relayed:     make(chan relayedInv, 16),
		}

		netConnection := loopbackNetConnection(t)
		sender := peerpkg.New(netConnection)
		incomingRoute := router.NewRoute("incoming")
		outgoingRoute := router.NewRoute("outgoing")
		flowDone := make(chan error, 1)
		go func() {
			flowDone <- HandleRelayInvs(context, nil, netConnection, incomingRoute, outgoingRoute, sender)
		}()
		defer func() {
			incomingRoute.Close()
			select {
			case <-flowDone:
			case <-time.After(30 * time.Second):
				t.Fatalf("the relay flow did not stop after its route closed")
			}
		}()

		scriptPublicKey, _ := testutils.OpTrueScript()
		buildBlock := func(extraData byte) *externalapi.DomainBlock {
			coinbaseData := &externalapi.DomainCoinbaseData{ScriptPublicKey: scriptPublicKey, ExtraData: []byte{extraData}}
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
			return block
		}
		block := buildBlock(1)
		hash := consensushashing.BlockHash(block)
		consensusWithSibling.sibling = buildBlock(2)
		siblingHash := consensushashing.BlockHash(consensusWithSibling.sibling)

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

		for range 2 {
			select {
			case relayed := <-context.relayed:
				switch {
				case relayed.hash.Equal(hash):
					if relayed.excluded != sender {
						t.Fatalf("the relay flow announced block %s to the peer that sent it", hash)
					}
				case relayed.hash.Equal(siblingHash):
					if relayed.excluded != nil {
						t.Fatalf("the relay flow left a peer out of the announcement of sibling %s", siblingHash)
					}
				default:
					t.Fatalf("the relay flow announced unexpected block %s", relayed.hash)
				}
			case err := <-flowDone:
				t.Fatalf("the relay flow stopped: %+v", err)
			case <-time.After(30 * time.Second):
				t.Fatalf("the relay flow did not announce both block %s and sibling %s", hash, siblingHash)
			}
		}
	})
}
