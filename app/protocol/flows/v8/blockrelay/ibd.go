package blockrelay

import (
	"sort"
	"time"

	"github.com/HoosatNetwork/HTND/app/appmessage"
	peerpkg "github.com/HoosatNetwork/HTND/app/protocol/peer"
	"github.com/HoosatNetwork/HTND/app/protocol/protocolerrors"
	"github.com/HoosatNetwork/HTND/domain"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/ruleerrors"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/infrastructure/config"
	"github.com/HoosatNetwork/HTND/infrastructure/db/database"
	"github.com/HoosatNetwork/HTND/infrastructure/logger"
	"github.com/HoosatNetwork/HTND/infrastructure/network/addressmanager"
	"github.com/HoosatNetwork/HTND/infrastructure/network/netadapter/router"
	"github.com/pkg/errors"
)

func wrapResolveVirtualError(err error) error {
	if err == nil {
		return nil
	}
	if database.IsNotFoundError(err) {
		return err
	}
	if errors.As(err, &ruleerrors.RuleError{}) {
		return protocolerrors.Wrapf(true, err, "resolve virtual failed during IBD")
	}
	return protocolerrors.Wrapf(false, err, "resolve virtual failed during IBD")
}

// IBDContext is the interface for the context needed for the HandleIBD flow.
type IBDContext interface {
	Domain() domain.Domain
	Config() *config.Config
	OnNewBlock(block *externalapi.DomainBlock) error
	OnNewBlockTemplate() error
	OnPruningPointUTXOSetOverride() error
	IsIBDRunning() bool
	TrySetIBDRunning(ibdPeer *peerpkg.Peer, isNearlySynced bool) bool
	UnsetIBDRunning()
	IsRecoverableError(err error) bool
	AddressManager() *addressmanager.AddressManager
}

type handleIBDFlow struct {
	IBDContext
	incomingRoute, outgoingRoute *router.Route
	peer                         *peerpkg.Peer
	lastRateCheckTime            time.Time
	consecutiveLowRateCount      int
	minHeadersPerSecond          float64
	minBlocksPerSecond           float64
	slowIBDTicks                 int
	headersProcessedSinceLast    int64 // Track since last check
	blocksProcessedSinceLast     int64
}

// HandleIBD handles IBD
func HandleIBD(context IBDContext, incomingRoute *router.Route, outgoingRoute *router.Route,
	peer *peerpkg.Peer,
) error {
	flow := &handleIBDFlow{
		IBDContext:    context,
		incomingRoute: incomingRoute,
		outgoingRoute: outgoingRoute,
		peer:          peer,
	}
	return flow.start()
}

func (flow *handleIBDFlow) start() error {
	for {
		// Wait for IBD requests triggered by other flows
		block, ok := <-flow.peer.IBDRequestChannel()
		if !ok {
			return nil
		}
		err := flow.runIBDIfNotRunning(block)
		if err != nil {
			return err
		}
	}
}

func (flow *handleIBDFlow) updateBlockVersionFromDAAScore(daaScore uint64) {
	var blockVersion uint16 = 1
	for _, powScore := range flow.IBDContext.Config().ActiveNetParams.POWScores {
		if daaScore >= powScore {
			blockVersion++
		}
	}
	constants.SetBlockVersion(blockVersion)
}

func (flow *handleIBDFlow) runIBDIfNotRunning(block *externalapi.DomainBlock) error {
	isNearlySynced, errNs := flow.Domain().Consensus().IsNearlySynced()
	if errNs != nil {
		isNearlySynced = false // If we can't tell, err on the side of caution
	}

	wasIBDNotRunning := flow.TrySetIBDRunning(flow.peer, isNearlySynced)
	if !wasIBDNotRunning {
		log.Debugf("IBD is already running")
		return nil
	}

	flow.lastRateCheckTime = time.Now()
	flow.consecutiveLowRateCount = 0
	flow.minHeadersPerSecond = float64(flow.Config().MinHeadersPerSecond)
	flow.minBlocksPerSecond = float64(flow.Config().MinBlocksPerSecond)
	flow.slowIBDTicks = 5 // 30 seconds when done in 10 second slices
	flow.headersProcessedSinceLast = 0
	flow.blocksProcessedSinceLast = 0

	flow.updateBlockVersionFromDAAScore(block.Header.DAAScore())
	isFinishedSuccessfully := false
	var err error
	defer func() {
		flow.UnsetIBDRunning()
		err = flow.logIBDFinished(isFinishedSuccessfully, err)
	}()

	// Determine timeout based on sync status
	timeout := flow.getIBDTimeout()

	// Channel to receive the IBD result
	ibdDone := make(chan error, 1)

	// Run IBD in a goroutine
	go func() {
		ibdDone <- flow.runIBD(block)
	}()

	// Wait for IBD to complete or timeout
	select {
	case err = <-ibdDone:
		if err == nil {
			isFinishedSuccessfully = true
		}
	case <-time.After(timeout):
		if !flow.Config().DisableIBDTimeout || timeout == 0 {
			log.Warnf("IBD with peer %s timed out after %v, disconnecting and trying to ban the peer depending on --enablebanning setting", flow.peer, timeout)
			// Disconnect & Remove the peer from address manager to prevent immediate reconnection
			if err := flow.logIBDFinished(false, protocolerrors.Errorf(false, "IBD timed out")); err != nil {
				log.Warnf("logIBDFinished returned error: %v", err)
			}
			netAddress := flow.peer.Connection().NetAddress()
			if err := flow.AddressManager().RemoveAddress(netAddress); err != nil {
				log.Warnf("Failed to remove address %s from address manager: %v", netAddress, err)
			}
			flow.peer.Connection().Disconnect()
			return protocolerrors.Errorf(true, "IBD timed out, nothing to worry, we will find another peer soon!")
		}
	}

	return err
}

