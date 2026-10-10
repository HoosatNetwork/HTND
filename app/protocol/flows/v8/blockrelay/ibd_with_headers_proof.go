package blockrelay

import (
	"fmt"
	"strings"
	"time"

	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
	"github.com/HoosatNetwork/HTND/v2/app/protocol/common"
	"github.com/HoosatNetwork/HTND/v2/app/protocol/protocolerrors"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/ruleerrors"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/constants"
	"github.com/pkg/errors"
)

func (flow *handleIBDFlow) ibdWithHeadersProof(
	syncerHeaderSelectedTipHash, relayBlockHash *externalapi.DomainHash, highBlockDAAScore uint64,
) error {
	flow.updateBlockVersionFromDAAScore(highBlockDAAScore)
	err := flow.Domain().InitStagingConsensusWithoutGenesis()
	if err != nil {
		// If a staging consensus already exists (due to interrupted IBD), clean it up and retry
		if strings.Contains(err.Error(), "staging consensus already exists") {
			log.Warnf("Staging consensus already exists from previous interrupted IBD, cleaning up before retry")
			deleteStagingConsensusErr := flow.Domain().DeleteStagingConsensus()
			if deleteStagingConsensusErr != nil {
				log.Errorf("Failed to delete existing staging consensus: %s", deleteStagingConsensusErr)
				return deleteStagingConsensusErr
			}
			// Retry initialization after cleanup
			err = flow.Domain().InitStagingConsensusWithoutGenesis()
			if err != nil {
				return err
			}
		} else {
			return err
		}
	}

	err = flow.downloadHeadersAndPruningUTXOSet(syncerHeaderSelectedTipHash, relayBlockHash, highBlockDAAScore)
	if err != nil {
		// Check if this is the special error indicating pruning point is unchanged
		// In this case, we clean up and return nil to indicate success (skip headers proof)
		if err.Error() == "skip headers proof: pruning point unchanged" {
			deleteStagingConsensusErr := flow.Domain().DeleteStagingConsensus()
			if deleteStagingConsensusErr != nil {
				log.Errorf("Failed to delete staging consensus: %s", deleteStagingConsensusErr)
				return deleteStagingConsensusErr
			}
			return nil
		}

		if !flow.IsRecoverableError(err) {
			return err
		}

		log.Infof("IBD with pruning proof from %s was unsuccessful. Deleting the staging consensus. (%s)", flow.peer, err)
		deleteStagingConsensusErr := flow.Domain().DeleteStagingConsensus()
		flow.UnsetIBDRunning()
		if deleteStagingConsensusErr != nil {
			return deleteStagingConsensusErr
		}

		return err
	}

	log.Infof("Header download stage of IBD with pruning proof completed successfully from %s.", flow.peer)
	log.Infof("Committing the staging consensus and deleting the previous obsolete one if such exists.")
	err = flow.Domain().CommitStagingConsensus()
	if err != nil {
		return err
	}

	err = flow.OnPruningPointUTXOSetOverride()
	if err != nil {
		return err
	}

	return nil
}

func (flow *handleIBDFlow) requireStagingConsensus() (externalapi.Consensus, error) {
	stagingConsensus := flow.Domain().StagingConsensus()
	if stagingConsensus == nil {
		if err := flow.Domain().InitStagingConsensusWithoutGenesis(); err != nil {
			return nil, err
		}
		stagingConsensus = flow.Domain().StagingConsensus()
	}
	if stagingConsensus == nil {
		return nil, protocolerrors.New(false, "staging consensus is not ready")
	}

	return stagingConsensus, nil
}

// banPeerNow bans this peer without --enablebanning. The rule already failed.
// The console line is the notice. The address stays banned for BanDuration.
func (flow *handleIBDFlow) banPeerNow(reason string) {
	addr := flow.peer.Connection().NetAddress()
	if addr == nil {
		log.Warnf("could not ban %s (%s): no address", flow.peer, reason)
		return
	}
	if err := flow.AddressManager().Ban(addr); err != nil {
		log.Warnf("could not ban %s (%s): %s", flow.peer, reason, err)
		return
	}
	log.Infof("banned %s for 120 minutes (%s)", addr, reason)
}

