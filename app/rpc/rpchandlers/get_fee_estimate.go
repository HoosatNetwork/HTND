package rpchandlers

import (
	"github.com/Hoosat-Oy/HTND/app/appmessage"
	"github.com/Hoosat-Oy/HTND/app/rpc/rpccontext"
	"github.com/Hoosat-Oy/HTND/infrastructure/network/netadapter/router"
)

// HandleGetFeeEstimate handles the respectively named RPC command.
func HandleGetFeeEstimate(context *rpccontext.Context, _ *router.Router, _ appmessage.Message) (appmessage.Message, error) {
	// HTND's config min relay fee is expressed as sompi per kB.
	// A good baseline feerate (sompi/gram) is therefore minRelay / 1000.
	feerateSompiPerGram := float64(context.Config.MinRelayTxFee) / 1000.0

	estimate := &appmessage.RPCFeeEstimate{
		PriorityBucket: &appmessage.RPCFeerateBucket{Feerate: feerateSompiPerGram, EstimatedSeconds: 1},
		NormalBuckets:  []*appmessage.RPCFeerateBucket{{Feerate: feerateSompiPerGram, EstimatedSeconds: 60}},
		LowBuckets:     []*appmessage.RPCFeerateBucket{{Feerate: feerateSompiPerGram, EstimatedSeconds: 3600}},
	}

	return appmessage.NewGetFeeEstimateResponseMessage(estimate), nil
}