func (flow *handleIBDFlow) getIBDTimeout() time.Duration {
	if !flow.Config().DisableIBDTimeout {
		isNearlySynced, err := flow.Domain().Consensus().IsNearlySynced()
		if err != nil {
			log.Warnf("Failed to check if nearly synced, using default timeout: %v", err)
			return flow.Config().IBDTimeout
		}

		if isNearlySynced {
			// If nearly synced, IBD should be faster, use shorter timeout
			return flow.Config().NearlySyncedIBDTimeout
		}
		// If not nearly synced, allow more time for IBD
		return flow.Config().IBDTimeout
	}
	return 0
}

func (flow *handleIBDFlow) runIBD(block *externalapi.DomainBlock) error {
	relayBlockHash := consensushashing.BlockHash(block)

	log.Infof("IBD started with peer %s and relayBlockHash %s", flow.peer, relayBlockHash)
	log.Infof("Syncing blocks up to %s", relayBlockHash)
	log.Infof("Trying to find highest known syncer chain block from peer %s with relay hash %s", flow.peer, relayBlockHash)

	syncerHeaderSelectedTipHash, highestKnownSyncerChainHash, err := flow.negotiateMissingSyncerChainSegment(nil, nil)
	if err != nil {
		return err
	}

	shouldDownloadHeadersProof, shouldSync, err := flow.shouldSyncAndShouldDownloadHeadersProof(block, highestKnownSyncerChainHash)
	if err != nil {
		return err
	}

	if !shouldSync {
		return nil
	}

	if shouldDownloadHeadersProof {
		log.Infof("Starting IBD with headers proof")
		err = flow.ibdWithHeadersProof(syncerHeaderSelectedTipHash, relayBlockHash, block.Header.DAAScore())
		if err != nil {
			return err
		}
	} else {
		// When doing sync without headers proof we need to revalidate that the syncee tip
		// and highest known syncer hash can negototiate, so that syncee wont sync from malicious node
		// which would pollute useless headers to the node.
		// tips, err := flow.Domain().Consensus().Tips()
		// if err != nil {
		// 	return err
		// }

		// if !relayBlockHash.Equal(flow.Config().NetParams().GenesisHash) {
		// 	syncerHeaderSelectedTipHash, highestKnownSyncerChainHash, err = flow.negotiateMissingSyncerChainSegment(tips[0], syncerHeaderSelectedTipHash)
		// 	if err != nil {
		// 		return err
		// 	}

		// 	_, shouldSync, err := flow.shouldSyncAndShouldDownloadHeadersProof(block, highestKnownSyncerChainHash)
		// 	if err != nil {
		// 		return err
		// 	}

		// 	if !shouldSync {
		// 		return nil
		// 	}
		// }

		if flow.Config().NetParams().DisallowDirectBlocksOnTopOfGenesis && !flow.Config().AllowSubmitBlockWhenNotSynced {
			isGenesisVirtualSelectedParent, err := flow.isGenesisVirtualSelectedParent()
			if err != nil {
				return err
			}

			if isGenesisVirtualSelectedParent {
				log.Infof("Cannot IBD to %s because it won't change the pruning point. The node needs to IBD "+
					"to the recent pruning point before normal operation can resume.", relayBlockHash)
				return nil
			}
		}

		err = flow.syncPruningPointFutureHeaders(
			flow.Domain().Consensus(),
			syncerHeaderSelectedTipHash, highestKnownSyncerChainHash, relayBlockHash, block.Header.DAAScore())
		if err != nil {
			return err
		}
	}

	// We start by syncing missing bodies over the syncer selected chain
	log.Infof("Starting sync missing block bodies")
	err = flow.syncMissingBlockBodies(syncerHeaderSelectedTipHash)
	if err != nil {
		return err
	}
	log.Info("Check if relay block hash is in the anticone of the syncer selected tip")
	relayBlockInfo, err := flow.Domain().Consensus().GetBlockInfo(relayBlockHash)
	if err != nil {
		return err
	}
	// Relay block might be in the anticone of syncer selected tip, thus
	// check his chain for missing bodies as well.
	// Note: this operation can be slightly optimized to avoid the full chain search since relay block
	// is in syncer virtual mergeset which has bounded size.
	if relayBlockInfo.BlockStatus == externalapi.StatusHeaderOnly {
		err = flow.syncMissingBlockBodies(relayBlockHash)
		if err != nil {
			return err
		}
	}

	log.Infof("Finished syncing blocks up to %s", relayBlockHash)

	return nil
}