func (flow *handleIBDFlow) shouldSyncAndShouldDownloadHeadersProof(
	relayBlock *externalapi.DomainBlock,
	highestKnownSyncerChainHash *externalapi.DomainHash,
) (shouldDownload, shouldSync bool, err error) {
	var highestSharedBlockFound, isPruningPointInSharedBlockChain bool
	if highestKnownSyncerChainHash != nil {
		blockInfo, err := flow.Domain().Consensus().GetBlockInfo(highestKnownSyncerChainHash)
		if err != nil {
			return false, false, err
		}

		highestSharedBlockFound = blockInfo.HasBody()
		pruningPoint, err := flow.Domain().Consensus().PruningPoint()
		if err != nil {
			return false, false, err
		}

		isPruningPointInSharedBlockChain, err = flow.Domain().Consensus().IsInSelectedParentChainOf(
			pruningPoint, highestKnownSyncerChainHash)
		if err != nil {
			return false, false, err
		}
	}
	// === Strong finality conflict signal ===
	// If we share a block but the pruning point is NOT in its selected parent chain,
	// the peer is on a chain that violates finality relative to our current state.
	log.Debugf("Highest Shared Block %s", highestKnownSyncerChainHash)
	log.Debugf("Is pruning point in shared block chain %t", isPruningPointInSharedBlockChain)
	if highestSharedBlockFound && !isPruningPointInSharedBlockChain {
		log.Warnf("Detected potential finality conflict: pruning point not in peer's shared chain. " +
			"Stopping IBD from this peer to avoid adopting incorrect history.")
		return false, false, nil
	}

	if !highestSharedBlockFound || !isPruningPointInSharedBlockChain {
		hasMoreBlueWorkThanSelectedTipAndPruningDepthMoreBlueScore, err := flow.checkIfHighHashHasMoreBlueWorkThanSelectedTipAndPruningDepthMoreBlueScore(relayBlock)
		if err != nil {
			return false, false, err
		}

		if hasMoreBlueWorkThanSelectedTipAndPruningDepthMoreBlueScore {
			// Kaspa spam protector. Two conditions: the local chain is live,
			// and this process has been up long enough that this is not a
			// consensus that was just created. A fresh node still syncs.
			if live, err := flow.localChainIsLive(); err != nil {
				return false, false, err
			} else if live && processIsMature() {
				log.Infof("refusing headers-proof IBD from %s: local chain is live", flow.peer)
				return false, false, nil
			}
			return true, true, nil
		}

		if highestKnownSyncerChainHash == nil {
			log.Infof("Stopping IBD since IBD from this node will cause a finality conflict")
			return false, false, nil
		}

		return false, true, nil
	}

	return false, true, nil
}

func (flow *handleIBDFlow) checkIfHighHashHasMoreBlueWorkThanSelectedTipAndPruningDepthMoreBlueScore(relayBlock *externalapi.DomainBlock) (bool, error) {
	virtualSelectedParent, err := flow.Domain().Consensus().GetVirtualSelectedParent()
	if err != nil {
		return false, err
	}

	virtualSelectedTipInfo, err := flow.Domain().Consensus().GetBlockInfo(virtualSelectedParent)
	if err != nil {
		return false, err
	}
	// Use the relay block's DAA score (not the header version) when
	// updating block version determination logic.
	flow.updateBlockVersionFromDAAScore(relayBlock.Header.DAAScore())
	if relayBlock.Header.BlueScore() < virtualSelectedTipInfo.BlueScore+flow.Config().NetParams().PruningDepth() {
		return false, nil
	}

	return relayBlock.Header.BlueWork().Cmp(virtualSelectedTipInfo.BlueWork) > 0, nil
}

