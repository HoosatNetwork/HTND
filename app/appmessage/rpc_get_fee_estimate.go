package appmessage

// RPCFeerateBucket represents a fee-rate bucket for fee estimation.
// Fee rate is in sompi/gram units.
//
// NOTE: This mirrors the Kaspad RpcFeerateBucket schema.
type RPCFeerateBucket struct {
	Feerate          float64
	EstimatedSeconds float64
}

// RPCFeeEstimate is data required for making fee estimates.
//
// NOTE: This mirrors the Kaspad RpcFeeEstimate schema.
type RPCFeeEstimate struct {
	PriorityBucket *RPCFeerateBucket
	NormalBuckets  []*RPCFeerateBucket
	LowBuckets     []*RPCFeerateBucket
}

// GetFeeEstimateRequestMessage requests a fee estimate.
type GetFeeEstimateRequestMessage struct {
	baseMessage
}

// Command returns the protocol command string for the message.
func (msg *GetFeeEstimateRequestMessage) Command() MessageCommand {
	return CmdGetFeeEstimateRequestMessage
}

// NewGetFeeEstimateRequestMessage returns an instance of the message.
func NewGetFeeEstimateRequestMessage() *GetFeeEstimateRequestMessage {
	return &GetFeeEstimateRequestMessage{}
}

// GetFeeEstimateResponseMessage returns a fee estimate.
type GetFeeEstimateResponseMessage struct {
	baseMessage
	Estimate *RPCFeeEstimate
	Error    *RPCError
}

// Command returns the protocol command string for the message.
func (msg *GetFeeEstimateResponseMessage) Command() MessageCommand {
	return CmdGetFeeEstimateResponseMessage
}

// NewGetFeeEstimateResponseMessage returns an instance of the message.
func NewGetFeeEstimateResponseMessage(estimate *RPCFeeEstimate) *GetFeeEstimateResponseMessage {
	return &GetFeeEstimateResponseMessage{Estimate: estimate}
}