func (flow *handleIBDFlow) negotiateMissingSyncerChainSegment(highHash *externalapi.DomainHash, lowHash *externalapi.DomainHash) (*externalapi.DomainHash, *externalapi.DomainHash, error) {
	/*
		Algorithm:
			Request full selected chain block locator from syncer
			Find the highest block which we know
			Repeat the locator step over the new range until finding max(past(syncee) \cap chain(syncer))
	*/

	// Empty hashes indicate that the full chain is queried
	locatorHashes, err := flow.getSyncerChainBlockLocator(highHash, lowHash, time.Minute*30)
	if err != nil {
		return nil, nil, err
	}
	if len(locatorHashes) == 0 {
		return nil, nil, protocolerrors.Errorf(true, "Expecting initial syncer chain block locator "+
			"to contain at least one element")
	}
	log.Debugf("IBD chain negotiation with peer %s started and received %d hashes (%s, %s)", flow.peer,
		len(locatorHashes), locatorHashes[0], locatorHashes[len(locatorHashes)-1])
	syncerHeaderSelectedTipHash := locatorHashes[0]
	var highestKnownSyncerChainHash *externalapi.DomainHash
	chainNegotiationRestartCounter := 0
	chainNegotiationZoomCounts := 0
	maxZoomSteps := len(locatorHashes) * 64
	// [IBD-DEBUG] Tracks the previous zoom step's bounds so non-convergence (bounds not actually
	// narrowing between iterations) can be detected and logged - see the zoom-in loop below.
	var lastZoomLow, lastZoomHigh *externalapi.DomainHash
	// [IBD-DEBUG] Tracks how many consecutive zoom steps had unchanged bounds, and - if the
	// non-convergence is explained by a locally disqualified block, which can never become
	// "known" no matter how many times it's re-queried - that block's hash. See the early-exit
	// check below: a disqualified block getting stuck here means OUR local data is the problem,
	// not the peer, so banning the peer would be both wrong and pointless.
	consecutiveUnchangedZoomSteps := 0
	var stuckOnDisqualifiedHash *externalapi.DomainHash
	pruningPoint, err := flow.Domain().Consensus().PruningPoint()
	if err != nil {
		return nil, nil, err
	}

	for {
		var lowestUnknownSyncerChainHash, currentHighestKnownSyncerChainHash *externalapi.DomainHash
		var lowestUnknownIsDisqualified bool
		for i := 0; i < len(locatorHashes); i++ {
			info, err := flow.Domain().Consensus().GetBlockInfo(locatorHashes[i])
			if err != nil {
				return nil, nil, err
			}
			if info.Exists {
				if info.BlockStatus == externalapi.StatusInvalid {
					return nil, nil, protocolerrors.Errorf(false, "Sent invalid chain block %s", locatorHashes[i])
				}

				if info.BlockStatus == externalapi.StatusHeaderOnly {
					continue
				}

				isPruningPointOnSyncerChain, err := flow.Domain().Consensus().IsInSelectedParentChainOf(pruningPoint, locatorHashes[i])
				if err != nil {
					// locatorHashes[i] exists locally and isn't header-only, so its reachability
					// data is expected to be present. An error here means our local data for this
					// block is missing or corrupted - silently treating it as "unknown" would make
					// the zoom-in loop re-derive the same boundary forever and never converge, so
					// surface the error instead of masking it.
					return nil, nil, errors.Wrapf(err, "failed checking isPruningPointOnSyncerChain for %s", locatorHashes[i])
				}

				// We're only interested in syncer chain blocks that have our pruning
				// point in their selected chain. Otherwise, it means one of the following:
				// 1) We will not switch the virtual selected chain to the syncers chain since it will violate finality
				//    (hence we can ignore it unless merged by others).
				// 2) syncerChainHash is actually in the past of our pruning point so there's no
				//    point in syncing from it.
				if isPruningPointOnSyncerChain {
					currentHighestKnownSyncerChainHash = locatorHashes[i]
					break
				}
			}
			lowestUnknownSyncerChainHash = locatorHashes[i]
			lowestUnknownIsDisqualified = info.Exists && info.BlockStatus == externalapi.StatusDisqualifiedFromChain
		}
		// No unknown blocks, break. Note this can only happen in the first iteration
		if lowestUnknownSyncerChainHash == nil {
			highestKnownSyncerChainHash = currentHighestKnownSyncerChainHash
			break
		}
		// No shared block, break
		if currentHighestKnownSyncerChainHash == nil {
			highestKnownSyncerChainHash = nil
			break
		}
		// No point in zooming further
		if len(locatorHashes) == 1 {
			highestKnownSyncerChainHash = currentHighestKnownSyncerChainHash
			break
		}
		// Zoom in
		locatorHashes, err = flow.getSyncerChainBlockLocator(
			lowestUnknownSyncerChainHash,
			currentHighestKnownSyncerChainHash, time.Second*10)
		if err != nil {
			return nil, nil, err
		}
		if len(locatorHashes) > 0 {
			if !locatorHashes[0].Equal(lowestUnknownSyncerChainHash) ||
				!locatorHashes[len(locatorHashes)-1].Equal(currentHighestKnownSyncerChainHash) {
				return nil, nil, protocolerrors.Errorf(true, "Expecting the high and low "+
					"hashes to match the locator bounds")
			}

			chainNegotiationZoomCounts++
			log.Debugf("IBD chain negotiation with peer %s zoomed in (%d) and received %d hashes (%s, %s)", flow.peer,
				chainNegotiationZoomCounts, len(locatorHashes), locatorHashes[0], locatorHashes[len(locatorHashes)-1])

			// [IBD-DEBUG] A properly-narrowing exponential search should converge in a few dozen
			// steps even for a chain of hundreds of millions of blocks (log2), not the 1000+ seen
			// before banning peers - which means the (low, high) bounds sent to the peer likely
			// aren't actually narrowing between iterations. Surface the bounds (and whether they
			// changed since last iteration) at a visible level, rate-limited, so a non-converging
			// run can actually be diagnosed instead of just banning the peer and moving on.
			boundsUnchanged := lastZoomLow != nil && lastZoomHigh != nil &&
				lastZoomLow.Equal(lowestUnknownSyncerChainHash) && lastZoomHigh.Equal(currentHighestKnownSyncerChainHash)
			if chainNegotiationZoomCounts <= 20 || chainNegotiationZoomCounts%50 == 0 {
				log.Warnf("[IBD-DEBUG] zoom step %d/%d with peer %s: bounds (low=%s, high=%s), %d hashes returned, "+
					"unchanged-since-last-step=%t", chainNegotiationZoomCounts, maxZoomSteps, flow.peer,
					lowestUnknownSyncerChainHash, currentHighestKnownSyncerChainHash, len(locatorHashes), boundsUnchanged)
			}
			lastZoomLow, lastZoomHigh = lowestUnknownSyncerChainHash, currentHighestKnownSyncerChainHash

			if boundsUnchanged {
				consecutiveUnchangedZoomSteps++
				if lowestUnknownIsDisqualified {
					stuckOnDisqualifiedHash = lowestUnknownSyncerChainHash
				}
			} else {
				consecutiveUnchangedZoomSteps = 0
				stuckOnDisqualifiedHash = nil
			}

			// A block that's locally StatusDisqualifiedFromChain can never become "known" no
			// matter how many times this same boundary gets re-queried - the peer's responses are
			// consistent and the bounds will never narrow, so the normal maxZoomSteps ban below
			// would eventually fire on a peer that did nothing wrong. Bail out well before that,
			// without banning, and point directly at the local block responsible so it can be
			// investigated with the pruning/disqualification diagnostics instead.
			if consecutiveUnchangedZoomSteps >= 20 && stuckOnDisqualifiedHash != nil {
				return nil, nil, errors.Errorf("IBD chain negotiation with peer %s is stuck (%d consecutive "+
					"unchanged zoom steps) because local block %s is StatusDisqualifiedFromChain and can "+
					"never resolve as known - this is a local data problem, not a misbehaving peer, so not "+
					"banning it. Investigate %s with FindAndReproduceRootDisqualification/"+
					"--enable-utxo-debug-diagnostics", flow.peer, consecutiveUnchangedZoomSteps,
					stuckOnDisqualifiedHash, stuckOnDisqualifiedHash)
			}

			// [IBD-DEBUG] Stuck for a while and NOT explained by a disqualified block - dump every
			// entry in the current locator window (status, and whether our pruning point is on its
			// selected chain) exactly once, so the actual reason this specific window can never
			// narrow is visible instead of inferred. Fires once per stuck run (not every iteration)
			// via the == check.
			if consecutiveUnchangedZoomSteps == 20 && stuckOnDisqualifiedHash == nil {
				log.Warnf("[IBD-DEBUG] zoom stuck for %d consecutive steps with peer %s, not explained by a "+
					"disqualified block - dumping full locator window (%d entries) for direct diagnosis:",
					consecutiveUnchangedZoomSteps, flow.peer, len(locatorHashes))
				for i, hash := range locatorHashes {
					info, infoErr := flow.Domain().Consensus().GetBlockInfo(hash)
					if infoErr != nil {
						log.Warnf("[IBD-DEBUG]   [%d] %s: GetBlockInfo failed: %s", i, hash, infoErr)
						continue
					}
					if !info.Exists {
						log.Warnf("[IBD-DEBUG]   [%d] %s: does not exist locally", i, hash)
						continue
					}
					if info.BlockStatus == externalapi.StatusHeaderOnly {
						log.Warnf("[IBD-DEBUG]   [%d] %s: status=%s (header downloaded, no body)",
							i, hash, info.BlockStatus)
						continue
					}
					onSyncerChain, chainErr := flow.Domain().Consensus().IsInSelectedParentChainOf(pruningPoint, hash)
					if chainErr != nil {
						log.Warnf("[IBD-DEBUG]   [%d] %s: status=%s, IsInSelectedParentChainOf failed: %s",
							i, hash, info.BlockStatus, chainErr)
						continue
					}
					log.Warnf("[IBD-DEBUG]   [%d] %s: status=%s, pruningPointOnItsChain=%t",
						i, hash, info.BlockStatus, onSyncerChain)
				}
			}

			if len(locatorHashes) == 2 {
				// We found our search target
				highestKnownSyncerChainHash = currentHighestKnownSyncerChainHash
				break
			}
			// Since the zoom-in always queries two consecutive entries in the previous locator, it is
			// expected to decrease in size at least every two iterations. Use a bound based on the
			// original locator size to detect if we're stuck in a loop. If we exceed the bound,
			// ban the peer as it may be misbehaving.
			if chainNegotiationZoomCounts > maxZoomSteps {
				log.Warnf("IBD chain negotiation: Number of zoom-in steps %d exceeded the upper bound of %d, with %d locatorhashes. "+
					"Banning peer %s",
					chainNegotiationZoomCounts, maxZoomSteps, len(locatorHashes), flow.peer)
				// Ban the misbehaving peer
				netAddress := flow.peer.Connection().NetAddress()
				if err := flow.AddressManager().RemoveAddress(netAddress); err != nil {
					log.Warnf("Failed to remove address %s from address manager: %v", netAddress, err)
				}
				flow.peer.Connection().Disconnect()
				highestKnownSyncerChainHash = nil
				break
			}

		} else { // Empty locator signals a restart due to chain changes
			chainNegotiationZoomCounts = 0
			chainNegotiationRestartCounter++
			if chainNegotiationRestartCounter > 32 {
				return nil, nil, protocolerrors.Errorf(false,
					"IBD chain negotiation with syncer %s exceeded restart limit %d", flow.peer, chainNegotiationRestartCounter)
			}
			log.Warnf("IBD chain negotiation with syncer %s restarted %d times", flow.peer, chainNegotiationRestartCounter)

			// An empty locator signals that the syncer chain was modified and no longer contains one of
			// the queried hashes, so we restart the search. We use a shorter timeout here to avoid a timeout attack
			locatorHashes, err = flow.getSyncerChainBlockLocator(nil, nil, time.Second*10)
			if err != nil {
				return nil, nil, err
			}
			if len(locatorHashes) == 0 {
				return nil, nil, protocolerrors.Errorf(true, "Expecting initial syncer chain block locator "+
					"to contain at least one element")
			}
			log.Infof("IBD chain negotiation with peer %s restarted (%d) and received %d hashes (%s, %s)", flow.peer,
				chainNegotiationRestartCounter, len(locatorHashes), locatorHashes[0], locatorHashes[len(locatorHashes)-1])

			// Reset the max zoom steps based on the new locator size
			maxZoomSteps = len(locatorHashes) * 64
			// Reset syncer's header selected tip
			syncerHeaderSelectedTipHash = locatorHashes[0]
		}
	}

	log.Infof("Found highest known syncer chain block %s from peer %s",
		highestKnownSyncerChainHash, flow.peer)

	return syncerHeaderSelectedTipHash, highestKnownSyncerChainHash, nil
}