// localChainIsLive reports whether the virtual tip is recent enough that a
// headers-proof IBD would replace a chain this node is already following.
// Kaspa uses the finality point timestamp and a 3/2 finality window. The
// public consensus API does not expose the finality point, so the tip
// timestamp is the same signal: a live tip means the chain is current.
func (flow *handleIBDFlow) localChainIsLive() (bool, error) {
	tip, err := flow.Domain().Consensus().GetVirtualSelectedParent()
	if err != nil {
		return false, err
	}
	if tip.Equal(flow.Config().NetParams().GenesisHash) {
		return false, nil
	}
	header, err := flow.Domain().Consensus().GetBlockHeader(tip)
	if err != nil {
		return false, err
	}
	params := flow.Config().NetParams()
	version := constants.GetBlockVersion()
	idx := int(version) - 1
	if idx < 0 || idx >= len(params.FinalityDuration) {
		idx = len(params.FinalityDuration) - 1
	}
	window := params.FinalityDuration[idx] * 3 / 2
	tipTime := time.UnixMilli(header.TimeInMilliseconds())
	return time.Since(tipTime) < window, nil
}

var processStartedAt = time.Now()

// processIsMature is the second Kaspa condition. Kaspa waits one finality
// window after the consensus object is created. That window is 24 hours, which
// would leave a restarted node open to the blue-score flood. Ten minutes is
// enough to tell a just-started process from one that is following the tip.
func processIsMature() bool {
	return time.Since(processStartedAt) > 10*time.Minute
}

const maxPruningPointProofBytes = 1024 * 1024 * 1024

func pruningPointProofTooLarge(msg *appmessage.MsgPruningPointProof) bool {
	var headers int
	for _, level := range msg.Headers {
		headers += len(level)
	}
	// A header on the wire is well under 1 KiB. 1 GiB of headers is not a proof.
	return headers > maxPruningPointProofBytes/1024
}

func (flow *handleIBDFlow) syncAndValidatePruningPointProof() (*externalapi.DomainHash, error) {
	log.Infof("Downloading the pruning point proof from %s", flow.peer)
	err := flow.outgoingRoute.Enqueue(appmessage.NewMsgRequestPruningPointProof())
	if err != nil {
		return nil, err
	}
	message, err := flow.incomingRoute.DequeueWithTimeout(common.DefaultTimeout)
	if err != nil {
		return nil, err
	}
	pruningPointProofMessage, ok := message.(*appmessage.MsgPruningPointProof)
	if !ok {
		return nil, protocolerrors.Errorf(true, "received unexpected message type. "+
			"expected: %s, got: %s", appmessage.CmdPruningPointProof, message.Command())
	}
	if pruningPointProofTooLarge(pruningPointProofMessage) {
		log.Infof("peer banned for 120 minutes, pruning-point proof exceeds 1 GiB: %s", flow.peer)
		flow.banPeerNow("pruning-point proof exceeds 1 GiB")
		return nil, protocolerrors.New(true, "pruning point proof exceeds 1 GiB")
	}
	pruningPointProof := appmessage.MsgPruningPointProofToDomainPruningPointProof(pruningPointProofMessage)
	err = flow.Domain().Consensus().ValidatePruningPointProof(pruningPointProof)
	if err != nil {
		if errors.As(err, &ruleerrors.RuleError{}) {
			return nil, protocolerrors.Wrapf(true, err, "pruning point proof validation failed")
		}
		return nil, err
	}

	stagingConsensus, err := flow.requireStagingConsensus()
	if err != nil {
		return nil, err
	}
	if stagingConsensus == nil {
		return nil, protocolerrors.New(false, "staging consensus is not ready")
	}

	err = stagingConsensus.ApplyPruningPointProof(pruningPointProof)
	if err != nil {
		return nil, err
	}

	return consensushashing.HeaderHash(pruningPointProof.Headers[0][len(pruningPointProof.Headers[0])-1]), nil
}

