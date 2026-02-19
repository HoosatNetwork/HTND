package blockrelay

import (
	"sync/atomic"

	"github.com/Hoosat-Oy/HTND/app/appmessage"
	peerpkg "github.com/Hoosat-Oy/HTND/app/protocol/peer"
	"github.com/Hoosat-Oy/HTND/app/protocol/protocolerrors"
	"github.com/Hoosat-Oy/HTND/domain"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/constants"
	"github.com/Hoosat-Oy/HTND/infrastructure/config"
	"github.com/Hoosat-Oy/HTND/infrastructure/network/netadapter/router"
)

// PruningPointAndItsAnticoneRequestsContext is the interface for the context needed for the HandlePruningPointAndItsAnticoneRequests flow.
type PruningPointAndItsAnticoneRequestsContext interface {
	Domain() domain.Domain
	Config() *config.Config
}

var isBusy uint32

// HandlePruningPointAndItsAnticoneRequests listens to appmessage.MsgRequestPruningPointAndItsAnticone messages and sends
// the pruning point and its anticone to the requesting peer.
func HandlePruningPointAndItsAnticoneRequests(context PruningPointAndItsAnticoneRequestsContext, incomingRoute *router.Route,
	outgoingRoute *router.Route, peer *peerpkg.Peer) error {

	for {
		err := func() error {
			_, err := incomingRoute.Dequeue()
			if err != nil {
				return err
			}

			if !atomic.CompareAndSwapUint32(&isBusy, 0, 1) {
				return protocolerrors.Errorf(false, "node is busy with other pruning point anticone requests")
			}
			defer atomic.StoreUint32(&isBusy, 0)

			log.Debugf("Got request for pruning point and its anticone from %s", peer)

			pruningPointHeaders, err := context.Domain().Consensus().PruningPointHeaders()
			if err != nil {
				return err
			}

			msgPruningPointHeaders := make([]*appmessage.MsgBlockHeader, len(pruningPointHeaders))
			for i := range pruningPointHeaders {
				msgPruningPointHeaders[i] = appmessage.DomainBlockHeaderToBlockHeader(pruningPointHeaders[i])
			}

			err = outgoingRoute.Enqueue(appmessage.NewMsgPruningPoints(msgPruningPointHeaders))
			if err != nil {
				return err
			}

			pointAndItsAnticone, err := context.Domain().Consensus().PruningPointAndItsAnticone()
			if err != nil {
				return err
			}

			windowSize := context.Config().NetParams().DifficultyAdjustmentWindowSize[constants.GetBlockVersion()-1]
			daaWindowBlocks := make([]*externalapi.TrustedDataDataDAAHeader, 0, windowSize)
			daaWindowHashesToIndex := make(map[externalapi.DomainHash]int, windowSize)
			trustedDataDAABlockIndexes := make(map[externalapi.DomainHash][]uint64)

			ghostdagData := make([]*externalapi.BlockGHOSTDAGDataHashPair, 0)
			ghostdagDataHashToIndex := make(map[externalapi.DomainHash]int)
			trustedDataGHOSTDAGDataIndexes := make(map[externalapi.DomainHash][]uint64)
			for i := range pointAndItsAnticone {
				blockDAAWindowHashes, err := context.Domain().Consensus().BlockDAAWindowHashes(pointAndItsAnticone[i])
				if err != nil {
					return err
				}

				trustedDataDAABlockIndexes[*pointAndItsAnticone[i]] = make([]uint64, 0, windowSize)
				for x := range blockDAAWindowHashes {
					index, exists := daaWindowHashesToIndex[*blockDAAWindowHashes[x]]
					if !exists {
						trustedDataDataDAAHeader, err := context.Domain().Consensus().TrustedDataDataDAAHeader(pointAndItsAnticone[i], blockDAAWindowHashes[x], uint64(i))
						if err != nil {
							return err
						}
						daaWindowBlocks = append(daaWindowBlocks, trustedDataDataDAAHeader)
						index = len(daaWindowBlocks) - 1
						daaWindowHashesToIndex[*blockDAAWindowHashes[x]] = index
					}

					trustedDataDAABlockIndexes[*pointAndItsAnticone[i]] = append(trustedDataDAABlockIndexes[*pointAndItsAnticone[i]], uint64(index))
				}

				ghostdagDataBlockHashes, err := context.Domain().Consensus().TrustedBlockAssociatedGHOSTDAGDataBlockHashes(pointAndItsAnticone[i])
				if err != nil {
					return err
				}

				trustedDataGHOSTDAGDataIndexes[*pointAndItsAnticone[i]] = make([]uint64, 0, context.Config().NetParams().K[constants.GetBlockVersion()-1])
				for y := range ghostdagDataBlockHashes {
					index, exists := ghostdagDataHashToIndex[*ghostdagDataBlockHashes[y]]
					if !exists {
						data, err := context.Domain().Consensus().TrustedGHOSTDAGData(ghostdagDataBlockHashes[y])
						if err != nil {
							return err
						}
						ghostdagData = append(ghostdagData, &externalapi.BlockGHOSTDAGDataHashPair{
							Hash:         ghostdagDataBlockHashes[y],
							GHOSTDAGData: data,
						})
						index = len(ghostdagData) - 1
						ghostdagDataHashToIndex[*ghostdagDataBlockHashes[y]] = index
					}

					trustedDataGHOSTDAGDataIndexes[*pointAndItsAnticone[i]] = append(trustedDataGHOSTDAGDataIndexes[*pointAndItsAnticone[i]], uint64(index))
				}
			}

			err = outgoingRoute.Enqueue(appmessage.DomainTrustedDataToTrustedData(daaWindowBlocks, ghostdagData))
			if err != nil {
				return err
			}
			for i := range pointAndItsAnticone {
				block, found, err := context.Domain().Consensus().GetBlock(pointAndItsAnticone[i])
				if err != nil {
					return err
				}

				if !found {
					return protocolerrors.Errorf(false, "pruning point anticone block %s not found", pointAndItsAnticone[i])
				}

				err = outgoingRoute.Enqueue(appmessage.DomainBlockWithTrustedDataToBlockWithTrustedDataV4(block, trustedDataDAABlockIndexes[*pointAndItsAnticone[i]], trustedDataGHOSTDAGDataIndexes[*pointAndItsAnticone[i]]))
				if err != nil {
					return err
				}

				if (i+1)%getIBDBatchSize() == 0 {
					// No timeout here, as we don't care if the syncee takes its time computing,
					// since it only blocks this dedicated flow
					message, err := incomingRoute.Dequeue()
					if err != nil {
						return err
					}
					if _, ok := message.(*appmessage.MsgRequestNextPruningPointAndItsAnticoneBlocks); !ok {
						return protocolerrors.Errorf(true, "received unexpected message type. "+
							"expected: %s, got: %s", appmessage.CmdRequestNextPruningPointAndItsAnticoneBlocks, message.Command())
					}
				}
			}

			err = outgoingRoute.Enqueue(appmessage.NewMsgDoneBlocksWithTrustedData())
			if err != nil {
				return err
			}

			log.Debugf("Sent pruning point and its anticone to %s", peer)
			return nil
		}()
		if err != nil {
			return err
		}
	}
}
