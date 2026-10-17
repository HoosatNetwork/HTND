package rejects

import (
	"unicode/utf8"

	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
	"github.com/HoosatNetwork/HTND/v2/app/protocol/protocolerrors"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/network/netadapter/router"
)

// maxRejectReasonLength bounds how much of a peer's reject reason is kept in the
// resulting error, which is stored on the connection and logged.
const maxRejectReasonLength = 2048

// HandleRejectsContext is the interface for the context needed for the HandleRejects flow.
type HandleRejectsContext any

type handleRejectsFlow struct {
	HandleRejectsContext
	incomingRoute, outgoingRoute *router.Route
}

// HandleRejects handles all reject messages coming through incomingRoute.
// This function assumes that incomingRoute will only return MsgReject.
func HandleRejects(context HandleRejectsContext, incomingRoute *router.Route, outgoingRoute *router.Route) error {
	flow := &handleRejectsFlow{
		HandleRejectsContext: context,
		incomingRoute:        incomingRoute,
		outgoingRoute:        outgoingRoute,
	}
	return flow.start()
}

func (flow *handleRejectsFlow) start() error {
	message, err := flow.incomingRoute.Dequeue()
	if err != nil {
		return err
	}
	rejectMessage := message.(*appmessage.MsgReject)

	reason := rejectMessage.Reason
	if len(reason) > maxRejectReasonLength {
		// Cut at a character boundary so the kept part stays valid UTF-8.
		cut := maxRejectReasonLength
		for cut > 0 && !utf8.RuneStart(reason[cut]) {
			cut--
		}
		reason = reason[:cut]
	}

	return protocolerrors.Errorf(false, "got reject message: `%s`", reason)
}
