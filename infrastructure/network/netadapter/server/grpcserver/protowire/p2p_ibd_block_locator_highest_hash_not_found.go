package protowire

import (
	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
	"github.com/pkg/errors"
)

func (x *HoosatdMessage_IbdBlockLocatorHighestHashNotFound) toAppMessage() (appmessage.Message, error) {
	if x == nil {
		return nil, errors.Wrapf(errorNil, "HoosatdMessage_IbdBlockLocatorHighestHashNotFound is nil")
	}
	return &appmessage.MsgIBDBlockLocatorHighestHashNotFound{}, nil
}

func (x *HoosatdMessage_IbdBlockLocatorHighestHashNotFound) fromAppMessage(_ *appmessage.MsgIBDBlockLocatorHighestHashNotFound) error {
	x.IbdBlockLocatorHighestHashNotFound = &IbdBlockLocatorHighestHashNotFoundMessage{}
	return nil
}
