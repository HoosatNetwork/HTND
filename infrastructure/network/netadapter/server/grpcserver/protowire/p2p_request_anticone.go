package protowire

import (
	"github.com/Hoosat-Oy/HTND/app/appmessage"
	"github.com/pkg/errors"
)

func (x *HoosatdMessage_RequestAnticone) toAppMessage() (appmessage.Message, error) {
	if x == nil {
		return nil, errors.Wrapf(errorNil, "HoosatdMessage_RequestAnticone is nil")
	}
	return x.RequestAnticone.toAppMessage()
}

func (x *RequestAnticoneMessage) toAppMessage() (appmessage.Message, error) {
	if x == nil {
		return nil, errors.Wrapf(errorNil, "RequestAnticoneMessage is nil")
	}
	blockHash, err := x.BlockHash.toDomain()
	if err != nil {
		return nil, err
	}

	contextHash, err := x.ContextHash.toDomain()
	if err != nil {
		return nil, err
	}

	return &appmessage.MsgRequestAnticone{
		BlockHash:   blockHash,
		ContextHash: contextHash,
	}, nil
}

func (x *HoosatdMessage_RequestAnticone) fromAppMessage(msgRequestPastDiff *appmessage.MsgRequestAnticone) error {
	x.RequestAnticone = &RequestAnticoneMessage{
		BlockHash:   domainHashToProto(msgRequestPastDiff.BlockHash),
		ContextHash: domainHashToProto(msgRequestPastDiff.ContextHash),
	}
	return nil
}