func (flow *handleIBDFlow) isGenesisVirtualSelectedParent() (bool, error) {
	virtualSelectedParent, err := flow.Domain().Consensus().GetVirtualSelectedParent()
	if err != nil {
		return false, err
	}

	return virtualSelectedParent.Equal(flow.Config().NetParams().GenesisHash), nil
}

func (flow *handleIBDFlow) logIBDFinished(isFinishedSuccessfully bool, err error) error {
	if !isFinishedSuccessfully {
		return err
	}
	log.Infof("IBD with peer %s finished successfully", flow.peer)
	return nil
}

func (flow *handleIBDFlow) getSyncerChainBlockLocator(
	highHash, lowHash *externalapi.DomainHash, _ time.Duration,
) ([]*externalapi.DomainHash, error) {
	requestIbdChainBlockLocatorMessage := appmessage.NewMsgIBDRequestChainBlockLocator(highHash, lowHash)
	err := flow.outgoingRoute.Enqueue(requestIbdChainBlockLocatorMessage)
	if err != nil {
		return nil, err
	}
	message, err := flow.incomingRoute.DequeueWithTimeout(flow.Config().IBDDequeueTimeout)
	if err != nil {
		return nil, err
	}
	switch message := message.(type) {
	case *appmessage.MsgIBDChainBlockLocator:
		if len(message.BlockLocatorHashes) > 64 {
			return nil, protocolerrors.Errorf(true,
				"Got block locator of size %d>64 while expecting locator to have size "+
					"which is logarithmic in DAG size (which should never exceed 2^64)",
				len(message.BlockLocatorHashes))
		}
		return message.BlockLocatorHashes, nil
	default:
		return nil, protocolerrors.Errorf(true, "received unexpected message type. "+
			"expected: %s, got: %s", appmessage.CmdIBDChainBlockLocator, message.Command())
	}
}

