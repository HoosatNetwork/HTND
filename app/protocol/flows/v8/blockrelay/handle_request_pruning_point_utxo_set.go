package blockrelay

import (
	"errors"
	"slices"

	pkgerrors "github.com/pkg/errors"

	"github.com/HoosatNetwork/HTND/app/appmessage"
	"github.com/HoosatNetwork/HTND/app/protocol/common"
	"github.com/HoosatNetwork/HTND/app/protocol/protocolerrors"
	"github.com/HoosatNetwork/HTND/domain"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/ruleerrors"
	"github.com/HoosatNetwork/HTND/infrastructure/logger"
	"github.com/HoosatNetwork/HTND/infrastructure/network/netadapter/router"
)

// HandleRequestPruningPointUTXOSetContext is the interface for the context needed for the HandleRequestPruningPointUTXOSet flow.
type HandleRequestPruningPointUTXOSetContext interface {
	Domain() domain.Domain
}

type handleRequestPruningPointUTXOSetFlow struct {
	HandleRequestPruningPointUTXOSetContext
	incomingRoute, outgoingRoute *router.Route
}

// HandleRequestPruningPointUTXOSet listens to appmessage.MsgRequestPruningPointUTXOSet messages and sends
// the pruning point UTXO set and block body.
func HandleRequestPruningPointUTXOSet(context HandleRequestPruningPointUTXOSetContext, incomingRoute,
	outgoingRoute *router.Route,
) error {
	flow := &handleRequestPruningPointUTXOSetFlow{
		HandleRequestPruningPointUTXOSetContext: context,
		incomingRoute:                           incomingRoute,
		outgoingRoute:                           outgoingRoute,
	}

	return flow.start()
}

func (flow *handleRequestPruningPointUTXOSetFlow) start() error {
	for {
		msgRequestPruningPointUTXOSet, err := flow.waitForRequestPruningPointUTXOSetMessages()
		if err != nil {
			return err
		}

		err = flow.handleRequestPruningPointUTXOSetMessage(msgRequestPruningPointUTXOSet)
		if err != nil {
			return err
		}
	}
}

func (flow *handleRequestPruningPointUTXOSetFlow) handleRequestPruningPointUTXOSetMessage(
	msgRequestPruningPointUTXOSet *appmessage.MsgRequestPruningPointUTXOSet,
) error {
	onEnd := logger.LogAndMeasureExecutionTime(log, "handleRequestPruningPointUTXOSetFlow")
	defer onEnd()

	log.Debugf("Got request for pruning point UTXO set")

	return flow.sendPruningPointUTXOSet(msgRequestPruningPointUTXOSet)
}

func (flow *handleRequestPruningPointUTXOSetFlow) waitForRequestPruningPointUTXOSetMessages() (
	*appmessage.MsgRequestPruningPointUTXOSet, error,
) {
	message, err := flow.incomingRoute.Dequeue()
	if err != nil {
		return nil, err
	}
	msgRequestPruningPointUTXOSet, ok := message.(*appmessage.MsgRequestPruningPointUTXOSet)
	if !ok {
		// TODO: Change to shouldBan: true once we fix the bug of getting redundant messages
		return nil, protocolerrors.Errorf(false, "received unexpected message type. "+
			"expected: %s, got: %s", appmessage.CmdRequestPruningPointUTXOSet, message.Command())
	}
	return msgRequestPruningPointUTXOSet, nil
}

