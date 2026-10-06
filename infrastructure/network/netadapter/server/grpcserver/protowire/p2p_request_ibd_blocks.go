package protowire

import (
	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
	"github.com/pkg/errors"
)

func (x *HoosatdMessage_RequestIBDBlocks) toAppMessage() (appmessage.Message, error) {
	if x == nil {
		return nil, errors.Wrapf(errorNil, "HoosatdMessage_RequestIBDBlocks is nil")
	}
	return x.RequestIBDBlocks.toAppMessage()
}

func (x *RequestIBDBlocksMessage) toAppMessage() (appmessage.Message, error) {
	if x == nil {
		return nil, errors.Wrapf(errorNil, "RequestIBDBlocksMessage is nil")
	}
	if len(x.Hashes) > appmessage.MaxRequestIBDBlocksHashes {
		return nil, errors.Errorf("too many hashes for message "+
			"[count %d, max %d]", len(x.Hashes), appmessage.MaxRequestIBDBlocksHashes)
	}
	hashes, err := protoHashesToDomain(x.Hashes)
	if err != nil {
		return nil, err
	}
	return &appmessage.MsgRequestIBDBlocks{Hashes: hashes}, nil
}

func (x *HoosatdMessage_RequestIBDBlocks) fromAppMessage(msgRequestIBDBlocks *appmessage.MsgRequestIBDBlocks) error {
	if len(msgRequestIBDBlocks.Hashes) > appmessage.MaxRequestIBDBlocksHashes {
		return errors.Errorf("too many hashes for message "+
			"[count %d, max %d]", len(msgRequestIBDBlocks.Hashes), appmessage.MaxRequestIBDBlocksHashes)
	}

	x.RequestIBDBlocks = &RequestIBDBlocksMessage{
		Hashes: domainHashesToProto(msgRequestIBDBlocks.Hashes),
	}

	return nil
}