func (flow *handleIBDFlow) syncPruningPointFutureHeaders(
	consensus externalapi.Consensus,
	syncerHeaderSelectedTipHash, highestKnownSyncerChainHash, relayBlockHash *externalapi.DomainHash,
	highBlockDAAScoreHint uint64,
) error {
	log.Infof("Downloading headers from %s", flow.peer)

	if highestKnownSyncerChainHash.Equal(syncerHeaderSelectedTipHash) {
		// No need to get syncer selected tip headers → sync relay past and return
		return flow.syncMissingRelayPast(consensus, syncerHeaderSelectedTipHash, relayBlockHash)
	}

	err := flow.sendRequestHeaders(highestKnownSyncerChainHash, syncerHeaderSelectedTipHash)
	if err != nil {
		return err
	}
	highestSharedBlockHeader, err := consensus.GetBlockHeader(highestKnownSyncerChainHash)
	if err != nil {
		return err
	}
	progressReporter := newIBDProgressReporter(highestSharedBlockHeader.DAAScore(), highBlockDAAScoreHint, "block headers")

	for {
		// Receive next batch of headers (this call blocks)
		blockHeadersMessage, doneIBD, err := flow.receiveHeaders()
		if err != nil {
			return err
		}

		if doneIBD {
			log.Debugf("IBD Done!")
			// IBD of headers is finished → proceed to sync relay past
			return flow.syncMissingRelayPast(consensus, syncerHeaderSelectedTipHash, relayBlockHash)
		}

		if len(blockHeadersMessage.BlockHeaders) == 0 {
			return protocolerrors.Errorf(true, "Received an empty headers message from peer %s", flow.peer)
		}
		log.Infof("Received %d headers", len(blockHeadersMessage.BlockHeaders))

		// Process all headers in this batch
		for _, header := range blockHeadersMessage.BlockHeaders {
			// log.Infof("Processing header %s", header.BlockHash())
			err = flow.processHeader(consensus, header)
			if err != nil {
				return err
			}
			flow.headersProcessedSinceLast++
			// Periodic rate check (e.g., every 10 seconds) inside loop
			if time.Since(flow.lastRateCheckTime) >= 10*time.Second {
				if err := flow.checkPeriodicRate("headers"); err != nil {
					return err
				}
			}
		}

		// Report progress
		lastReceivedHeader := blockHeadersMessage.BlockHeaders[len(blockHeadersMessage.BlockHeaders)-1]
		progressReporter.reportProgress(len(blockHeadersMessage.BlockHeaders), lastReceivedHeader.DAAScore)

		// Ask for the next batch
		if !lastReceivedHeader.BlockHash().Equal(syncerHeaderSelectedTipHash) {
			log.Infof("Requesting more with last received header %s", lastReceivedHeader.BlockHash())
		}
		err = flow.outgoingRoute.Enqueue(appmessage.NewMsgRequestNextHeaders())
		if err != nil {
			return err
		}
	}
}

func (flow *handleIBDFlow) syncMissingRelayPast(consensus externalapi.Consensus, syncerHeaderSelectedTipHash *externalapi.DomainHash, relayBlockHash *externalapi.DomainHash) error {
	// Finished downloading syncer selected tip blocks,
	// check if we already have the triggering relayBlockHash
	// TODO: undo this modification to check if it's still needed
	// if syncerHeaderSelectedTipHash.Equal(relayBlockHash) {
	// 	return nil
	// }
	relayBlockInfo, err := consensus.GetBlockInfo(relayBlockHash)
	if err != nil {
		return err
	}
	if !relayBlockInfo.Exists {
		// Send a special header request for the selected tip anticone. This is expected to
		// be a small set, as it is bounded to the size of virtual's mergeset.

		log.Infof("Request anticone")
		err = flow.sendRequestAnticone(syncerHeaderSelectedTipHash, relayBlockHash)
		if err != nil {
			return err
		}
		anticoneHeadersMessage, anticoneDone, err := flow.receiveHeaders()
		if err != nil {
			return err
		}
		log.Infof("Received headers %d", len(anticoneHeadersMessage.BlockHeaders))
		if anticoneDone {
			return protocolerrors.Errorf(true,
				"Expected one anticone header chunk for past(%s) cap anticone(%s) but got zero",
				relayBlockHash, syncerHeaderSelectedTipHash)
		}
		_, anticoneDone, err = flow.receiveHeaders()
		if err != nil {
			return err
		}
		if !anticoneDone {
			return protocolerrors.Errorf(true,
				"Expected only one anticone header chunk for past(%s) cap anticone(%s)",
				relayBlockHash, syncerHeaderSelectedTipHash)
		}
		for _, header := range anticoneHeadersMessage.BlockHeaders {
			err = flow.processHeader(consensus, header)
			if err != nil {
				return err
			}
		}
	}

	// If the relayBlockHash has still not been received, the peer is misbehaving
	relayBlockInfo, err = consensus.GetBlockInfo(relayBlockHash)
	if err != nil {
		return err
	}
	if !relayBlockInfo.Exists {
		return protocolerrors.Errorf(true, "did not receive relayBlockHash block %s from peer %s during block download", relayBlockHash, flow.peer)
	}
	return nil
}