func (flow *handleRequestPruningPointUTXOSetFlow) sendPruningPointUTXOSet(
	msgRequestPruningPointUTXOSet *appmessage.MsgRequestPruningPointUTXOSet,
) error {
	ibdBatchSize := getIBDBatchSize()
	// Send the UTXO set in `step`-sized chunks
	const step = 1000
	var fromOutpoint *externalapi.DomainOutpoint
	chunksSent := 0

	// Pre-allocate the wire-message buffer once
	wirePairsBuffer := make([]*appmessage.OutpointAndUTXOEntryPair, 0, step)

	for {
		pruningPointUTXOs, err := flow.Domain().Consensus().GetPruningPointUTXOs(
			msgRequestPruningPointUTXOSet.PruningPointHash, fromOutpoint, step)
		if err != nil {
			if errors.Is(err, ruleerrors.ErrWrongPruningPointHash) {
				return flow.outgoingRoute.Enqueue(appmessage.NewMsgUnexpectedPruningPoint())
			}
			// Any other error must abort the transfer.
			//
			// This used to fall through. err was non-nil, pruningPointUTXOs was nil, so an empty
			// chunk went out, `finished` computed 0 < step = true, and the peer was sent
			// DonePruningPointUTXOSetChunks - told the transfer had completed successfully while
			// holding a truncated UTXO set. Neither side logged anything. The receiving node then
			// found its imported set did not match the pruning point header, "repaired" its own
			// trust anchor to whatever had arrived, and carried on with a permanently incomplete
			// set. That is silent, and it is how two nodes end up disagreeing about balances.
			//
			// The read can genuinely fail mid-transfer: consensus is only locked per chunk and a
			// fresh cursor is opened for each one, so a pruning-point advance that rewrites the
			// bucket underneath the transfer leaves the outpoint being resumed from gone - which
			// LevelDB's Seek reports as ErrNotFound. Failing here makes the syncing peer retry,
			// which is recoverable; truncating silently is not.
			return pkgerrors.Wrapf(err, "failed to read the pruning point UTXO set for %s at outpoint %v",
				msgRequestPruningPointUTXOSet.PruningPointHash, fromOutpoint)
		}

		log.Debugf("Retrieved %d UTXOs for pruning block %s",
			len(pruningPointUTXOs), msgRequestPruningPointUTXOSet.PruningPointHash)

		// Reuse the buffer slice
		wirePairsBuffer = wirePairsBuffer[:0]
		wirePairsBuffer = appmessage.AppendDomainOutpointAndUTXOEntryPairsToOutpointAndUTXOEntryPairs(
			pruningPointUTXOs, wirePairsBuffer)

		err = flow.outgoingRoute.Enqueue(appmessage.NewMsgPruningPointUTXOSetChunk(slices.Clone(wirePairsBuffer)))
		if err != nil {
			return err
		}

		finished := len(pruningPointUTXOs) < step
		if finished && chunksSent%ibdBatchSize != 0 {
			log.Debugf("Finished sending UTXOs for pruning block %s",
				msgRequestPruningPointUTXOSet.PruningPointHash)

			return flow.outgoingRoute.Enqueue(appmessage.NewMsgDonePruningPointUTXOSetChunks())
		}

		if len(pruningPointUTXOs) > 0 {
			fromOutpoint = pruningPointUTXOs[len(pruningPointUTXOs)-1].Outpoint
		}
		chunksSent++

		// Wait for the peer to request more chunks every `ibdBatchSize` chunks
		if chunksSent%ibdBatchSize == 0 {
			message, err := flow.incomingRoute.DequeueWithTimeout(common.DefaultTimeout)
			if err != nil {
				return err
			}
			_, ok := message.(*appmessage.MsgRequestNextPruningPointUTXOSetChunk)
			if !ok {
				// TODO: Change to shouldBan: true once we fix the bug of getting redundant messages
				return protocolerrors.Errorf(false, "received unexpected message type. "+
					"expected: %s, got: %s", appmessage.CmdRequestNextPruningPointUTXOSetChunk, message.Command())
			}

			if finished {
				log.Debugf("Finished sending UTXOs for pruning block %s",
					msgRequestPruningPointUTXOSet.PruningPointHash)

				return flow.outgoingRoute.Enqueue(appmessage.NewMsgDonePruningPointUTXOSetChunks())
			}
		}
	}
}