func (flow *handleIBDFlow) downloadHeadersAndPruningUTXOSet(
	syncerHeaderSelectedTipHash, relayBlockHash *externalapi.DomainHash,
	highBlockDAAScore uint64,
) error {
	proofPruningPoint, err := flow.syncAndValidatePruningPointProof()
	if err != nil {
		return err
	}

	// Check if the proof pruning point is the same as the current pruning point
	// If so, we don't need to do headers proof IBD
	currentPruningPoint, err := flow.Domain().Consensus().PruningPoint()
	if err != nil {
		return err
	}

	log.Debugf("Proof pruning point: %s, Current pruning point: %s", proofPruningPoint, currentPruningPoint)
	if currentPruningPoint.Equal(proofPruningPoint) {
		log.Infof("Proof pruning point is the same as current pruning point, skipping headers proof IBD")
		// Return a special error that the caller will recognize to skip headers proof
		return errors.New("skip headers proof: pruning point unchanged")
	}

	err = flow.syncPruningPointsAndPruningPointAnticone(proofPruningPoint)
	if err != nil {
		return err
	}

	// TODO: Remove this condition once there's more proper way to check finality violation
	// in the headers proof.
	if proofPruningPoint.Equal(flow.Config().NetParams().GenesisHash) {
		return protocolerrors.Errorf(true, "the genesis pruning point violates finality")
	}

	stagingConsensus, err := flow.requireStagingConsensus()
	if err != nil {
		return err
	}

	err = flow.syncPruningPointFutureHeaders(stagingConsensus,
		syncerHeaderSelectedTipHash, proofPruningPoint, relayBlockHash, highBlockDAAScore)
	if err != nil {
		return err
	}

	log.Infof("Headers downloaded from peer %s", flow.peer)

	relayBlockInfo, err := stagingConsensus.GetBlockInfo(relayBlockHash)
	if err != nil {
		return err
	}

	if !relayBlockInfo.Exists {
		return protocolerrors.Errorf(true, "the triggering IBD block was not sent")
	}

	err = flow.validatePruningPointFutureHeaderTimestamps()
	if err != nil {
		return err
	}

	// Accept the pruning point only if it is the root of the chain just downloaded, while refusing it
	// still means nothing more than deleting the staging consensus. See checkPruningPointMeetsChains.
	stagingHeadersSelectedTip, err := stagingConsensus.GetHeadersSelectedTip()
	if err != nil {
		return err
	}
	err = checkPruningPointMeetsChains(stagingConsensus, proofPruningPoint, []namedBlock{
		{name: "the syncer's headers selected tip", hash: syncerHeaderSelectedTipHash},
		{name: "the relay block", hash: relayBlockHash},
		{name: "this node's headers selected tip", hash: stagingHeadersSelectedTip},
	})
	if err != nil {
		log.Warnf("IBD with pruning proof from %s: %s", flow.peer, err)
		return err
	}

	log.Debugf("Syncing the current pruning point UTXO set")
	syncedPruningPointUTXOSetSuccessfully, err := flow.syncPruningPointUTXOSet(stagingConsensus, proofPruningPoint)
	if err != nil {
		return err
	}
	if !syncedPruningPointUTXOSetSuccessfully {
		log.Debugf("Aborting IBD because the pruning point UTXO set failed to sync")
		// Returning nil here would cause the caller to treat IBD as successful and commit the
		// staging consensus, even though critical pruning-point data is missing.
		// This must be a recoverable error so the staging consensus is deleted and IBD can retry.
		return protocolerrors.New(false, "pruning point UTXO set failed to sync")
	}
	log.Debugf("Finished syncing the current pruning point UTXO set")

	return nil
}

