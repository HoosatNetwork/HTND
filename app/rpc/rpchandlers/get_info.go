package rpchandlers

import (
	"strconv"

	"github.com/HoosatNetwork/HTND/app/appmessage"
	"github.com/HoosatNetwork/HTND/app/rpc/rpccontext"
	"github.com/HoosatNetwork/HTND/infrastructure/network/netadapter/router"
	"github.com/HoosatNetwork/HTND/version"
)

// HandleGetInfo handles the respectively named RPC command
func HandleGetInfo(context *rpccontext.Context, _ *router.Router, _ appmessage.Message) (appmessage.Message, error) {
	isNearlySynced, err := context.Domain.Consensus().IsNearlySynced()
	if err != nil {
		return nil, err
	}
	transactionCount, err := strconv.ParseUint(strconv.Itoa(context.Domain.MiningManager().TransactionCount(true, false)), 10, 64)
	if err != nil {
		return nil, err
	}

	// Deliberately separate from isSynced. A node whose imported pruning-point UTXO set is missing
	// coins still syncs, still serves, and still reports isSynced - while rejecting transactions the
	// network accepted and reporting balances that disagree with other nodes. Any failure to
	// establish health reads as unverified rather than propagating an error, because GetInfo is what
	// callers use to decide whether the node is usable at all, and it should not stop answering just
	// because this one question could not be settled.
	isUTXOSetVerified := false
	if health, err := context.Domain.Consensus().UTXOSetHealth(); err != nil {
		log.Warnf("Could not determine UTXO set health for GetInfo: %s", err)
	} else {
		isUTXOSetVerified = health.BaselineVerified
	}

	response := appmessage.NewGetInfoResponseMessage(
		context.NetAdapter.ID().String(),
		transactionCount,
		version.Version(),
		context.Config.UTXOIndex,
		context.ProtocolManager.Context().HasPeers() && isNearlySynced,
		isUTXOSetVerified,
	)

	return response, nil
}
