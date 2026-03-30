package protowire

import (
	"github.com/Hoosat-Oy/HTND/app/appmessage"
	"github.com/pkg/errors"
)

func (x *HoosatdMessage_IbdBlockLocatorHighestHash) toAppMessage() (appmessage.Message, error) {
	if x == nil {
		return nil, errors.Wrapf(errorNil, "HoosatdMessage_IbdBlockLocatorHighestHash is nil")
	}
	return x.IbdBlockLocatorHighestHash.toAppMessgage()
}

func (x *IbdBlockLocatorHighestHashMessage) toAppMessgage() (appmessage.Message, error) {
	if x == nil {
		return nil, errors.Wrapf(errorNil, "IbdBlockLocatorHighestHashMessage is nil")
	}
	highestHash, err := x.HighestHash.toDomain()
	if err != nil {
		return nil, err
	}

	return &appmessage.MsgIBDBlockLocatorHighestHash{
		HighestHash: highestHash,
	}, nil
}

func (x *HoosatdMessage_IbdBlockLocatorHighestHash) fromAppMessage(message *appmessage.MsgIBDBlockLocatorHighestHash) error {
	x.IbdBlockLocatorHighestHash = &IbdBlockLocatorHighestHashMessage{
		HighestHash: domainHashToProto(message.HighestHash),
	}
	return nil
}