func (flow *handleIBDFlow) sendRequestAnticone(
	syncerHeaderSelectedTipHash, relayBlockHash *externalapi.DomainHash,
) error {
	msgRequestAnticone := appmessage.NewMsgRequestAnticone(syncerHeaderSelectedTipHash, relayBlockHash)
	return flow.outgoingRoute.Enqueue(msgRequestAnticone)
}

func (flow *handleIBDFlow) sendRequestHeaders(
	highestKnownSyncerChainHash, syncerHeaderSelectedTipHash *externalapi.DomainHash,
) error {
	msgRequestHeaders := appmessage.NewMsgRequstHeaders(highestKnownSyncerChainHash, syncerHeaderSelectedTipHash)
	return flow.outgoingRoute.Enqueue(msgRequestHeaders)
}

func (flow *handleIBDFlow) receiveHeaders() (msgIBDBlock *appmessage.BlockHeadersMessage, doneHeaders bool, err error) {
	message, err := flow.incomingRoute.DequeueWithTimeout(flow.Config().IBDDequeueTimeout)
	if err != nil {
		return nil, false, err
	}
	switch message := message.(type) {
	case *appmessage.BlockHeadersMessage:
		return message, false, nil
	case *appmessage.MsgDoneHeaders:
		return nil, true, nil
	default:
		return nil, false,
			protocolerrors.Errorf(true, "received unexpected message type. "+
				"expected: %s or %s, got: %s",
				appmessage.CmdBlockHeaders,
				appmessage.CmdDoneHeaders,
				message.Command())
	}
}

func (flow *handleIBDFlow) processHeader(consensus externalapi.Consensus, msgBlockHeader *appmessage.MsgBlockHeader) error {
	header := appmessage.BlockHeaderToDomainBlockHeader(msgBlockHeader)
	block := &externalapi.DomainBlock{
		Header:       header,
		Transactions: nil,
		PoWHash:      "",
	}
	blockHash := consensushashing.BlockHash(block)
	blockInfo, err := consensus.GetBlockInfo(blockHash)
	if err != nil {
		return err
	}
	if blockInfo.Exists {
		log.Debugf("Block header %s is already in the DAG. Skipping...", blockHash)
		return nil
	}
	err = consensus.ValidateAndInsertBlock(block, false, true)
	if err != nil {
		if errors.Is(err, ruleerrors.ErrDuplicateBlock) {
			return nil
		}
		log.Errorf("Rejected block header %s from %s during IBD: %+v", blockHash, flow.peer, errors.WithStack(err))
		return err
	}

	return nil
}

func (flow *handleIBDFlow) validatePruningPointFutureHeaderTimestamps() error {
	headerSelectedTipHash, err := flow.Domain().StagingConsensus().GetHeadersSelectedTip()
	if err != nil {
		return err
	}
	headerSelectedTipHeader, err := flow.Domain().StagingConsensus().GetBlockHeader(headerSelectedTipHash)
	if err != nil {
		return err
	}
	headerSelectedTipTimestamp := headerSelectedTipHeader.TimeInMilliseconds()

	currentSelectedTipHash, err := flow.Domain().Consensus().GetHeadersSelectedTip()
	if err != nil {
		return err
	}
	currentSelectedTipHeader, err := flow.Domain().Consensus().GetBlockHeader(currentSelectedTipHash)
	if err != nil {
		return err
	}
	currentSelectedTipTimestamp := currentSelectedTipHeader.TimeInMilliseconds()

	if headerSelectedTipTimestamp < currentSelectedTipTimestamp {
		return protocolerrors.Errorf(false, "the timestamp of the candidate selected "+
			"tip is smaller than the current selected tip")
	}

	minTimestampDifferenceInMilliseconds := (1 * time.Minute).Milliseconds()
	if headerSelectedTipTimestamp-currentSelectedTipTimestamp < minTimestampDifferenceInMilliseconds {
		return protocolerrors.Errorf(false, "difference between the timestamps of "+
			"the current pruning point and the candidate pruning point is too small. Aborting IBD...")
	}
	return nil
}