func (flow *handleIBDFlow) syncPruningPointsAndPruningPointAnticone(proofPruningPoint *externalapi.DomainHash) error {
	// Check if the proof pruning point is the same as the current pruning point
	// If so, no need to download pruning points and anticone
	currentPruningPoint, err := flow.Domain().Consensus().PruningPoint()
	if err != nil {
		return err
	}

	log.Debugf("In syncPruningPointsAndPruningPointAnticone: proofPruningPoint=%s, currentPruningPoint=%s", proofPruningPoint, currentPruningPoint)
	if currentPruningPoint.Equal(proofPruningPoint) {
		log.Debugf("Proof pruning point is the same as current pruning point, skipping pruning points and anticone sync")
		return nil
	}

	log.Infof("Downloading the past pruning points and the pruning point anticone from %s", flow.peer)
	err = flow.outgoingRoute.Enqueue(appmessage.NewMsgRequestPruningPointAndItsAnticone())
	if err != nil {
		return err
	}

	err = flow.validateAndInsertPruningPoints(proofPruningPoint)
	if err != nil {
		return err
	}

	message, err := flow.incomingRoute.DequeueWithTimeout(common.DefaultTimeout)
	if err != nil {
		return err
	}

	msgTrustedData, ok := message.(*appmessage.MsgTrustedData)
	if !ok {
		return protocolerrors.Errorf(true, "received unexpected message type. "+
			"expected: %s, got: %s", appmessage.CmdTrustedData, message.Command())
	}

	pruningPointWithMetaData, done, err := flow.receiveBlockWithTrustedData()
	if err != nil {
		return err
	}

	if done {
		return protocolerrors.Errorf(true, "got `done` message before receiving the pruning point")
	}

	if !pruningPointWithMetaData.Block.Header.BlockHash().Equal(proofPruningPoint) {
		return protocolerrors.Errorf(true, "first block with trusted data is not the pruning point")
	}

	err = flow.processBlockWithTrustedData(flow.Domain().StagingConsensus(), pruningPointWithMetaData, msgTrustedData)
	if err != nil {
		return err
	}

	// The rest of the anticone is collected first and inserted only once it is in topological order:
	// see orderBlocksWithTrustedDataTopologically for why the peer's order cannot be used as is.
	var anticoneBlocks []*appmessage.MsgBlockWithTrustedDataV4
	i := 0
	for ; ; i++ {
		blockWithTrustedData, done, err := flow.receiveBlockWithTrustedData()
		if err != nil {
			return err
		}

		if done {
			break
		}

		anticoneBlocks = append(anticoneBlocks, blockWithTrustedData)

		// We're using i+2 because we want to check if the next block will belong to the next batch, but we already downloaded
		// the pruning point outside the loop so we use i+2 instead of i+1.
		if (i+2)%getIBDBatchSize() == 0 {
			log.Infof("Downloaded %d blocks from the pruning point anticone", i+1)
			err := flow.outgoingRoute.Enqueue(appmessage.NewMsgRequestNextPruningPointAndItsAnticoneBlocks())
			if err != nil {
				return err
			}
		}
	}

	for _, blockWithTrustedData := range orderBlocksWithTrustedDataTopologically(anticoneBlocks) {
		err = flow.processBlockWithTrustedData(flow.Domain().StagingConsensus(), blockWithTrustedData, msgTrustedData)
		if err != nil {
			return err
		}
	}

	log.Infof("Finished downloading pruning point and its anticone from %s. Total blocks downloaded: %d", flow.peer, i+1)
	return nil
}

func (flow *handleIBDFlow) processBlockWithTrustedData(
	consensus externalapi.Consensus, block *appmessage.MsgBlockWithTrustedDataV4, data *appmessage.MsgTrustedData,
) error {
	blockWithTrustedData := &externalapi.BlockWithTrustedData{
		Block:        appmessage.MsgBlockToDomainBlock(block.Block),
		DAAWindow:    make([]*externalapi.TrustedDataDataDAAHeader, 0, len(block.DAAWindowIndices)),
		GHOSTDAGData: make([]*externalapi.BlockGHOSTDAGDataHashPair, 0, len(block.GHOSTDAGDataIndices)),
	}

	// The indices come from the peer and point into the trusted data the same peer sent earlier. Used
	// unchecked, an index past its end panicked the IBD flow, which took the node down.
	for _, index := range block.DAAWindowIndices {
		if index >= uint64(len(data.DAAWindow)) {
			return protocolerrors.Errorf(true, "block with trusted data references DAA window entry %d, "+
				"but only %d were sent", index, len(data.DAAWindow))
		}
		blockWithTrustedData.DAAWindow = append(blockWithTrustedData.DAAWindow, appmessage.TrustedDataDataDAABlockV4ToTrustedDataDataDAAHeader(data.DAAWindow[index]))
	}

	for _, index := range block.GHOSTDAGDataIndices {
		if index >= uint64(len(data.GHOSTDAGData)) {
			return protocolerrors.Errorf(true, "block with trusted data references GHOSTDAG data entry %d, "+
				"but only %d were sent", index, len(data.GHOSTDAGData))
		}
		blockWithTrustedData.GHOSTDAGData = append(blockWithTrustedData.GHOSTDAGData, appmessage.GHOSTDAGHashPairToDomainGHOSTDAGHashPair(data.GHOSTDAGData[index]))
	}

	err := consensus.ValidateAndInsertBlockWithTrustedData(blockWithTrustedData, false)
	if err != nil {
		if errors.As(err, &ruleerrors.RuleError{}) {
			return protocolerrors.Wrapf(true, err, "failed validating block with trusted data")
		}
		return err
	}
	return nil
}

