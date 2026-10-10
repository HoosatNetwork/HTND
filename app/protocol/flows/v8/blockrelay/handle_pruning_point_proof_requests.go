package blockrelay

import (
	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
	peerpkg "github.com/HoosatNetwork/HTND/v2/app/protocol/peer"
	"github.com/HoosatNetwork/HTND/v2/app/protocol/protocolerrors"
	"github.com/HoosatNetwork/HTND/v2/domain"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter/router"
)

// PruningPointProofRequestsContext is the interface for the context needed for the HandlePruningPointProofRequests flow.
type PruningPointProofRequestsContext interface {
	Domain() domain.Domain
}

// HandlePruningPointProofRequests listens to appmessage.MsgRequestPruningPointProof messages and sends
// the pruning point proof to the requesting peer.
func HandlePruningPointProofRequests(context PruningPointProofRequestsContext, incomingRoute *router.Route,
	outgoingRoute *router.Route, peer *peerpkg.Peer,
) error {
	for {
		_, err := incomingRoute.Dequeue()
		if err != nil {
			return err
		}

		log.Debugf("Got request for pruning point proof from %s", peer)

		err = checkOwnPruningPointMeetsHeadersChain(context.Domain().Consensus())
		if err != nil {
			return err
		}

		pruningPointProof, err := context.Domain().Consensus().BuildPruningPointProof()
		if err != nil {
			return err
		}
		pruningPointProofMessage := appmessage.DomainPruningPointProofToMsgPruningPointProof(pruningPointProof)
		if pruningPointProofTooLarge(pruningPointProofMessage) {
			log.Warnf("not sending pruning point proof to %s: it exceeds 1 GiB", peer)
			return protocolerrors.New(false, "pruning point proof exceeds 1 GiB")
		}
		err = outgoingRoute.Enqueue(pruningPointProofMessage)
		if err != nil {
			return err
		}

		log.Debugf("Sent pruning point proof to %s", peer)
	}
}