func (flow *handleIBDFlow) receiveAndInsertPruningPointUTXOSet(
	consensus externalapi.Consensus, pruningPointHash *externalapi.DomainHash,
) (bool, error) {
	onEnd := logger.LogAndMeasureExecutionTime(log, "receiveAndInsertPruningPointUTXOSet")
	defer onEnd()

	receivedChunkCount := 0
	receivedUTXOCount := 0
	// Pre-allocate a buffer to hold the domain pairs.
	// 1000 is the standard chunk size defined in sendPruningPointUTXOSet.
	// Instead of leaving a mess for the GC to tidy up afterwards
	domainPairsBuffer := make([]*externalapi.OutpointAndUTXOEntryPair, 0, 1000)

	for {
		message, err := flow.incomingRoute.DequeueWithTimeout(flow.Config().IBDDequeueTimeout)
		if err != nil {
			return false, err
		}

		switch message := message.(type) {
		case *appmessage.MsgPruningPointUTXOSetChunk:
			receivedUTXOCount += len(message.OutpointAndUTXOEntryPairs)

			// Clear the buffer, but keep the backing array allocation
			domainPairsBuffer = domainPairsBuffer[:0]

			// Use the new helper to populate the buffer
			domainPairsBuffer = appmessage.AppendOutpointAndUTXOEntryPairsToDomainOutpointAndUTXOEntryPairs(
				message.OutpointAndUTXOEntryPairs, domainPairsBuffer)

			err := consensus.AppendImportedPruningPointUTXOs(domainPairsBuffer)
			if err != nil {
				return false, err
			}

			receivedChunkCount++
			if receivedChunkCount%getIBDBatchSize() == 0 {
				log.Infof("Received %d UTXO set chunks so far, totaling in %d UTXOs",
					receivedChunkCount, receivedUTXOCount)

				requestNextPruningPointUTXOSetChunkMessage := appmessage.NewMsgRequestNextPruningPointUTXOSetChunk()
				err := flow.outgoingRoute.Enqueue(requestNextPruningPointUTXOSetChunkMessage)
				if err != nil {
					return false, err
				}
			}

		case *appmessage.MsgDonePruningPointUTXOSetChunks:
			log.Infof("Finished receiving the UTXO set. Total UTXOs: %d", receivedUTXOCount)
			return true, nil

		case *appmessage.MsgUnexpectedPruningPoint:
			log.Infof("Could not receive the next UTXO chunk because the pruning point %s "+
				"is no longer the pruning point of peer %s", pruningPointHash, flow.peer)
			return false, nil

		default:
			return false, protocolerrors.Errorf(true, "received unexpected message type. "+
				"expected: %s or %s or %s, got: %s", appmessage.CmdPruningPointUTXOSetChunk,
				appmessage.CmdDonePruningPointUTXOSetChunks, appmessage.CmdUnexpectedPruningPoint, message.Command(),
			)
		}
	}
}

func (flow *handleIBDFlow) syncMissingBlockBodies(highHash *externalapi.DomainHash) error {
	hashes, err := flow.Domain().Consensus().GetMissingBlockBodyHashes(highHash)
	log.Infof("Found %d missing block bodies to sync.", len(hashes))
	if err != nil {
		return err
	}
	if len(hashes) == 0 {
		log.Debugf("No missing block body hashes found.")
		return nil
	}
	// for _, hash := range hashes {
	// 	log.Infof("Syncing hash %s", hash)
	// }

	lowBlockHeader, err := flow.Domain().Consensus().GetBlockHeader(hashes[0])
	if err != nil {
		return err
	}
	highBlockHeader, err := flow.Domain().Consensus().GetBlockHeader(hashes[len(hashes)-1])
	if err != nil {
		return err
	}
	progressReporter := newIBDProgressReporter(lowBlockHeader.DAAScore(), highBlockHeader.DAAScore(), "blocks")
	highestProcessedDAAScore := lowBlockHeader.DAAScore()
	updateVirtual, err := flow.Domain().Consensus().IsNearlySynced()
	if err != nil {
		return err
	}

	ibdBatchSize := getIBDBatchSize()
	// Allocate the map once with the maximum capacity needed.
	// This prevents the map from having to dynamically grow and wait for the damn GC to arrive
	receivedBlocks := make(map[externalapi.DomainHash]*externalapi.DomainBlock, ibdBatchSize)
	for offset := 0; offset < len(hashes); offset += ibdBatchSize {
		// Re-check if we're nearly synced at the start of each batch to update the updateVirtual flag
		// This allows the node to transition from non-nearly-synced to nearly-synced during IBD

		var hashesToRequest []*externalapi.DomainHash
		if offset+ibdBatchSize < len(hashes) {
			hashesToRequest = hashes[offset : offset+ibdBatchSize]
		} else {
			hashesToRequest = hashes[offset:]
		}

		// Cache to store received blocks for this batch only
		clear(receivedBlocks) // Re-use is better than re-allocation :)

		// Request blocks
		err := flow.outgoingRoute.Enqueue(appmessage.NewMsgRequestIBDBlocks(hashesToRequest))
		if err != nil {
			return err
		}
		// Dequeue all messages for the requested hashes
		receivedCount := 0
		for receivedCount < len(hashesToRequest) {
			message, err := flow.incomingRoute.DequeueWithTimeout(flow.Config().IBDDequeueTimeout)
			if err != nil {
				// Only retry on a genuine timeout. Propagate everything else
				if !errors.Is(err, router.ErrTimeout) {
					return err
				}

				// Find which hashes we still need
				missingHashes := make([]*externalapi.DomainHash, 0, len(hashesToRequest)-receivedCount)
				for _, h := range hashesToRequest {
					if _, exists := receivedBlocks[*h]; !exists {
						missingHashes = append(missingHashes, h)
					}
				}
				if len(missingHashes) == 0 {
					// Should be extremely rare (race), but still surface the timeout.
					return err
				}

				log.Debugf("Timeout waiting for blocks, re-requesting %d missing blocks", len(missingHashes))
				if err := flow.outgoingRoute.Enqueue(appmessage.NewMsgRequestIBDBlocks(missingHashes)); err != nil {
					return err
				}
				continue
			}

			msgIBDBlock, ok := message.(*appmessage.MsgIBDBlock)
			if !ok {
				log.Errorf("Received unexpected message type. expected: %s, got: %s", appmessage.CmdIBDBlock, message.Command())
				return protocolerrors.Errorf(false, "received unexpected message type. "+
					"expected: %s, got: %s", appmessage.CmdIBDBlock, message.Command())
			}

			if msgIBDBlock.MsgBlock == nil {
				log.Errorf("Received nil MsgBlock in MsgIBDBlock at index %d", receivedCount)
				return protocolerrors.Errorf(false, "received nil MsgBlock in MsgIBDBlock at index %d", receivedCount)
			}

			block := appmessage.MsgBlockToDomainBlock(msgIBDBlock.MsgBlock)
			if block == nil {
				log.Errorf("MsgBlockToDomainBlock returned nil at index %d", receivedCount)
				return protocolerrors.Errorf(false, "MsgBlockToDomainBlock returned nil at index %d", receivedCount)
			}

			blockHash := consensushashing.BlockHash(block)
			if blockHash == nil {
				log.Errorf("BlockHash returned nil for block at index %d", receivedCount)
				return protocolerrors.Errorf(false, "BlockHash returned nil for block at index %d", receivedCount)
			}

			// Only count new blocks to avoid incrementing for duplicates
			if _, exists := receivedBlocks[*blockHash]; !exists {
				receivedBlocks[*blockHash] = block
				receivedCount++
				log.Debugf("Received block %s and stored in cache", blockHash)
			}
		}

		sort.Slice(hashesToRequest, func(i, j int) bool {
			return receivedBlocks[*hashesToRequest[i]].Header.DAAScore() < receivedBlocks[*hashesToRequest[j]].Header.DAAScore()
		})

		// Process blocks in the order of expected hashes
		for _, expectedHash := range hashesToRequest {
			block, exists := receivedBlocks[*expectedHash]
			if !exists {
				continue
			}
			err = flow.Domain().Consensus().ValidateAndInsertBlock(block, updateVirtual, false)
			if err != nil {
				if errors.Is(err, ruleerrors.ErrDuplicateBlock) {
					continue
				}
				log.Infof("Rejected block %s from %s during IBD", expectedHash, flow.peer)
				return err
			}
			err = flow.OnNewBlock(block)
			if err != nil {
				return err
			}

			highestProcessedDAAScore = block.Header.DAAScore()
			flow.blocksProcessedSinceLast++
			// Periodic rate check (e.g., every 10 seconds) inside loop
			if time.Since(flow.lastRateCheckTime) >= 10*time.Second {
				if err := flow.checkPeriodicRate("blocks"); err != nil {
					return err
				}
			}
		}

		progressReporter.reportProgress(len(hashesToRequest), highestProcessedDAAScore)
	}

	// err = flow.Domain().Consensus().RepairBlockStatuses()
	// if err != nil {
	// 	log.Warnf("Failed to repair block statuses before resolve: %v", err)
	// }

	log.Infof("Start resolving virtual")
	if !updateVirtual {
		err = flow.resolveVirtual(highestProcessedDAAScore)
		if err != nil {
			return err
		}
	}

	return flow.OnNewBlockTemplate()
}