func (flow *handleIBDFlow) receiveBlockWithTrustedData() (*appmessage.MsgBlockWithTrustedDataV4, bool, error) {
	message, err := flow.incomingRoute.DequeueWithTimeout(common.DefaultTimeout)
	if err != nil {
		return nil, false, err
	}

	switch downCastedMessage := message.(type) {
	case *appmessage.MsgBlockWithTrustedDataV4:
		return downCastedMessage, false, nil
	case *appmessage.MsgDoneBlocksWithTrustedData:
		return nil, true, nil
	default:
		return nil, false,
			protocolerrors.Errorf(true, "received unexpected message type. "+
				"expected: %s or %s, got: %s",
				(&appmessage.MsgBlockWithTrustedData{}).Command(),
				(&appmessage.MsgDoneBlocksWithTrustedData{}).Command(),
				downCastedMessage.Command())
	}
}

func (flow *handleIBDFlow) receivePruningPoints() (*appmessage.MsgPruningPoints, error) {
	message, err := flow.incomingRoute.DequeueWithTimeout(common.DefaultTimeout)
	if err != nil {
		return nil, err
	}

	msgPruningPoints, ok := message.(*appmessage.MsgPruningPoints)
	if !ok {
		return nil,
			protocolerrors.Errorf(true, "received unexpected message type. "+
				"expected: %s, got: %s", appmessage.CmdPruningPoints, message.Command())
	}

	return msgPruningPoints, nil
}

func (flow *handleIBDFlow) validateAndInsertPruningPoints(proofPruningPoint *externalapi.DomainHash) error {
	currentPruningPoint, err := flow.Domain().Consensus().PruningPoint()
	if err != nil {
		return err
	}

	if currentPruningPoint.Equal(proofPruningPoint) {
		// Pruning points are the same, no need to import
		log.Debugf("Proof pruning point is the same as current pruning point, skipping import")
		return nil
	}

	pruningPoints, err := flow.receivePruningPoints()
	if err != nil {
		return err
	}

	headers := make([]externalapi.BlockHeader, len(pruningPoints.Headers))
	for i := 0; i < len(pruningPoints.Headers); i++ {
		headers[i] = appmessage.BlockHeaderToDomainBlockHeader(pruningPoints.Headers[i])
	}

	arePruningPointsViolatingFinality, err := flow.Domain().Consensus().ArePruningPointsViolatingFinality(headers)
	if err != nil {
		return err
	}

	if arePruningPointsViolatingFinality {
		log.Infof("peer banned for 120 minutes, pruning points violate finality: %s", flow.peer)
		log.Infof("looking for another peer now")
		flow.banPeerNow("pruning points violate finality")
		flow.UnsetIBDRunning()
		return protocolerrors.Errorf(true, "pruning points are violating finality")
	}

	lastPruningPoint := consensushashing.HeaderHash(headers[len(headers)-1])
	if !lastPruningPoint.Equal(proofPruningPoint) {
		return protocolerrors.Errorf(true, "the proof pruning point is not equal to the last pruning point in the list")
	}

	err = flow.Domain().StagingConsensus().ImportPruningPoints(headers)
	if err != nil {
		return err
	}

	return nil
}

