package blockrelay

import (
	"sort"

	"github.com/HoosatNetwork/HTND/app/appmessage"
	"github.com/HoosatNetwork/HTND/app/protocol/peer"
	"github.com/HoosatNetwork/HTND/domain"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/infrastructure/config"
	"github.com/HoosatNetwork/HTND/infrastructure/network/netadapter/router"
)

// RequestAnticoneContext is the interface for the context needed for the HandleRequestHeaders flow.
type RequestAnticoneContext interface {
	Domain() domain.Domain
	Config() *config.Config
}

type handleRequestAnticoneFlow struct {
	RequestAnticoneContext
	incomingRoute, outgoingRoute *router.Route
	peer                         *peer.Peer
}

// HandleRequestAnticone handles RequestAnticone messages
func HandleRequestAnticone(context RequestAnticoneContext, incomingRoute *router.Route,
	outgoingRoute *router.Route, peer *peer.Peer,
) error {
	flow := &handleRequestAnticoneFlow{
		RequestAnticoneContext: context,
		incomingRoute:          incomingRoute,
		outgoingRoute:          outgoingRoute,
		peer:                   peer,
	}
	return flow.start()
}

func (flow *handleRequestAnticoneFlow) start() error {
	for {
		blockHash, contextHash, err := receiveRequestAnticone(flow.incomingRoute)
		if err != nil {
			return err
		}
		log.Debugf("Received requestAnticone with blockHash: %s, contextHash: %s", blockHash, contextHash)
		log.Debugf("Getting past(%s) cap anticone(%s) for peer %s", contextHash, blockHash, flow.peer)

		// GetAnticone is expected to be called by the syncee for getting the anticone of the header selected tip
		// intersected by past of relayed block, and is thus expected to be bounded by mergeset limit since
		// we relay blocks only if they enter virtual's mergeset. We add a 2 factor for possible sync gaps.
		var blockHashes []*externalapi.DomainHash
		blockHashes, err = flow.Domain().Consensus().GetAnticone(blockHash, contextHash, flow.Config().ActiveNetParams.MergeSetSizeLimit*5000)
		if err != nil {
			// GetAnticone fails, among other reasons, whenever blockHash or contextHash is a hash
			// this node does not have - a peer syncing from a point this node has pruned or never
			// had is a legitimate case, not evidence of anything attributable to the request.
			// Unconditionally banning here (as this used to) banned honest peers for that, and for
			// any of this node's own local errors underneath it. Returning the error unwrapped, like
			// the sibling GetHashesBetween/GetBlockHeaders calls in handle_request_headers.go
			// already do, lets flowcontext.HandleError's own classifier decide - it bans only for an
			// actual ruleerrors.RuleError, not for a not-found or an unexpected local failure.
			return err
		}
		log.Debugf("Got %d header hashes in past(%s) cap anticone(%s)", len(blockHashes), contextHash, blockHash)

		// Fetch headers in a single batch to reduce consensus read overhead
		domainHeaders, err := flow.Domain().Consensus().GetBlockHeaders(blockHashes)
		if err != nil {
			return err
		}
		blockHeaders := make([]*appmessage.MsgBlockHeader, len(domainHeaders))
		for i, dh := range domainHeaders {
			blockHeaders[i] = appmessage.DomainBlockHeaderToBlockHeader(dh)
		}

		// We sort the headers in bottom-up topological order before sending
		sort.Slice(blockHeaders, func(i, j int) bool {
			return blockHeaders[i].BlueWork.Cmp(blockHeaders[j].BlueWork) < 0
		})

		blockHeadersMessage := appmessage.NewBlockHeadersMessage(blockHeaders)
		err = flow.outgoingRoute.Enqueue(blockHeadersMessage)
		if err != nil {
			return err
		}

		err = flow.outgoingRoute.Enqueue(appmessage.NewMsgDoneHeaders())
		if err != nil {
			return err
		}
	}
}

func receiveRequestAnticone(incomingRoute *router.Route) (blockHash *externalapi.DomainHash,
	contextHash *externalapi.DomainHash, err error,
) {
	message, err := incomingRoute.Dequeue()
	if err != nil {
		return nil, nil, err
	}
	msgRequestAnticone := message.(*appmessage.MsgRequestAnticone)

	return msgRequestAnticone.BlockHash, msgRequestAnticone.ContextHash, nil
}