func (flow *handleIBDFlow) resolveVirtual(estimatedVirtualDAAScoreTarget uint64) error {
	err := flow.Domain().Consensus().ResolveVirtual(func(virtualDAAScoreStart uint64, virtualDAAScore uint64) {
		var percents int
		if estimatedVirtualDAAScoreTarget <= virtualDAAScoreStart {
			percents = 100
		} else {
			percents = int(float64(virtualDAAScore-virtualDAAScoreStart) / float64(estimatedVirtualDAAScoreTarget-virtualDAAScoreStart) * 100)
		}
		if percents < 0 {
			percents = 0
		} else if percents > 100 {
			percents = 100
		}
		log.Infof("Resolving virtual. Estimated progress: %d%%", percents)
	})
	if err != nil {
		if database.IsNotFoundError(err) {
			log.Errorf("Error: Not found: %s", err)
			return err
		}
		wrappedErr := wrapResolveVirtualError(err)
		if protocolErr := (protocolerrors.ProtocolError{}); errors.As(wrappedErr, &protocolErr) {
			log.Warnf("ResolveVirtual failed during IBD from %s: %v", flow.peer, err)
		}
		return wrappedErr
	}

	log.Infof("Resolved virtual")
	return nil
}

// NEW: Helper for periodic rate check
func (flow *handleIBDFlow) checkPeriodicRate(itemType string) error {
	now := time.Now()
	elapsed := now.Sub(flow.lastRateCheckTime).Seconds()
	if elapsed <= 9 {
		return nil // Avoid division by zero and low artificial first count too....
	}

	var rate float64
	var minRate float64
	if itemType == "headers" {
		rate = float64(flow.headersProcessedSinceLast) / elapsed
		minRate = flow.minHeadersPerSecond
	} else {
		rate = float64(flow.blocksProcessedSinceLast) / elapsed
		minRate = flow.minBlocksPerSecond
	}

	// Only for debug purposes
	// log.Infof("IBD processed %.2f %s/sec , low rate count: %d", rate, itemType, flow.consecutiveLowRateCount)
	if rate < minRate {
		flow.consecutiveLowRateCount++
		log.Warnf("IBD processed %.2f %s/sec (below %.2f), low rate count: %d", rate, itemType, minRate, flow.consecutiveLowRateCount)
		if flow.consecutiveLowRateCount >= flow.slowIBDTicks {
			log.Warnf("IBD PEER STUCK -  sent low %s rate for %d ticks, DISCONNECTING", itemType, flow.slowIBDTicks)
			return flow.disconnectPeerDueToLowRate()
		}
	} else {
		flow.consecutiveLowRateCount = 0 // Reset on good rate
	}

	// Reset for next interval
	flow.lastRateCheckTime = now
	flow.headersProcessedSinceLast = 0
	flow.blocksProcessedSinceLast = 0
	return nil
}

// NEW: Helper to disconnect peer (same as before)
func (flow *handleIBDFlow) disconnectPeerDueToLowRate() error {
	netAddress := flow.peer.Connection().NetAddress()
	if err := flow.AddressManager().RemoveAddress(netAddress); err != nil {
		log.Warnf("Failed to remove address %s from address manager: %v", netAddress, err)
	}
	flow.peer.Connection().Disconnect()
	return protocolerrors.Errorf(true, "Peer disconnected due to consistently low IBD rate")
}