func (flow *handleIBDFlow) syncPruningPointUTXOSet(consensus externalapi.Consensus,
	pruningPoint *externalapi.DomainHash,
) (bool, error) {
	log.Infof("Checking if the suggested pruning point %s is compatible to the node DAG", pruningPoint)
	// isValid, err := flow.Domain().StagingConsensus().IsValidPruningPoint(pruningPoint)
	// if err != nil {
	// 	return false, err
	// }

	// if !isValid {
	// 	return false, protocolerrors.Errorf(true, "invalid pruning point %s", pruningPoint)
	// }

	if !flow.peerMaySupplyCoinSet() {
		log.Warnf("Not fetching pruning-point coin set from peer %s (utxobase=%s empty=%v forbidden=%v)",
			flow.peer, flow.peer.UTXOBaselineAdvertised(), flow.localFloorIsGenesis(), flow.peer.IBDCoinSetForbidden())
		return false, nil
	}
	log.Info("Fetching the pruning point UTXO set")
	isSuccessful, err := flow.fetchMissingUTXOSet(consensus, pruningPoint)
	if err != nil {
		log.Infof("An error occurred while fetching the pruning point UTXO set. Stopping IBD. (%s)", err)
		if !flow.localFloorIsGenesis() {
			flow.peer.ForbidIBDCoinSet()
		}
		return false, err
	}

	if !isSuccessful {
		log.Infof("Couldn't successfully fetch the pruning point UTXO set. Stopping IBD.")
		if !flow.localFloorIsGenesis() {
			flow.peer.ForbidIBDCoinSet()
		}
		return false, nil
	}

	log.Info("Fetched the new pruning point UTXO set")
	return true, nil
}

func (flow *handleIBDFlow) fetchMissingUTXOSet(consensus externalapi.Consensus, pruningPointHash *externalapi.DomainHash) (succeed bool, err error) {
	defer func() {
		err := flow.Domain().StagingConsensus().ClearImportedPruningPointData()
		if err != nil {
			panic(fmt.Sprintf("failed to clear imported pruning point data: %s", err))
		}
	}()

	err = flow.outgoingRoute.Enqueue(appmessage.NewMsgRequestPruningPointUTXOSet(pruningPointHash))
	if err != nil {
		return false, err
	}

	receivedAll, err := flow.receiveAndInsertPruningPointUTXOSet(consensus, pruningPointHash)
	if err != nil {
		return false, err
	}
	if !receivedAll {
		return false, nil
	}

	err = flow.Domain().StagingConsensus().ValidateAndInsertImportedPruningPoint(pruningPointHash)
	if err != nil {
		// TODO: Find a better way to deal with finality conflicts.
		if errors.Is(err, ruleerrors.ErrSuggestedPruningViolatesFinality) {
			return false, nil
		}
		// For ErrBadPruningPointUTXOSet, this is likely due to missing UTXO diffs from disqualified blocks.
		// This is a recoverable error - the node should try a different peer rather than banning.
		if errors.Is(err, ruleerrors.ErrBadPruningPointUTXOSet) {
			log.Infof("peer banned for 120 minutes, pruning-point set does not match the header: %s", flow.peer)
			flow.banPeerNow("pruning-point set does not match the header")
			return false, protocolerrors.New(true, "pruning point UTXO set hash mismatch: "+err.Error())
		}
		// ErrMissingTxOut here means the served set does not hold an output that the pruning point block
		// itself spends. That is a property of the chain's UTXO state, not of the peer: the peer served
		// what its own pruning point UTXO set holds, and every other peer holds the same state. Banning
		// for it worked through the peer list one expensive full-UTXO-set download at a time and never
		// synced. The import tolerates this case now, so reaching here means it came from somewhere else
		// in the import - still not the peer's fault, so disconnect without banning and try another node.
		if errors.As(err, &ruleerrors.ErrMissingTxOut{}) {
			log.Infof("The pruning point UTXO set from %s is missing outputs spent by the pruning point "+
				"block itself. This is chain state rather than peer misbehaviour. Will try another node. (%s)",
				flow.peer, err)
			return false, protocolerrors.New(false, "pruning point UTXO set is missing outputs spent by "+
				"the pruning point block: "+err.Error())
		}
		return false, protocolerrors.ConvertToBanningProtocolErrorIfRuleError(err, "error with pruning point UTXO set")
	}

	return true, nil
}
