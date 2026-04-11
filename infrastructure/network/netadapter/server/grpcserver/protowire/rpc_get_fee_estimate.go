package protowire

import (
	"github.com/Hoosat-Oy/HTND/app/appmessage"
	"github.com/pkg/errors"
)

func (x *HoosatdMessage_GetFeeEstimateRequest) toAppMessage() (appmessage.Message, error) {
	if x == nil {
		return nil, errors.Wrapf(errorNil, "HoosatdMessage_GetFeeEstimateRequest is nil")
	}
	return x.GetFeeEstimateRequest.toAppMessage()
}

func (x *HoosatdMessage_GetFeeEstimateRequest) fromAppMessage(_ *appmessage.GetFeeEstimateRequestMessage) error {
	x.GetFeeEstimateRequest = &GetFeeEstimateRequestMessage{}
	return nil
}

func (x *GetFeeEstimateRequestMessage) toAppMessage() (appmessage.Message, error) {
	if x == nil {
		return nil, errors.Wrapf(errorNil, "GetFeeEstimateRequestMessage is nil")
	}
	return &appmessage.GetFeeEstimateRequestMessage{}, nil
}

func (x *HoosatdMessage_GetFeeEstimateResponse) toAppMessage() (appmessage.Message, error) {
	if x == nil {
		return nil, errors.Wrapf(errorNil, "HoosatdMessage_GetFeeEstimateResponse is nil")
	}
	return x.GetFeeEstimateResponse.toAppMessage()
}

func (x *HoosatdMessage_GetFeeEstimateResponse) fromAppMessage(message *appmessage.GetFeeEstimateResponseMessage) error {
	var estimate *RpcFeeEstimate
	if message.Estimate != nil {
		estimate = &RpcFeeEstimate{}
		estimate.fromAppMessage(message.Estimate)
	}

	var err *RPCError
	if message.Error != nil {
		err = &RPCError{Message: message.Error.Message}
	}

	x.GetFeeEstimateResponse = &GetFeeEstimateResponseMessage{
		Estimate: estimate,
		Error:    err,
	}
	return nil
}

func (x *GetFeeEstimateResponseMessage) toAppMessage() (appmessage.Message, error) {
	if x == nil {
		return nil, errors.Wrapf(errorNil, "GetFeeEstimateResponseMessage is nil")
	}
	rpcErr, err := x.Error.toAppMessage()
	// Error is an optional field
	if err != nil && !errors.Is(err, errorNil) {
		return nil, err
	}

	var estimate *appmessage.RPCFeeEstimate
	if x.Estimate != nil {
		appEstimate, err := x.Estimate.toAppMessage()
		if err != nil {
			return nil, err
		}
		estimate = appEstimate
	}

	if rpcErr != nil && estimate != nil {
		return nil, errors.New("GetFeeEstimateResponseMessage contains both an error and a response")
	}

	return &appmessage.GetFeeEstimateResponseMessage{
		Estimate: estimate,
		Error:    rpcErr,
	}, nil
}

func (x *RpcFeerateBucket) toAppMessage() (*appmessage.RPCFeerateBucket, error) {
	if x == nil {
		return nil, errors.Wrapf(errorNil, "RpcFeerateBucket is nil")
	}
	return &appmessage.RPCFeerateBucket{
		Feerate:          x.Feerate,
		EstimatedSeconds: x.EstimatedSeconds,
	}, nil
}

func (x *RpcFeerateBucket) fromAppMessage(message *appmessage.RPCFeerateBucket) {
	*x = RpcFeerateBucket{
		Feerate:          message.Feerate,
		EstimatedSeconds: message.EstimatedSeconds,
	}
}

func (x *RpcFeeEstimate) toAppMessage() (*appmessage.RPCFeeEstimate, error) {
	if x == nil {
		return nil, errors.Wrapf(errorNil, "RpcFeeEstimate is nil")
	}

	var priorityBucket *appmessage.RPCFeerateBucket
	if x.PriorityBucket != nil {
		appPriorityBucket, err := x.PriorityBucket.toAppMessage()
		if err != nil {
			return nil, err
		}
		priorityBucket = appPriorityBucket
	}

	normalBuckets := make([]*appmessage.RPCFeerateBucket, len(x.NormalBuckets))
	for i, bucket := range x.NormalBuckets {
		appBucket, err := bucket.toAppMessage()
		if err != nil {
			return nil, err
		}
		normalBuckets[i] = appBucket
	}

	lowBuckets := make([]*appmessage.RPCFeerateBucket, len(x.LowBuckets))
	for i, bucket := range x.LowBuckets {
		appBucket, err := bucket.toAppMessage()
		if err != nil {
			return nil, err
		}
		lowBuckets[i] = appBucket
	}

	return &appmessage.RPCFeeEstimate{
		PriorityBucket: priorityBucket,
		NormalBuckets:  normalBuckets,
		LowBuckets:     lowBuckets,
	}, nil
}

func (x *RpcFeeEstimate) fromAppMessage(message *appmessage.RPCFeeEstimate) {
	var priorityBucket *RpcFeerateBucket
	if message.PriorityBucket != nil {
		priorityBucket = &RpcFeerateBucket{}
		priorityBucket.fromAppMessage(message.PriorityBucket)
	}

	normalBuckets := make([]*RpcFeerateBucket, len(message.NormalBuckets))
	for i, bucket := range message.NormalBuckets {
		normalBuckets[i] = &RpcFeerateBucket{}
		normalBuckets[i].fromAppMessage(bucket)
	}

	lowBuckets := make([]*RpcFeerateBucket, len(message.LowBuckets))
	for i, bucket := range message.LowBuckets {
		lowBuckets[i] = &RpcFeerateBucket{}
		lowBuckets[i].fromAppMessage(bucket)
	}

	*x = RpcFeeEstimate{
		PriorityBucket: priorityBucket,
		NormalBuckets:  normalBuckets,
		LowBuckets:     lowBuckets,
	}
}
