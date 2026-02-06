package rpchandlers

import (
	"github.com/Hoosat-Oy/HTND/app/appmessage"
	"github.com/Hoosat-Oy/HTND/app/rpc/rpccontext"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/txscript"

	"github.com/Hoosat-Oy/HTND/infrastructure/network/netadapter/router"
	"github.com/Hoosat-Oy/HTND/util"
)

// HandleGetMempoolEntriesByAddresses handles the respectively named RPC command
func HandleGetMempoolEntriesByAddresses(context *rpccontext.Context, _ *router.Router, request appmessage.Message) (appmessage.Message, error) {
	isNearlySynced, err := context.Domain.Consensus().IsNearlySynced()
	if err != nil && context.Config.ActiveNetParams.Net == appmessage.Mainnet {
		return nil, err
	}
	if !isNearlySynced {
		return appmessage.NewGetMempoolEntriesByAddressesResponseMessage(nil), nil
	}
	getMempoolEntriesByAddressesRequest := request.(*appmessage.GetMempoolEntriesByAddressesRequestMessage)

	mempoolEntriesByAddresses := make([]*appmessage.MempoolEntryByAddress, 0)

	sendingInTransactionPool, receivingInTransactionPool, sendingInOrphanPool, receivingInOrphanPool, err := context.Domain.MiningManager().GetTransactionsByAddresses(!getMempoolEntriesByAddressesRequest.FilterTransactionPool, getMempoolEntriesByAddressesRequest.IncludeOrphanPool)
	if err != nil {
		return nil, err
	}

	for _, addressString := range getMempoolEntriesByAddressesRequest.Addresses {

		address, err := util.DecodeAddress(addressString, context.Config.NetParams().Prefix)
		if err != nil {
			errorMessage := &appmessage.GetMempoolEntriesByAddressesResponseMessage{}
			errorMessage.Error = appmessage.RPCErrorf("Could not decode address '%s': %s", addressString, err)
			return errorMessage, nil
		}

		sending := make([]*appmessage.MempoolEntry, 0)
		receiving := make([]*appmessage.MempoolEntry, 0)

		scriptPublicKey, err := txscript.PayToAddrScript(address)
		if err != nil {
			errorMessage := &appmessage.GetMempoolEntriesByAddressesResponseMessage{}
			errorMessage.Error = appmessage.RPCErrorf("Could not extract scriptPublicKey from address '%s': %s", addressString, err)
			return errorMessage, nil
		}

		if !getMempoolEntriesByAddressesRequest.FilterTransactionPool {

			if transaction, found := sendingInTransactionPool[scriptPublicKey.String()]; found {
				rpcTransaction := appmessage.DomainTransactionToRPCTransaction(transaction)
				err := context.PopulateTransactionWithVerboseData(rpcTransaction, nil)
				if err != nil {
					return nil, err
				}

				sending = append(sending, &appmessage.MempoolEntry{
					Fee:         transaction.Fee,
					Transaction: rpcTransaction,
					IsOrphan:    false,
				},
				)
			}

			if transaction, found := receivingInTransactionPool[scriptPublicKey.String()]; found {
				rpcTransaction := appmessage.DomainTransactionToRPCTransaction(transaction)
				err := context.PopulateTransactionWithVerboseData(rpcTransaction, nil)
				if err != nil {
					return nil, err
				}

				receiving = append(receiving, &appmessage.MempoolEntry{
					Fee:         transaction.Fee,
					Transaction: rpcTransaction,
					IsOrphan:    false,
				},
				)
			}
		}
		if getMempoolEntriesByAddressesRequest.IncludeOrphanPool {

			if transaction, found := sendingInOrphanPool[scriptPublicKey.String()]; found {
				rpcTransaction := appmessage.DomainTransactionToRPCTransaction(transaction)
				err := context.PopulateTransactionWithVerboseData(rpcTransaction, nil)
				if err != nil {
					return nil, err
				}

				sending = append(sending, &appmessage.MempoolEntry{
					Fee:         transaction.Fee,
					Transaction: rpcTransaction,
					IsOrphan:    true,
				},
				)
			}

			if transaction, found := receivingInOrphanPool[scriptPublicKey.String()]; found {
				rpcTransaction := appmessage.DomainTransactionToRPCTransaction(transaction)
				err := context.PopulateTransactionWithVerboseData(rpcTransaction, nil)
				if err != nil {
					return nil, err
				}

				receiving = append(receiving, &appmessage.MempoolEntry{
					Fee:         transaction.Fee,
					Transaction: rpcTransaction,
					IsOrphan:    true,
				},
				)
			}

		}

		if len(sending) > 0 || len(receiving) > 0 {
			mempoolEntriesByAddresses = append(
				mempoolEntriesByAddresses,
				&appmessage.MempoolEntryByAddress{
					Address:   address.String(),
					Sending:   sending,
					Receiving: receiving,
				},
			)
		}
	}

	return appmessage.NewGetMempoolEntriesByAddressesResponseMessage(mempoolEntriesByAddresses), nil
}
