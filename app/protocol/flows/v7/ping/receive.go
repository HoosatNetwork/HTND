package ping

import (
	"github.com/HoosatNetwork/HTND/app/appmessage"
	"github.com/HoosatNetwork/HTND/app/protocol/protocolerrors"
	"github.com/HoosatNetwork/HTND/infrastructure/network/netadapter/router"
)

// ReceivePingsContext is the interface for the context needed for the ReceivePings flow.
type ReceivePingsContext any

type receivePingsFlow struct {
	ReceivePingsContext
	incomingRoute, outgoingRoute *router.Route
}

// ReceivePings handles all ping messages coming through incomingRoute.
// This function assumes that incomingRoute will only return MsgPing.
func ReceivePings(context ReceivePingsContext, incomingRoute *router.Route, outgoingRoute *router.Route) error {
	flow := &receivePingsFlow{
		ReceivePingsContext: context,
		incomingRoute:       incomingRoute,
		outgoingRoute:       outgoingRoute,
	}
	return flow.start()
}

func (flow *receivePingsFlow) start() error {
	for {
		message, err := flow.incomingRoute.Dequeue()
		if err != nil {
			return err
		}
		pingMessage, err := unwrapPingMessage(message)
		if err != nil {
			return err
		}

		pongMessage := appmessage.NewMsgPong(pingMessage.Nonce)
		err = flow.outgoingRoute.Enqueue(pongMessage)
		if err != nil {
			return err
		}
	}
}

func unwrapPingMessage(message appmessage.Message) (*appmessage.MsgPing, error) {
	pingMessage, ok := message.(*appmessage.MsgPing)
	if !ok {
		return nil, protocolerrors.Errorf(true, "received unexpected message type. expected: %s, got: %s",
			appmessage.CmdPing, message.Command())
	}

	return pingMessage, nil
}
