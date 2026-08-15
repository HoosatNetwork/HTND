package protowire

import (
	"github.com/HoosatNetwork/HTND/app/appmessage"
	"github.com/pkg/errors"
)

func (x *HoosatdMessage_IbdBlock) toAppMessage() (appmessage.Message, error) {
	if x == nil {
		return nil, errors.Wrapf(errorNil, "HoosatdMessage_IbdBlock is nil")
	}
	msgBlock, err := x.IbdBlock.toAppMessage()
	if err != nil {
		return nil, err
	}
	return &appmessage.MsgIBDBlock{MsgBlock: msgBlock}, nil
}

func (x *HoosatdMessage_IbdBlock) fromAppMessage(msgIBDBlock *appmessage.MsgIBDBlock) error {
	if x == nil {
		return errors.Wrapf(errorNil, "HoosatdMessage_IbdBlock is nil")
	}
	if msgIBDBlock == nil {
		return errors.Wrapf(errorNil, "msgIBDBlock is nil")
	}
	x.IbdBlock = new(BlockMessage)
	return x.IbdBlock.fromAppMessage(msgIBDBlock.MsgBlock)
}
