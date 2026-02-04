package dagknight

import (
	"crypto/sha256"
	"math"
	"sort"
	"strconv"

	"github.com/Hoosat-Oy/HTND/util/difficulty"

	"math/big"

	"github.com/Hoosat-Oy/HTND/domain/consensus/database"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/constants"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/lrucache"
	"github.com/pkg/errors"
)

type dagknighthelper struct {
	k                  []externalapi.KType
	dataStore          model.GHOSTDAGDataStore
	dbAccess           model.DBReader
	dagTopologyManager model.DAGTopologyManager
	headerStore        model.BlockHeaderStore
	genesis            *externalapi.DomainHash
	// Per-invocation caches to accelerate traversals
	futureCache     map[string][]*externalapi.DomainHash
	pastCache       map[string][]*externalapi.DomainHash
	parentsCache    map[string][]*externalapi.DomainHash
	childrenCache   map[string][]*externalapi.DomainHash
	allBlocksCached []*externalapi.DomainHash
	blockDataCache  map[string]*externalapi.BlockGHOSTDAGData
	umcVotingCache  map[string]int
	// Additional ephemeral caches for hot paths
	ancestorCache map[string]bool                      // "A|B" -> IsAncestorOf(A,B)
	chainCache    map[string][]*externalapi.DomainHash // block -> selected-parent chain to genesis
	chainSetCache map[string]map[string]bool           // block -> set membership of its chain
	// Paper-faithful context caches
	ctxPastCache      map[string]dagContext                // "ctx|past|B" -> past(B)∩ctx
	ctxTopoOrderCache map[string][]*externalapi.DomainHash // ctx.id -> topo order (genesis..tips)
	ctxTopoIndexCache map[string]map[string]int            // ctx.id -> node string -> topo index
	ctxReachCache     map[string]bool                      // "ctx|A->B" -> reachable within ctx
	rankCache         map[string]int                       // block string -> rank_G(block) (Definition 5-ish)
	rankInProgress    map[string]bool                      // recursion guard

	// Cross-invocation cache for expensive Algorithm-3 rank calculations.
	// Keyed by a hash of (context id, P).
	rankInContextLRU *lrucache.LRUCache[int]
}

// New creates a new instance of this alternative ghostdag impl
func New(
	databaseContext model.DBReader,
	dagTopologyManager model.DAGTopologyManager,
	ghostdagDataStore model.GHOSTDAGDataStore,
	headerStore model.BlockHeaderStore,
	k []externalapi.KType,
	genesisHash *externalapi.DomainHash) model.GHOSTDAGManager {

	return &dagknighthelper{
		dbAccess:           databaseContext,
		dagTopologyManager: dagTopologyManager,
		dataStore:          ghostdagDataStore,
		headerStore:        headerStore,
		k:                  k,
		genesis:            genesisHash,
		rankInContextLRU:   lrucache.New[int](8192, true),
	}
}

func (dk *dagknighthelper) rankInContextCacheKey(g dagContext, p []*externalapi.DomainHash) *externalapi.DomainHash {
	// Note: g.id and hashOfHashes(p) are already stable, order-independent digests.
	pKey := dk.hashOfHashes(p) // raw 32 bytes as string
	h := sha256.New()
	_, _ = h.Write([]byte(g.id))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(pKey))
	sum := h.Sum(nil)
	var arr [externalapi.DomainHashSize]byte
	copy(arr[:], sum)
	return externalapi.NewDomainHashFromByteArray(&arr)
}

// GHOSTDAG implements model.GHOSTDAGManager.
func (dk *dagknighthelper) GHOSTDAG(stagingArea *model.StagingArea, blockHash *externalapi.DomainHash) error {
	return dk.DAGKNIGHT(stagingArea, blockHash)
}

/* --------------------------------------------- */

func (dk *dagknighthelper) DAGKNIGHT(stagingArea *model.StagingArea, blockCandidate *externalapi.DomainHash) error {
	// Reset per-invocation caches
	dk.clearCaches()

	myWork := new(big.Int)
	maxWork := new(big.Int)
	var myScore uint64
	var spScore uint64
	/* find the selectedParent */
	blockParents, err := dk.dagTopologyManager.Parents(stagingArea, blockCandidate)
	if err != nil {
		return err
	}
	parentsSet := newHashSet(blockParents)
	// Paper Algorithm 2: chain-parent(B) is selected tip of past(B).
	// We approximate past(B) with inclusive past of parents.
	selectedParent, lastRank, hadConflict, err := dk.chainParentAndRankViaKNIGHT(stagingArea, blockParents)
	if err != nil {
		return err
	}
	// Safety fallback: selected parent must be among direct parents.
	if selectedParent == nil || !parentsSet.has(selectedParent) {
		selectedParent = nil
		lastRank = 0
	}
	maxWork = new(big.Int)
	spScore = 0
	for _, parent := range blockParents {
		blockData, err := dk.dataStore.Get(dk.dbAccess, stagingArea, parent, false)
		if database.IsNotFoundError(err) {
			log.Infof("GHOSTDAG failed to retrieve with %s\n", parent)
			return err
		}
		if err != nil {
			return err
		}
		blockWork := blockData.BlueWork()
		blockScore := blockData.BlueScore()
		// If Algorithm 2 produced a valid chain-parent, enforce it.
		if selectedParent != nil {
			if parent.Equal(selectedParent) {
				maxWork = blockWork
				spScore = blockScore
			}
			continue
		}
		// Otherwise, fall back to the previous selected-parent rule.
		if blockWork.Cmp(maxWork) == 1 || (blockWork.Cmp(maxWork) == 0 && ismoreHash(parent, selectedParent)) {
			selectedParent = parent
			maxWork = blockWork
			spScore = blockScore
		}
	}
	myWork.Set(maxWork)
	myScore = spScore

	/* Goal: iterate blockCandidate's mergeSet and divide it to : blue, blues, reds. */
	var mergeSetBlues = make([]*externalapi.DomainHash, 0)
	var mergeSetReds = make([]*externalapi.DomainHash, 0)
	var blueSet = make([]*externalapi.DomainHash, 0)
	mergeSetBluesSet := make(hashSet)
	mergeSetRedsSet := make(hashSet)
	blueSetSet := make(hashSet)

	// In KNIGHT, chain-parent is a direct parent; nevertheless, guard against nil.
	if selectedParent != nil {
		mergeSetBlues = append(mergeSetBlues, selectedParent)
		mergeSetBluesSet.add(selectedParent)
	}

	mergeSetArr, err := dk.findMergeSet(stagingArea, blockParents, selectedParent)
	if err != nil {
		return err
	}

	err = dk.sortByBlueWork(stagingArea, mergeSetArr)
	if err != nil {
		return err
	}
	err = dk.findBlueSet(stagingArea, &blueSet, blueSetSet, selectedParent)
	if err != nil {
		return err
	}

	idx := int(constants.GetBlockVersion()) - 1
	if lastRank == 0 {
		dk.k[idx] = externalapi.KType(lastRank + 1)
	} else {
		dk.k[idx] = externalapi.KType(lastRank)
	}
	if !blockCandidate.Equal(model.VirtualBlockHash) && !blockCandidate.Equal(model.VirtualGenesisBlockHash) {
		log.Debugf("paperRank=%d parents=%d hadConflict=%v k=%v", lastRank, len(blockParents), hadConflict, int(dk.k[idx]))
	}

	for _, mergeSetBlock := range mergeSetArr {
		if mergeSetBlock.Equal(selectedParent) {
			if !mergeSetBluesSet.has(selectedParent) {
				mergeSetBlues = append(mergeSetBlues, selectedParent)
				mergeSetBluesSet.add(selectedParent)
				blueSet = append(blueSet, selectedParent)
				blueSetSet.add(selectedParent)
			}
			continue
		}
		err := dk.divideBlueRed(stagingArea, mergeSetBlock, &mergeSetBlues, &mergeSetReds, &blueSet, mergeSetBluesSet, mergeSetRedsSet, blueSetSet, int(dk.k[idx]))
		if err != nil {
			return err
		}
	}
	myScore += uint64(len(mergeSetBlues))

	// We add up all the *work*(not blueWork) that all our blues and selected parent did
	for _, blue := range mergeSetBlues {
		// Virtual genesis has no header; skip adding its work.
		if blue.Equal(model.VirtualGenesisBlockHash) {
			continue
		}
		header, err := dk.headerStore.BlockHeader(dk.dbAccess, stagingArea, blue)
		if err != nil {
			return err
		}
		myWork.Add(myWork, difficulty.CalcWork(header.Bits()))
	}

	e := externalapi.NewBlockGHOSTDAGData(myScore, myWork, selectedParent, mergeSetBlues, mergeSetReds, nil)
	dk.dataStore.Stage(stagingArea, blockCandidate, e, false)
	return nil
}

// OrderDAG implements Algorithm 2: KNIGHT DAG ordering algorithm
func (dk *dagknighthelper) OrderDAG(stagingArea *model.StagingArea, tips []*externalapi.DomainHash) (*externalapi.DomainHash, []*externalapi.DomainHash, error) {
	if len(tips) == 0 {
		return nil, nil, errors.New("no tips")
	}
	if len(tips) == 1 && tips[0].Equal(dk.genesis) {
		return dk.genesis, []*externalapi.DomainHash{dk.genesis}, nil
	}

	// Reconstruct the input DAG context G from its tips (G == past_G(tips(G)) inclusive).
	ctxG, err := dk.contextFromTipsInclusivePast(stagingArea, tips)
	if err != nil {
		return nil, nil, err
	}

	// Recursive calls on past of each tip
	chainParentMap := make(map[*externalapi.DomainHash]*externalapi.DomainHash)
	orderMap := make(map[*externalapi.DomainHash][]*externalapi.DomainHash)
	for _, b := range tips {
		pastTips, err := dk.PastTips(stagingArea, b) // Tips of the past subgraph per DAGKnight
		if err != nil {
			return nil, nil, err
		}
		chainParent, order, err := dk.OrderDAG(stagingArea, pastTips)
		if err != nil {
			return nil, nil, err
		}
		chainParentMap[b] = chainParent
		orderMap[b] = order
	}

	// P = tips
	p := tips

	for len(p) > 1 {
		// g ← latest common chain ancestor of all B ∈ P
		g, err := dk.latestCommonChainAncestor(stagingArea, p)
		if err != nil {
			return nil, nil, err
		}
		// Partition P into maximal disjoint sets P1, …, Pn
		partitions, err := dk.partitionTips(stagingArea, p, g)
		if err != nil {
			return nil, nil, err
		}
		// Calculate ranks in the paper's required conflict context: future_G(g)
		futureG, err := dk.futureWithinInclusive(stagingArea, g, ctxG)
		if err != nil {
			return nil, nil, err
		}
		ranks := make([]int, len(partitions))
		minRank := math.MaxInt32
		for i, pi := range partitions {
			rank, err := dk.calculateRankInContext(stagingArea, pi, futureG)
			if err != nil {
				return nil, nil, err
			}
			ranks[i] = rank
			if rank < minRank {
				minRank = rank
			}
		}
		// Collect Pi with min rank
		minPartitions := make([][]*externalapi.DomainHash, 0)
		for i, rank := range ranks {
			if rank == minRank {
				minPartitions = append(minPartitions, partitions[i])
			}
		}
		// Tie-Breaking in the same conflict context future_G(g)
		p, err = dk.tieBreakingInContext(stagingArea, futureG, minPartitions)
		if err != nil {
			return nil, nil, err
		}
	}

	// p is the single element
	theP := p[0]

	// order = order_p || p || anticone(p) in hash topo order
	orderP := orderMap[theP]
	order := append(orderP, theP)
	anticone, err := dk.AnticoneSortedWithin(stagingArea, theP, ctxG.nodes)
	if err != nil {
		return nil, nil, err
	}
	order = append(order, anticone...)

	return theP, order, nil
}

// calculateRankInContext implements Algorithm 3 using the given DAG context (G).
func (dk *dagknighthelper) calculateRankInContext(stagingArea *model.StagingArea, p []*externalapi.DomainHash, g dagContext) (int, error) {
	if dk.rankInContextLRU != nil {
		key := dk.rankInContextCacheKey(g, p)
		if v, ok := dk.rankInContextLRU.Get(key); ok {
			return v, nil
		}
	}

	const maxK = math.MaxInt32
	tipsG, err := dk.tipsInContext(stagingArea, g)
	if err != nil {
		return 0, err
	}
	reps, err := dk.repsInContext(stagingArea, p, g, tipsG)
	if err != nil {
		return 0, err
	}

	passedK := -1
	for k := 0; k <= maxK; k += 2 {
		for _, r := range reps {
			ck, _, err := dk.KColouringInContext(stagingArea, r, g, k, false)
			if err != nil {
				return 0, err
			}
			if len(ck) == 0 {
				continue
			}
			futureR, err := dk.futureWithinExclusive(stagingArea, r, g)
			if err != nil {
				return 0, err
			}
			gMinusFutureR := dk.differenceContext(g, futureR)
			e := int(math.Floor(math.Sqrt(float64(k))))
			vote, err := dk.umcVotingPaper(stagingArea, gMinusFutureR, ck, e)
			if err != nil {
				return 0, err
			}
			if vote > 0 {
				passedK = k
				break
			}
		}
		if passedK != -1 {
			break
		}
	}
	if passedK == -1 {
		if dk.rankInContextLRU != nil {
			key := dk.rankInContextCacheKey(g, p)
			dk.rankInContextLRU.Add(key, maxK)
		}
		return maxK, nil
	}
	if passedK >= 1 {
		kBefore := passedK - 1
		for _, r := range reps {
			ck, _, err := dk.KColouringInContext(stagingArea, r, g, kBefore, false)
			if err != nil {
				return 0, err
			}
			if len(ck) == 0 {
				continue
			}
			futureR, err := dk.futureWithinExclusive(stagingArea, r, g)
			if err != nil {
				return 0, err
			}
			gMinusFutureR := dk.differenceContext(g, futureR)
			e := int(math.Floor(math.Sqrt(float64(kBefore))))
			vote, err := dk.umcVotingPaper(stagingArea, gMinusFutureR, ck, e)
			if err != nil {
				return 0, err
			}
			if vote > 0 {
				if dk.rankInContextLRU != nil {
					key := dk.rankInContextCacheKey(g, p)
					dk.rankInContextLRU.Add(key, kBefore)
				}
				return kBefore, nil
			}
		}
	}
	if dk.rankInContextLRU != nil {
		key := dk.rankInContextCacheKey(g, p)
		dk.rankInContextLRU.Add(key, passedK)
	}

	return passedK, nil
}

// repsInContext approximates Definition 4 representatives for P ⊂ tips(G).
func (dk *dagknighthelper) repsInContext(stagingArea *model.StagingArea, p []*externalapi.DomainHash, g dagContext, tipsG []*externalapi.DomainHash) ([]*externalapi.DomainHash, error) {
	if len(p) == 0 {
		return []*externalapi.DomainHash{}, nil
	}
	// Inclusive past of a set: union of (x and its ancestors).
	pPast, err := dk.contextFromTipsInclusivePast(stagingArea, p)
	if err != nil {
		return nil, err
	}
	pSet := make(map[string]bool, len(p))
	for _, h := range p {
		if h != nil {
			pSet[h.String()] = true
		}
	}
	otherTips := make([]*externalapi.DomainHash, 0)
	for _, t := range tipsG {
		if t != nil && !pSet[t.String()] {
			otherTips = append(otherTips, t)
		}
	}
	otherPast, err := dk.contextFromTipsInclusivePast(stagingArea, otherTips)
	if err != nil {
		return nil, err
	}
	base := p[0]
	reps := make([]*externalapi.DomainHash, 0)
	for _, x := range pPast.nodes {
		if x == nil {
			continue
		}
		if otherPast.has(x) {
			continue
		}
		if !g.has(x) {
			continue
		}
		agrees, err := dk.agreesInContext(stagingArea, g, x, base)
		if err != nil {
			return nil, err
		}
		if agrees {
			reps = append(reps, x)
		}
	}
	if len(reps) == 0 {
		// Fallback to the tips themselves
		for _, h := range p {
			if g.has(h) {
				reps = append(reps, h)
			}
		}
	}
	return reps, nil
}

// chainParentAndRankViaKNIGHT selects a chain-parent for a new block using Algorithm 2’s while-loop logic.
// It returns the selected parent (a tip in the past subDAG) and the last conflict’s min-rank (Definition 5).
func (dk *dagknighthelper) chainParentAndRankViaKNIGHT(stagingArea *model.StagingArea, blockParents []*externalapi.DomainHash) (*externalapi.DomainHash, int, bool, error) {
	if len(blockParents) == 0 {
		return nil, 0, false, nil
	}
	parents := make([]*externalapi.DomainHash, 0, len(blockParents))
	for _, p := range blockParents {
		if p != nil {
			parents = append(parents, p)
		}
	}
	if len(parents) == 0 {
		return nil, 0, false, nil
	}

	// Fast-path: with a single parent there is no conflict and the chain-parent is that parent.
	// This is by far the common case and should not pay the Algorithm 2 cost.
	if len(parents) == 1 {
		return parents[0], 0, false, nil
	}

	// Fast-path: detect "no real conflict" even with multiple parents.
	// If all parents are on the same selected-parent-chain branch under their common ancestor,
	// then Definition-5 conflict does not apply and we can cheaply choose by the legacy rule.
	common, err := dk.latestCommonChainAncestor(stagingArea, parents)
	if err != nil {
		return nil, 0, false, err
	}
	branchKey := ""
	conflict := false
	for i, p := range parents {
		key := ""
		if common != nil && !p.Equal(common) {
			inChain, err := dk.dagTopologyManager.IsInSelectedParentChainOf(stagingArea, common, p)
			if err != nil {
				return nil, 0, false, err
			}
			if inChain {
				child, err := dk.dagTopologyManager.ChildInSelectedParentChainOf(stagingArea, common, p)
				if err != nil {
					return nil, 0, false, err
				}
				if child != nil {
					key = child.String()
				}
			}
		}
		if i == 0 {
			branchKey = key
			continue
		}
		if key != branchKey {
			conflict = true
			break
		}
	}
	if !conflict {
		sp, err := dk.ChooseSelectedParent(stagingArea, parents...)
		if err != nil {
			return nil, 0, false, err
		}
		return sp, 0, false, nil
	}

	// Conflict path: run Algorithm 2.
	// G := past(blockCandidate) is approximated by inclusive past of parents.
	g, err := dk.contextFromTipsInclusivePast(stagingArea, parents)
	if err != nil {
		return nil, 0, false, err
	}
	P := append([]*externalapi.DomainHash{}, parents...)
	lastMinRank := 0
	hadConflict := false
	for len(P) > 1 {
		hadConflict = true
		conflictPoint, err := dk.latestCommonChainAncestor(stagingArea, P)
		if err != nil {
			return nil, 0, false, err
		}
		partitions, err := dk.partitionTips(stagingArea, P, conflictPoint)
		if err != nil {
			return nil, 0, false, err
		}
		futureG, err := dk.futureWithinInclusive(stagingArea, conflictPoint, g)
		if err != nil {
			return nil, 0, false, err
		}
		minRank := math.MaxInt32
		minPartitions := make([][]*externalapi.DomainHash, 0)
		for _, pi := range partitions {
			rank, err := dk.calculateRankInContext(stagingArea, pi, futureG)
			if err != nil {
				return nil, 0, false, err
			}
			if rank < minRank {
				minRank = rank
				minPartitions = minPartitions[:0]
				minPartitions = append(minPartitions, pi)
			} else if rank == minRank {
				minPartitions = append(minPartitions, pi)
			}
		}
		if minRank == math.MaxInt32 {
			minRank = 0
		}
		lastMinRank = minRank
		// Resolve ties in the conflict context future_G(conflictPoint).
		P, err = dk.tieBreakingInContext(stagingArea, futureG, minPartitions)
		if err != nil {
			return nil, 0, false, err
		}
	}
	return P[0], lastMinRank, hadConflict, nil
}

// tieBreakingInContext implements Algorithm 4 in the provided conflict context G.
func (dk *dagknighthelper) tieBreakingInContext(stagingArea *model.StagingArea, g dagContext, partitions [][]*externalapi.DomainHash) ([]*externalapi.DomainHash, error) {
	if len(partitions) == 0 {
		return nil, errors.New("no partitions")
	}
	// Mutual rank (paper assumes equal for all tied candidates)
	k, err := dk.calculateRankInContext(stagingArea, partitions[0], g)
	if err != nil {
		return nil, err
	}
	gk := int(math.Floor(math.Sqrt(float64(k))))

	virtual, err := dk.virtualInContext(stagingArea, g)
	if err != nil {
		return nil, err
	}
	F, _, err := dk.KColouringInContext(stagingArea, virtual, g, gk, true)
	if err != nil {
		return nil, err
	}

	minMaxC := math.MaxInt32
	bestJ := 0
	for i, pi := range partitions {
		ciSet := make(map[string]bool)
		kStart := k / 2
		for kprime := kStart; kprime <= k; kprime++ {
			_, chainIKprime, err := dk.KColouringConditionedInContext(stagingArea, virtual, g, kprime, false, pi)
			if err != nil {
				return nil, err
			}
			chainIKprimeSet := make(map[string]bool, len(chainIKprime))
			for _, h := range chainIKprime {
				if h != nil {
					chainIKprimeSet[h.String()] = true
				}
			}
			for _, b := range F {
				anticoneB, err := dk.anticoneWithinContext(stagingArea, g, b)
				if err != nil {
					return nil, err
				}
				count := 0
				for _, a := range anticoneB {
					if a != nil && chainIKprimeSet[a.String()] {
						count++
					}
				}
				if count > kprime {
					ciSet[b.String()] = true
				}
			}
		}
		maxC := len(ciSet)
		if maxC < minMaxC || (maxC == minMaxC && ismoreHash(pi[0], partitions[bestJ][0])) {
			minMaxC = maxC
			bestJ = i
		}
	}
	return partitions[bestJ], nil
}

func (dk *dagknighthelper) virtualInContext(stagingArea *model.StagingArea, ctx dagContext) (*externalapi.DomainHash, error) {
	tips, err := dk.tipsInContext(stagingArea, ctx)
	if err != nil {
		return nil, err
	}
	if len(tips) == 0 {
		return dk.genesis, nil
	}
	g, err := dk.latestCommonChainAncestor(stagingArea, tips)
	if err != nil || g == nil {
		return dk.genesis, nil
	}
	return g, nil
}

// KColouringInContext implements Algorithm 5 over an explicit context DAG G.
// This is the paper-faithful version used by Algorithm 3 and Algorithm 4.
func (dk *dagknighthelper) KColouringInContext(stagingArea *model.StagingArea, c *externalapi.DomainHash, g dagContext, k int, freeSearch bool) ([]*externalapi.DomainHash, []*externalapi.DomainHash, error) {
	if c == nil {
		return []*externalapi.DomainHash{}, []*externalapi.DomainHash{}, nil
	}
	// Only consider parents that are in the context.
	parents, err := dk.ParentsCached(stagingArea, c)
	if err != nil {
		return nil, nil, err
	}
	parentsInG := make([]*externalapi.DomainHash, 0, len(parents))
	for _, p := range parents {
		if g.has(p) {
			parentsInG = append(parentsInG, p)
		}
	}
	if len(parentsInG) == 0 {
		// Paper Algorithm 5: if past_G(C)=∅ return ∅,∅.
		return []*externalapi.DomainHash{}, []*externalapi.DomainHash{}, nil
	}

	// Recurse over parents with the paper's agree/disagree rules.
	p := make([]*externalapi.DomainHash, 0)
	blueMap := make(map[string][]*externalapi.DomainHash)
	chainMap := make(map[string][]*externalapi.DomainHash)

	rankC, err := dk.rankOfBlockPaper(stagingArea, c)
	if err != nil {
		return nil, nil, err
	}

	for _, b := range parentsInG {
		agrees, err := dk.agreesInContext(stagingArea, g, b, c)
		if err != nil {
			return nil, nil, err
		}
		if agrees {
			subG, err := dk.pastWithinInclusiveInContext(stagingArea, b, g)
			if err != nil {
				return nil, nil, err
			}
			bluesB, chainB, err := dk.KColouringInContext(stagingArea, b, subG, k, freeSearch)
			if err != nil {
				return nil, nil, err
			}
			p = append(p, b)
			blueMap[b.String()] = bluesB
			chainMap[b.String()] = chainB
			continue
		}
		// Paper Algorithm 5 line 9: include disagreeing parent when free_search OR k > rank_G(C).
		if freeSearch || k > rankC {
			subG, err := dk.pastWithinInclusiveInContext(stagingArea, b, g)
			if err != nil {
				return nil, nil, err
			}
			bluesB, chainB, err := dk.KColouringInContext(stagingArea, b, subG, k, true)
			if err != nil {
				return nil, nil, err
			}
			p = append(p, b)
			blueMap[b.String()] = bluesB
			chainMap[b.String()] = chainB
		}
	}

	// Choose B_max := argmax |blues(B)|, tie-break by hash.
	var bMax *externalapi.DomainHash
	maxLen := -1
	for _, b := range p {
		l := len(blueMap[b.String()])
		if l > maxLen || (l == maxLen && ismoreHash(b, bMax)) {
			maxLen = l
			bMax = b
		}
	}
	if bMax == nil {
		return []*externalapi.DomainHash{}, []*externalapi.DomainHash{}, nil
	}

	bluesG := append(append([]*externalapi.DomainHash{}, blueMap[bMax.String()]...), bMax)
	chainG := append(append([]*externalapi.DomainHash{}, chainMap[bMax.String()]...), bMax)
	bluesSet := make(map[string]bool, len(bluesG))
	chainSet := make(map[string]bool, len(chainG))
	for _, h := range bluesG {
		if h != nil {
			bluesSet[h.String()] = true
		}
	}
	for _, h := range chainG {
		if h != nil {
			chainSet[h.String()] = true
		}
	}

	// Iterate anticone(bMax, G) in hash-based bottom-up topological order.
	anticone, err := dk.anticoneWithinContext(stagingArea, g, bMax)
	if err != nil {
		return nil, nil, err
	}
	ordered, err := dk.orderSubsetBottomUp(stagingArea, g, anticone)
	if err != nil {
		return nil, nil, err
	}

	for _, b := range ordered {
		anticoneB, err := dk.anticoneWithinContext(stagingArea, g, b)
		if err != nil {
			return nil, nil, err
		}
		countChain := 0
		countBlues := 0
		for _, a := range anticoneB {
			if a != nil && chainSet[a.String()] {
				countChain++
			}
			if a != nil && bluesSet[a.String()] {
				countBlues++
			}
		}
		if countChain <= k && countBlues < k {
			bluesG = append(bluesG, b)
			if b != nil {
				bluesSet[b.String()] = true
			}
		}
	}

	return bluesG, chainG, nil
}

// KColouringConditionedInContext is the paper tie-breaking variant: restrict recursion to blocks that agree with a given partition.
func (dk *dagknighthelper) KColouringConditionedInContext(stagingArea *model.StagingArea, c *externalapi.DomainHash, g dagContext, k int, freeSearch bool, conditioned []*externalapi.DomainHash) ([]*externalapi.DomainHash, []*externalapi.DomainHash, error) {
	if len(conditioned) == 0 {
		return dk.KColouringInContext(stagingArea, c, g, k, freeSearch)
	}
	condBase := conditioned[0]
	parents, err := dk.ParentsCached(stagingArea, c)
	if err != nil {
		return nil, nil, err
	}
	parentsInG := make([]*externalapi.DomainHash, 0, len(parents))
	for _, p := range parents {
		if !g.has(p) {
			continue
		}
		agreesCond, err := dk.agreesInContext(stagingArea, g, p, condBase)
		if err != nil {
			return nil, nil, err
		}
		if agreesCond {
			parentsInG = append(parentsInG, p)
		}
	}
	if len(parentsInG) == 0 {
		return []*externalapi.DomainHash{}, []*externalapi.DomainHash{}, nil
	}

	p := make([]*externalapi.DomainHash, 0)
	blueMap := make(map[string][]*externalapi.DomainHash)
	chainMap := make(map[string][]*externalapi.DomainHash)

	rankC, err := dk.rankOfBlockPaper(stagingArea, c)
	if err != nil {
		return nil, nil, err
	}

	for _, b := range parentsInG {
		agrees, err := dk.agreesInContext(stagingArea, g, b, c)
		if err != nil {
			return nil, nil, err
		}
		if agrees {
			subG, err := dk.pastWithinInclusiveInContext(stagingArea, b, g)
			if err != nil {
				return nil, nil, err
			}
			bluesB, chainB, err := dk.KColouringConditionedInContext(stagingArea, b, subG, k, freeSearch, conditioned)
			if err != nil {
				return nil, nil, err
			}
			p = append(p, b)
			blueMap[b.String()] = bluesB
			chainMap[b.String()] = chainB
			continue
		}
		if freeSearch || k > rankC {
			subG, err := dk.pastWithinInclusiveInContext(stagingArea, b, g)
			if err != nil {
				return nil, nil, err
			}
			bluesB, chainB, err := dk.KColouringConditionedInContext(stagingArea, b, subG, k, true, conditioned)
			if err != nil {
				return nil, nil, err
			}
			p = append(p, b)
			blueMap[b.String()] = bluesB
			chainMap[b.String()] = chainB
		}
	}

	var bMax *externalapi.DomainHash
	maxLen := -1
	for _, b := range p {
		l := len(blueMap[b.String()])
		if l > maxLen || (l == maxLen && ismoreHash(b, bMax)) {
			maxLen = l
			bMax = b
		}
	}
	if bMax == nil {
		return []*externalapi.DomainHash{}, []*externalapi.DomainHash{}, nil
	}

	bluesG := append(append([]*externalapi.DomainHash{}, blueMap[bMax.String()]...), bMax)
	chainG := append(append([]*externalapi.DomainHash{}, chainMap[bMax.String()]...), bMax)
	bluesSet := make(map[string]bool, len(bluesG))
	chainSet := make(map[string]bool, len(chainG))
	for _, h := range bluesG {
		if h != nil {
			bluesSet[h.String()] = true
		}
	}
	for _, h := range chainG {
		if h != nil {
			chainSet[h.String()] = true
		}
	}
	anticone, err := dk.anticoneWithinContext(stagingArea, g, bMax)
	if err != nil {
		return nil, nil, err
	}
	ordered, err := dk.orderSubsetBottomUp(stagingArea, g, anticone)
	if err != nil {
		return nil, nil, err
	}
	for _, b := range ordered {
		anticoneB, err := dk.anticoneWithinContext(stagingArea, g, b)
		if err != nil {
			return nil, nil, err
		}
		countChain := 0
		countBlues := 0
		for _, a := range anticoneB {
			if a != nil && chainSet[a.String()] {
				countChain++
			}
			if a != nil && bluesSet[a.String()] {
				countBlues++
			}
		}
		if countChain <= k && countBlues < k {
			bluesG = append(bluesG, b)
			if b != nil {
				bluesSet[b.String()] = true
			}
		}
	}
	return bluesG, chainG, nil
}

func (dk *dagknighthelper) Agrees(stagingArea *model.StagingArea, b *externalapi.DomainHash, c *externalapi.DomainHash) (bool, error) {
	// Agreement relative to future(g): two blocks agree if they lie on the same branch
	// from their latest common chain ancestor.
	g, err := dk.latestCommonChainAncestor(stagingArea, []*externalapi.DomainHash{b, c})
	if err != nil {
		return false, err
	}
	// Child in selected-parent chain from g towards each block
	var childB *externalapi.DomainHash
	var childC *externalapi.DomainHash
	// Guard: if g == b or g == c, skip child lookup (strict ancestor required)
	if g != nil && !g.Equal(b) {
		inChainB, err := dk.dagTopologyManager.IsInSelectedParentChainOf(stagingArea, g, b)
		if err != nil {
			return false, err
		}
		if inChainB {
			childB, err = dk.dagTopologyManager.ChildInSelectedParentChainOf(stagingArea, g, b)
			if err != nil {
				return false, err
			}
		}
	}
	if g != nil && !g.Equal(c) {
		inChainC, err := dk.dagTopologyManager.IsInSelectedParentChainOf(stagingArea, g, c)
		if err != nil {
			return false, err
		}
		if inChainC {
			childC, err = dk.dagTopologyManager.ChildInSelectedParentChainOf(stagingArea, g, c)
			if err != nil {
				return false, err
			}
		}
	}
	// Both on the same branch (including the case g == b or g == c)
	if childB == nil && childC == nil {
		return true, nil
	}
	if childB == nil || childC == nil {
		// One is exactly g's chain start, the other continues; treat as agreeing.
		return true, nil
	}
	return childB.Equal(childC), nil
}

// AnticoneWithin computes anticone of b limited to a provided context set.
func (dk *dagknighthelper) AnticoneWithin(stagingArea *model.StagingArea, b *externalapi.DomainHash, context []*externalapi.DomainHash) ([]*externalapi.DomainHash, error) {
	anticone := make([]*externalapi.DomainHash, 0)
	for _, h := range context {
		if h.Equal(b) {
			continue
		}
		isAnti, err := dk.isAnticone(stagingArea, h, b)
		if err != nil {
			return nil, err
		}
		if isAnti {
			anticone = append(anticone, h)
		}
	}
	return anticone, nil
}

// AnticoneSortedWithin computes and sorts anticone within a context set.
func (dk *dagknighthelper) AnticoneSortedWithin(stagingArea *model.StagingArea, b *externalapi.DomainHash, context []*externalapi.DomainHash) ([]*externalapi.DomainHash, error) {
	anticone, err := dk.AnticoneWithin(stagingArea, b, context)
	if err != nil {
		return nil, err
	}
	err = dk.sortByHash(anticone)
	return anticone, err
}

func (dk *dagknighthelper) Past(stagingArea *model.StagingArea, b *externalapi.DomainHash) ([]*externalapi.DomainHash, error) {
	// Cached BFS backward from b using Parents (exclusive past: does NOT include b itself)
	key := b.String()
	if dk.pastCache != nil {
		if cached, ok := dk.pastCache[key]; ok {
			return cached, nil
		}
	}
	visited := make(map[string]bool)
	queue, err := dk.ParentsCached(stagingArea, b)
	if err != nil {
		return nil, err
	}
	past := make([]*externalapi.DomainHash, 0)
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if visited[current.String()] {
			continue
		}
		visited[current.String()] = true
		past = append(past, current)
		parents, err := dk.ParentsCached(stagingArea, current)
		if err != nil {
			return nil, err
		}
		queue = append(queue, parents...)
	}
	if dk.pastCache == nil {
		dk.pastCache = make(map[string][]*externalapi.DomainHash)
	}
	dk.pastCache[key] = past
	return past, nil
}

func (dk *dagknighthelper) Future(stagingArea *model.StagingArea, b *externalapi.DomainHash) ([]*externalapi.DomainHash, error) {
	// Cached BFS forward using Children, excluding the start node b itself
	key := b.String()
	if dk.futureCache != nil {
		if cached, ok := dk.futureCache[key]; ok {
			return cached, nil
		}
	}
	visited := make(map[string]bool)
	queue := []*externalapi.DomainHash{b}
	future := make([]*externalapi.DomainHash, 0)
	start := b
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if visited[current.String()] {
			continue
		}
		visited[current.String()] = true
		if !current.Equal(start) {
			future = append(future, current)
		}
		children, err := dk.ChildrenCached(stagingArea, current)
		if err != nil {
			return nil, err
		}
		queue = append(queue, children...)
	}
	if dk.futureCache == nil {
		dk.futureCache = make(map[string][]*externalapi.DomainHash)
	}
	dk.futureCache[key] = future
	return future, nil
}

func (dk *dagknighthelper) AllBlocks(stagingArea *model.StagingArea) ([]*externalapi.DomainHash, error) {
	// Enumerate all reachable blocks from genesis via Children traversal with caching.
	if dk.allBlocksCached != nil {
		return dk.allBlocksCached, nil
	}
	visited := make(map[string]bool)
	queue := []*externalapi.DomainHash{dk.genesis}
	all := make([]*externalapi.DomainHash, 0)
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if current == nil {
			continue
		}
		if visited[current.String()] {
			continue
		}
		visited[current.String()] = true
		all = append(all, current)
		children, err := dk.ChildrenCached(stagingArea, current)
		if err != nil {
			return nil, err
		}
		queue = append(queue, children...)
	}
	dk.allBlocksCached = all
	return all, nil
}

// selectedParentOf returns the selected parent of a block, or nil if none.
func (dk *dagknighthelper) selectedParentOf(stagingArea *model.StagingArea, block *externalapi.DomainHash) (*externalapi.DomainHash, error) {
	if block == nil {
		return nil, nil
	}
	data, err := dk.BlockDataCached(stagingArea, block)
	if err != nil {
		return nil, err
	}
	return data.SelectedParent(), nil
}

func (dk *dagknighthelper) latestCommonChainAncestor(stagingArea *model.StagingArea, p []*externalapi.DomainHash) (*externalapi.DomainHash, error) {
	if len(p) == 0 {
		return dk.genesis, nil
	}
	// Build selected-parent chains once, then find closest common ancestor by membership
	chain0, err := dk.ChainToGenesis(stagingArea, p[0])
	if err != nil {
		return nil, err
	}
	otherSets := make([]map[string]bool, 0, len(p)-1)
	for i := 1; i < len(p); i++ {
		s, err := dk.ChainSetToGenesis(stagingArea, p[i])
		if err != nil {
			return nil, err
		}
		otherSets = append(otherSets, s)
	}
	for _, candidate := range chain0 { // from tip towards genesis
		inAll := true
		key := candidate.String()
		for _, s := range otherSets {
			if !s[key] {
				inAll = false
				break
			}
		}
		if inAll {
			return candidate, nil
		}
	}
	return dk.genesis, nil
}

func (dk *dagknighthelper) partitionTips(stagingArea *model.StagingArea, p []*externalapi.DomainHash, g *externalapi.DomainHash) ([][]*externalapi.DomainHash, error) {
	// Partition tips by their first child on the selected-parent chain from g
	// towards the tip (i.e., branch under g). Implemented via local chain traversal.
	byChild := make(map[string][]*externalapi.DomainHash)
	for _, tip := range p {
		if g != nil && tip.Equal(g) {
			byChild[tip.String()] = append(byChild[tip.String()], tip)
			continue
		}
		chain, err := dk.ChainToGenesis(stagingArea, tip)
		if err != nil {
			return nil, err
		}
		// Find position of g in tip's chain
		pos := -1
		for i, h := range chain {
			if h.Equal(g) {
				pos = i
				break
			}
		}
		if pos == -1 {
			// Not in chain under g, group by tip itself
			byChild[tip.String()] = append(byChild[tip.String()], tip)
			continue
		}
		var child *externalapi.DomainHash
		if pos > 0 {
			child = chain[pos-1]
		}
		key := tip.String()
		if child != nil {
			key = child.String()
		}
		byChild[key] = append(byChild[key], tip)
	}
	partitions := make([][]*externalapi.DomainHash, 0, len(byChild))
	for _, group := range byChild {
		partitions = append(partitions, group)
	}
	return partitions, nil
}

// PastTips returns the tips of the past subgraph of b (nodes in Past(b) with no children inside Past(b)).
func (dk *dagknighthelper) PastTips(stagingArea *model.StagingArea, b *externalapi.DomainHash) ([]*externalapi.DomainHash, error) {
	past, err := dk.Past(stagingArea, b)
	if err != nil {
		return nil, err
	}
	set := make(map[string]bool, len(past))
	for _, h := range past {
		set[h.String()] = true
	}
	tips := make([]*externalapi.DomainHash, 0)
	for _, h := range past {
		children, err := dk.ChildrenCached(stagingArea, h)
		if err != nil {
			return nil, err
		}
		inSetChild := false
		for _, c := range children {
			if set[c.String()] {
				inSetChild = true
				break
			}
		}
		if !inSetChild {
			tips = append(tips, h)
		}
	}
	return tips, nil
}

type dagContext struct {
	nodes []*externalapi.DomainHash
	set   map[externalapi.DomainHash]struct{}
	root  *externalapi.DomainHash
	id    string
}

func (ctx dagContext) has(h *externalapi.DomainHash) bool {
	if h == nil {
		return false
	}
	_, ok := ctx.set[*h]
	return ok
}

func newDAGContext(nodes []*externalapi.DomainHash, root *externalapi.DomainHash) dagContext {
	set := make(map[externalapi.DomainHash]struct{}, len(nodes))
	uniq := make([]*externalapi.DomainHash, 0, len(nodes))
	for _, h := range nodes {
		if h == nil {
			continue
		}
		if _, ok := set[*h]; ok {
			continue
		}
		set[*h] = struct{}{}
		uniq = append(uniq, h)
	}

	// Stable id for memoization:
	// - Avoid building huge joined strings.
	// - Avoid hex encoding; we keep the raw 32-byte sha256 digest as a string.
	// - Sort by hash bytes for deterministic order.
	ordered := make([]*externalapi.DomainHash, len(uniq))
	copy(ordered, uniq)
	sort.Slice(ordered, func(i, j int) bool {
		return ordered[i].Less(ordered[j])
	})
	h := sha256.New()
	if root != nil {
		_, _ = h.Write(root.ByteSlice())
		_, _ = h.Write([]byte{0})
	}
	for _, b := range ordered {
		_, _ = h.Write(b.ByteSlice())
		_, _ = h.Write([]byte{0})
	}
	id := string(h.Sum(nil))
	return dagContext{nodes: uniq, set: set, root: root, id: id}
}

func (dk *dagknighthelper) contextFromTipsInclusivePast(stagingArea *model.StagingArea, tips []*externalapi.DomainHash) (dagContext, error) {
	visited := make(map[externalapi.DomainHash]struct{}, len(tips))
	queue := make([]*externalapi.DomainHash, 0, len(tips))
	for _, t := range tips {
		if t == nil {
			continue
		}
		if _, ok := visited[*t]; ok {
			continue
		}
		visited[*t] = struct{}{}
		queue = append(queue, t)
	}
	nodes := make([]*externalapi.DomainHash, 0)
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur == nil {
			continue
		}
		nodes = append(nodes, cur)
		parents, err := dk.ParentsCached(stagingArea, cur)
		if err != nil {
			return dagContext{}, err
		}
		for _, p := range parents {
			if p == nil {
				continue
			}
			if _, ok := visited[*p]; ok {
				continue
			}
			visited[*p] = struct{}{}
			queue = append(queue, p)
		}
	}
	return newDAGContext(nodes, dk.genesis), nil
}

func (dk *dagknighthelper) tipsInContext(stagingArea *model.StagingArea, ctx dagContext) ([]*externalapi.DomainHash, error) {
	tips := make([]*externalapi.DomainHash, 0)
	for _, h := range ctx.nodes {
		children, err := dk.ChildrenCached(stagingArea, h)
		if err != nil {
			return nil, err
		}
		inCtxChild := false
		for _, c := range children {
			if ctx.has(c) {
				inCtxChild = true
				break
			}
		}
		if !inCtxChild {
			tips = append(tips, h)
		}
	}
	return tips, nil
}

func (dk *dagknighthelper) futureWithinInclusive(stagingArea *model.StagingArea, start *externalapi.DomainHash, ctx dagContext) (dagContext, error) {
	if start == nil {
		return dagContext{}, nil
	}
	visited := make(map[externalapi.DomainHash]struct{})
	queue := []*externalapi.DomainHash{start}
	nodes := make([]*externalapi.DomainHash, 0)
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur == nil {
			continue
		}
		if _, ok := visited[*cur]; ok {
			continue
		}
		visited[*cur] = struct{}{}
		if ctx.has(cur) {
			nodes = append(nodes, cur)
		}
		children, err := dk.ChildrenCached(stagingArea, cur)
		if err != nil {
			return dagContext{}, err
		}
		for _, c := range children {
			if c == nil {
				continue
			}
			if !ctx.has(c) {
				continue
			}
			queue = append(queue, c)
		}
	}
	return newDAGContext(nodes, start), nil
}

func (dk *dagknighthelper) futureWithinExclusive(stagingArea *model.StagingArea, start *externalapi.DomainHash, ctx dagContext) (dagContext, error) {
	inc, err := dk.futureWithinInclusive(stagingArea, start, ctx)
	if err != nil {
		return dagContext{}, err
	}
	if start == nil {
		return inc, nil
	}
	nodes := make([]*externalapi.DomainHash, 0, len(inc.nodes))
	for _, h := range inc.nodes {
		if h.Equal(start) {
			continue
		}
		nodes = append(nodes, h)
	}
	return newDAGContext(nodes, inc.root), nil
}

func (dk *dagknighthelper) differenceContext(a dagContext, exclude dagContext) dagContext {
	set := make(map[externalapi.DomainHash]struct{}, len(a.nodes))
	out := make([]*externalapi.DomainHash, 0, len(a.nodes))
	for _, h := range a.nodes {
		if h == nil {
			continue
		}
		if exclude.has(h) {
			continue
		}
		if _, ok := set[*h]; ok {
			continue
		}
		set[*h] = struct{}{}
		out = append(out, h)
	}
	return newDAGContext(out, a.root)
}

func (dk *dagknighthelper) hashOfHashes(blocks []*externalapi.DomainHash) string {
	ordered := make([]*externalapi.DomainHash, 0, len(blocks))
	for _, b := range blocks {
		if b != nil {
			ordered = append(ordered, b)
		}
	}
	sort.Slice(ordered, func(i, j int) bool {
		return ordered[i].Less(ordered[j])
	})
	h := sha256.New()
	for _, b := range ordered {
		_, _ = h.Write(b.ByteSlice())
		_, _ = h.Write([]byte{0})
	}
	return string(h.Sum(nil))
}

func (dk *dagknighthelper) hashOfContext(ctx dagContext) string {
	return ctx.id
}

// umcVotingPaper implements Algorithm 6 exactly: recursive calls shrink G to future(B).
func (dk *dagknighthelper) umcVotingPaper(stagingArea *model.StagingArea, g dagContext, u []*externalapi.DomainHash, e int) (int, error) {
	gKey := dk.hashOfContext(g)
	uKey := dk.hashOfHashes(u)
	key := "g:" + gKey + "|u:" + uKey + "|e:" + strconv.Itoa(e)
	if dk.umcVotingCache != nil {
		if v, ok := dk.umcVotingCache[key]; ok {
			return v, nil
		}
	}

	uSet := make(map[externalapi.DomainHash]struct{}, len(u))
	for _, h := range u {
		if h != nil {
			uSet[*h] = struct{}{}
		}
	}

	v := 0
	for _, b := range u {
		if b == nil {
			continue
		}
		futureB, err := dk.futureWithinExclusive(stagingArea, b, g)
		if err != nil {
			return 0, err
		}
		uFuture := make([]*externalapi.DomainHash, 0)
		for _, h := range futureB.nodes {
			if h != nil {
				if _, ok := uSet[*h]; ok {
					uFuture = append(uFuture, h)
				}
			}
		}
		vote, err := dk.umcVotingPaper(stagingArea, futureB, uFuture, e)
		if err != nil {
			return 0, err
		}
		v += vote
	}

	// sign(v - |G\U| + e)
	uInG := 0
	for k := range uSet {
		kk := k
		if g.has(&kk) {
			uInG++
		}
	}
	deficit := len(g.nodes) - uInG
	res := -1
	if v-deficit+e >= 0 {
		res = 1
	}
	if dk.umcVotingCache == nil {
		dk.umcVotingCache = make(map[string]int)
	}
	dk.umcVotingCache[key] = res
	return res, nil
}

// agreesInContext implements the paper's agreement notion relative to genesis(G).
// In the KNIGHT paper, for the conflict-context future_G(g), genesis(G)=g.
func (dk *dagknighthelper) agreesInContext(stagingArea *model.StagingArea, g dagContext, a, b *externalapi.DomainHash) (bool, error) {
	if g.root == nil {
		return dk.Agrees(stagingArea, a, b)
	}
	if a == nil || b == nil {
		return false, nil
	}
	if a.Equal(g.root) || b.Equal(g.root) {
		return true, nil
	}
	// Agreement in context: the first selected-parent-chain successor after genesis(G) must match.
	aInChain, err := dk.dagTopologyManager.IsInSelectedParentChainOf(stagingArea, g.root, a)
	if err != nil {
		return false, err
	}
	bInChain, err := dk.dagTopologyManager.IsInSelectedParentChainOf(stagingArea, g.root, b)
	if err != nil {
		return false, err
	}
	if !aInChain || !bInChain {
		return false, nil
	}
	aChild, err := dk.dagTopologyManager.ChildInSelectedParentChainOf(stagingArea, g.root, a)
	if err != nil {
		return false, err
	}
	bChild, err := dk.dagTopologyManager.ChildInSelectedParentChainOf(stagingArea, g.root, b)
	if err != nil {
		return false, err
	}
	if aChild == nil || bChild == nil {
		return false, nil
	}
	return aChild.Equal(bChild), nil
}

func (dk *dagknighthelper) pastWithinInclusiveInContext(stagingArea *model.StagingArea, start *externalapi.DomainHash, ctx dagContext) (dagContext, error) {
	if start == nil {
		return dagContext{}, nil
	}
	if dk.ctxPastCache != nil {
		key := ctx.id + "|past|" + start.String()
		if cached, ok := dk.ctxPastCache[key]; ok {
			return cached, nil
		}
	}
	visited := make(map[externalapi.DomainHash]struct{})
	queue := []*externalapi.DomainHash{start}
	nodes := make([]*externalapi.DomainHash, 0)
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur == nil {
			continue
		}
		if _, ok := visited[*cur]; ok {
			continue
		}
		visited[*cur] = struct{}{}
		if ctx.has(cur) {
			nodes = append(nodes, cur)
		}
		parents, err := dk.ParentsCached(stagingArea, cur)
		if err != nil {
			return dagContext{}, err
		}
		for _, p := range parents {
			if p == nil {
				continue
			}
			if !ctx.has(p) {
				continue
			}
			queue = append(queue, p)
		}
	}
	res := newDAGContext(nodes, ctx.root)
	if dk.ctxPastCache == nil {
		dk.ctxPastCache = make(map[string]dagContext)
	}
	key := ctx.id + "|past|" + start.String()
	dk.ctxPastCache[key] = res
	return res, nil
}

func (dk *dagknighthelper) isReachableWithinContext(stagingArea *model.StagingArea, ctx dagContext, from, to *externalapi.DomainHash) (bool, error) {
	if from == nil || to == nil {
		return false, nil
	}
	if from.Equal(to) {
		return true, nil
	}
	// Outside the context => unreachable within the induced sub-DAG.
	if !ctx.has(from) || !ctx.has(to) {
		return false, nil
	}
	key := ctx.id + "|" + from.String() + "->" + to.String()
	if dk.ctxReachCache != nil {
		if v, ok := dk.ctxReachCache[key]; ok {
			return v, nil
		}
	}
	// For the contexts constructed by this implementation, reachability queries are the hot-path.
	// Use the indexed ancestor relation instead of per-call BFS.
	found, err := dk.isAncestorOfCached(stagingArea, from, to)
	if err != nil {
		return false, err
	}
	if dk.ctxReachCache == nil {
		dk.ctxReachCache = make(map[string]bool)
	}
	dk.ctxReachCache[key] = found
	return found, nil
}

func (dk *dagknighthelper) anticoneWithinContext(stagingArea *model.StagingArea, ctx dagContext, x *externalapi.DomainHash) ([]*externalapi.DomainHash, error) {
	if x == nil {
		return []*externalapi.DomainHash{}, nil
	}
	res := make([]*externalapi.DomainHash, 0)
	for _, y := range ctx.nodes {
		if y == nil || y.Equal(x) {
			continue
		}
		reachXY, err := dk.isReachableWithinContext(stagingArea, ctx, x, y)
		if err != nil {
			return nil, err
		}
		if reachXY {
			continue
		}
		reachYX, err := dk.isReachableWithinContext(stagingArea, ctx, y, x)
		if err != nil {
			return nil, err
		}
		if reachYX {
			continue
		}
		res = append(res, y)
	}
	return res, nil
}

func (dk *dagknighthelper) topoOrderInContext(stagingArea *model.StagingArea, ctx dagContext) ([]*externalapi.DomainHash, map[string]int, error) {
	if dk.ctxTopoOrderCache != nil {
		if cached, ok := dk.ctxTopoOrderCache[ctx.id]; ok {
			idx := dk.ctxTopoIndexCache[ctx.id]
			return cached, idx, nil
		}
	}
	inDegree := make(map[string]int, len(ctx.nodes))
	childrenMap := make(map[string][]*externalapi.DomainHash, len(ctx.nodes))
	for _, n := range ctx.nodes {
		if n == nil {
			continue
		}
		inDegree[n.String()] = 0
		childrenMap[n.String()] = nil
	}
	for _, n := range ctx.nodes {
		if n == nil {
			continue
		}
		parents, err := dk.ParentsCached(stagingArea, n)
		if err != nil {
			return nil, nil, err
		}
		for _, p := range parents {
			if p == nil || !ctx.has(p) {
				continue
			}
			inDegree[n.String()]++
			childrenMap[p.String()] = append(childrenMap[p.String()], n)
		}
	}
	queue := make([]*externalapi.DomainHash, 0)
	for _, n := range ctx.nodes {
		if n == nil {
			continue
		}
		if inDegree[n.String()] == 0 {
			queue = append(queue, n)
		}
	}
	sort.Slice(queue, func(i, j int) bool { return ismoreHash(queue[i], queue[j]) })
	order := make([]*externalapi.DomainHash, 0, len(ctx.nodes))
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		order = append(order, cur)
		for _, ch := range childrenMap[cur.String()] {
			k := ch.String()
			inDegree[k]--
			if inDegree[k] == 0 {
				queue = append(queue, ch)
			}
		}
		sort.Slice(queue, func(i, j int) bool { return ismoreHash(queue[i], queue[j]) })
	}
	idx := make(map[string]int, len(order))
	for i, n := range order {
		idx[n.String()] = i
	}
	if dk.ctxTopoOrderCache == nil {
		dk.ctxTopoOrderCache = make(map[string][]*externalapi.DomainHash)
	}
	if dk.ctxTopoIndexCache == nil {
		dk.ctxTopoIndexCache = make(map[string]map[string]int)
	}
	dk.ctxTopoOrderCache[ctx.id] = order
	dk.ctxTopoIndexCache[ctx.id] = idx
	return order, idx, nil
}

func (dk *dagknighthelper) orderSubsetBottomUp(stagingArea *model.StagingArea, ctx dagContext, subset []*externalapi.DomainHash) ([]*externalapi.DomainHash, error) {
	_, idx, err := dk.topoOrderInContext(stagingArea, ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*externalapi.DomainHash, 0, len(subset))
	for _, h := range subset {
		if ctx.has(h) {
			out = append(out, h)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		ai := idx[out[i].String()]
		aj := idx[out[j].String()]
		if ai != aj {
			return ai > aj // bottom-up: tips first
		}
		return ismoreHash(out[i], out[j])
	})
	return out, nil
}

// rankOfBlockPaper computes rank_G(C) used by Algorithm 5 line 9.
// This is Definition 5-ish: the min-rank of the last conflict while selecting chain-parent in past(C).
func (dk *dagknighthelper) rankOfBlockPaper(stagingArea *model.StagingArea, c *externalapi.DomainHash) (int, error) {
	if c == nil {
		return 0, nil
	}
	key := c.String()
	if dk.rankCache != nil {
		if v, ok := dk.rankCache[key]; ok {
			return v, nil
		}
	}
	if dk.rankInProgress != nil {
		if dk.rankInProgress[key] {
			// Recursion guard: conservative default.
			return 0, nil
		}
	}
	if dk.rankInProgress == nil {
		dk.rankInProgress = make(map[string]bool)
	}
	dk.rankInProgress[key] = true
	defer func() { dk.rankInProgress[key] = false }()

	parents, err := dk.ParentsCached(stagingArea, c)
	if err != nil {
		return 0, err
	}
	_, lastRank, hadConflict, err := dk.chainParentAndRankViaKNIGHT(stagingArea, parents)
	if err != nil {
		return 0, err
	}
	if !hadConflict {
		lastRank = 0
	}
	if dk.rankCache == nil {
		dk.rankCache = make(map[string]int)
	}
	dk.rankCache[key] = lastRank
	return lastRank, nil
}

/* Existing functions below... */

func ismoreHash(parent *externalapi.DomainHash, selectedParent *externalapi.DomainHash) bool {
	if selectedParent == nil {
		return true
	}
	parentByteArray := parent.ByteArray()
	selectedParentByteArray := selectedParent.ByteArray()
	for i := 0; i < len(parentByteArray); i++ {
		switch {
		case parentByteArray[i] < selectedParentByteArray[i]:
			return false
		case parentByteArray[i] > selectedParentByteArray[i]:
			return true
		}
	}
	return false
}

func (dk *dagknighthelper) divideBlueRed(stagingArea *model.StagingArea, desiredBlock *externalapi.DomainHash,
	blues *[]*externalapi.DomainHash, reds *[]*externalapi.DomainHash, blueSet *[]*externalapi.DomainHash,
	bluesSet hashSet, redsSet hashSet, blueSetSet hashSet, k int) error {
	counter := 0

	var suspectsBlues = make([]*externalapi.DomainHash, 0)
	isMergeBlue := true
	for _, block := range *blueSet {
		isAnticone, err := dk.isAnticone(stagingArea, block, desiredBlock)
		if err != nil {
			return err
		}
		if isAnticone {
			counter++
			suspectsBlues = append(suspectsBlues, block)
		}
		if counter > k {
			isMergeBlue = false
			break
		}
	}
	if !isMergeBlue {
		if !redsSet.has(desiredBlock) {
			*reds = append(*reds, desiredBlock)
			redsSet.add(desiredBlock)
		}
		return nil
	}

	for _, blue := range suspectsBlues {
		isDestroyed, err := dk.checkIfDestroy(stagingArea, blue, blueSet, k)
		if err != nil {
			return err
		}
		if isDestroyed {
			isMergeBlue = false
			break
		}
	}
	if !isMergeBlue {
		if !redsSet.has(desiredBlock) {
			*reds = append(*reds, desiredBlock)
			redsSet.add(desiredBlock)
		}
		return nil
	}
	if !bluesSet.has(desiredBlock) {
		*blues = append(*blues, desiredBlock)
		bluesSet.add(desiredBlock)
	}
	if !blueSetSet.has(desiredBlock) {
		*blueSet = append(*blueSet, desiredBlock)
		blueSetSet.add(desiredBlock)
	}
	return nil
}

func (dk *dagknighthelper) isAnticone(stagingArea *model.StagingArea, blockA, blockB *externalapi.DomainHash) (bool, error) {
	isAAncestorOfB, err := dk.isAncestorOfCached(stagingArea, blockA, blockB)
	if err != nil {
		return false, err
	}
	if isAAncestorOfB {
		return false, nil
	}

	isBAncestorOfA, err := dk.isAncestorOfCached(stagingArea, blockB, blockA)
	if err != nil {
		return false, err
	}
	return !isBAncestorOfA, nil
}

func (dk *dagknighthelper) checkIfDestroy(stagingArea *model.StagingArea, blockBlue *externalapi.DomainHash,
	blueSet *[]*externalapi.DomainHash, k int) (bool, error) {
	counter := 0
	for _, blue := range *blueSet {
		isAnticone, err := dk.isAnticone(stagingArea, blue, blockBlue)
		if err != nil {
			return true, err
		}
		if isAnticone {
			counter++
		}
		if counter > k {
			return true, nil
		}
	}
	return false, nil
}

func (dk *dagknighthelper) findMergeSet(stagingArea *model.StagingArea, parents []*externalapi.DomainHash,
	selectedParent *externalapi.DomainHash) ([]*externalapi.DomainHash, error) {

	allMergeSet := make([]*externalapi.DomainHash, 0)
	allMergeSetSet := make(hashSet)
	blockQueue := make([]*externalapi.DomainHash, 0)
	queueSet := make(hashSet)
	for _, parent := range parents {
		if !queueSet.has(parent) {
			blockQueue = append(blockQueue, parent)
			queueSet.add(parent)
		}

	}
	for len(blockQueue) > 0 {
		block := blockQueue[0]
		blockQueue = blockQueue[1:]
		queueSet.remove(block)
		if selectedParent.Equal(block) {
			if !allMergeSetSet.has(block) {
				allMergeSet = append(allMergeSet, block)
				allMergeSetSet.add(block)
			}
			continue
		}
		isancestorOf, err := dk.dagTopologyManager.IsAncestorOf(stagingArea, block, selectedParent)
		if err != nil {
			return nil, err
		}
		if isancestorOf {
			continue
		}
		if !allMergeSetSet.has(block) {
			allMergeSet = append(allMergeSet, block)
			allMergeSetSet.add(block)
		}
		err = dk.insertParent(stagingArea, block, &blockQueue, queueSet)
		if err != nil {
			return nil, err
		}

	}
	return allMergeSet, nil
}

func (dk *dagknighthelper) insertParent(stagingArea *model.StagingArea, child *externalapi.DomainHash,
	queue *[]*externalapi.DomainHash, queueSet hashSet) error {

	parents, err := dk.dagTopologyManager.Parents(stagingArea, child)
	if err != nil {
		return err
	}
	for _, parent := range parents {
		if queueSet.has(parent) {
			continue
		}
		*queue = append(*queue, parent)
		queueSet.add(parent)
	}
	return nil
}

func (dk *dagknighthelper) findBlueSet(stagingArea *model.StagingArea, blueSet *[]*externalapi.DomainHash, blueSetSet hashSet, selectedParent *externalapi.DomainHash) error {
	for selectedParent != nil {
		if !blueSetSet.has(selectedParent) {
			*blueSet = append(*blueSet, selectedParent)
			blueSetSet.add(selectedParent)
		}
		blockData, err := dk.BlockDataCached(stagingArea, selectedParent)
		if database.IsNotFoundError(err) {
			log.Infof("findBlueSet failed to retrieve with %s\n", selectedParent)
			return err
		}
		if err != nil {
			return err
		}
		mergeSetBlue := blockData.MergeSetBlues()
		for _, blue := range mergeSetBlue {
			if blueSetSet.has(blue) {
				continue
			}
			*blueSet = append(*blueSet, blue)
			blueSetSet.add(blue)
		}
		selectedParent = blockData.SelectedParent()
	}
	return nil
}

type hashSet map[externalapi.DomainHash]struct{}

func newHashSet(items []*externalapi.DomainHash) hashSet {
	set := make(hashSet, len(items))
	for _, h := range items {
		set.add(h)
	}
	return set
}

func (s hashSet) has(h *externalapi.DomainHash) bool {
	if h == nil {
		return false
	}
	_, ok := s[*h]
	return ok
}

func (s hashSet) add(h *externalapi.DomainHash) {
	if h == nil {
		return
	}
	s[*h] = struct{}{}
}

func (s hashSet) remove(h *externalapi.DomainHash) {
	if h == nil {
		return
	}
	delete(s, *h)
}

func (dk *dagknighthelper) sortByBlueWork(stagingArea *model.StagingArea, arr []*externalapi.DomainHash) error {
	if len(arr) <= 1 {
		return nil
	}
	works := make(map[string]*big.Int, len(arr))
	for _, h := range arr {
		data, e := dk.BlockDataCached(stagingArea, h)
		if database.IsNotFoundError(e) {
			log.Infof("sortByBlueWork failed to retrieve with %s\n", h)
			return e
		}
		if e != nil {
			return e
		}
		works[h.String()] = data.BlueWork()
	}
	sort.Slice(arr, func(i, j int) bool {
		wi := works[arr[i].String()]
		wj := works[arr[j].String()]
		cmp := wi.Cmp(wj)
		if cmp > 0 {
			return true
		}
		if cmp == 0 {
			return ismoreHash(arr[i], arr[j])
		}
		return false
	})
	return nil
}

// sortByHash sorts hashes in ascending lexicographic order (hash-topo order).
func (dk *dagknighthelper) sortByHash(arr []*externalapi.DomainHash) error {
	if len(arr) <= 1 {
		return nil
	}
	sort.Slice(arr, func(i, j int) bool {
		ai := arr[i].ByteArray()
		aj := arr[j].ByteArray()
		for k := 0; k < len(ai) && k < len(aj); k++ {
			if ai[k] < aj[k] {
				return true
			}
			if ai[k] > aj[k] {
				return false
			}
		}
		return len(ai) < len(aj)
	})
	return nil
}

// dynamicK removed: rank is computed via CalculateRank per DAGKnight.

func (dk *dagknighthelper) BlockData(stagingArea *model.StagingArea, blockHash *externalapi.DomainHash) (*externalapi.BlockGHOSTDAGData, error) {
	return dk.BlockDataCached(stagingArea, blockHash)
}

func (dk *dagknighthelper) ChooseSelectedParent(stagingArea *model.StagingArea, blockHashes ...*externalapi.DomainHash) (*externalapi.DomainHash, error) {
	if len(blockHashes) == 0 {
		return nil, nil
	}
	var best *externalapi.DomainHash
	var bestData *externalapi.BlockGHOSTDAGData
	for _, h := range blockHashes {
		data, err := dk.BlockData(stagingArea, h)
		if err != nil {
			return nil, err
		}
		if best == nil || dk.Less(best, bestData, h, data) {
			best = h
			bestData = data
		}
	}
	return best, nil
}

func (dk *dagknighthelper) Less(blockHashA *externalapi.DomainHash, ghostdagDataA *externalapi.BlockGHOSTDAGData, blockHashB *externalapi.DomainHash, ghostdagDataB *externalapi.BlockGHOSTDAGData) bool {
	if ghostdagDataA.BlueScore() != ghostdagDataB.BlueScore() {
		return ghostdagDataA.BlueScore() < ghostdagDataB.BlueScore()
	}

	blueWorkCmp := ghostdagDataA.BlueWork().Cmp(ghostdagDataB.BlueWork())
	if blueWorkCmp != 0 {
		return blueWorkCmp < 0
	}

	return !ismoreHash(blockHashA, blockHashB)
}

func (dk *dagknighthelper) GetSortedMergeSet(stagingArea *model.StagingArea, blockHash *externalapi.DomainHash) ([]*externalapi.DomainHash, error) {
	ghostdagData, err := dk.BlockData(stagingArea, blockHash)
	if err != nil {
		return nil, err
	}

	mergeSet := append([]*externalapi.DomainHash{}, ghostdagData.MergeSetBlues()...)
	mergeSet = append(mergeSet, ghostdagData.MergeSetReds()...)

	err = dk.sortByBlueWork(stagingArea, mergeSet)
	if err != nil {
		return nil, err
	}

	return mergeSet, nil
}

/* -------------------- Caching Helpers -------------------- */

// clearCaches resets per-invocation caches to avoid cross-call memory growth and stale data.
func (dk *dagknighthelper) clearCaches() {
	dk.futureCache = nil
	dk.pastCache = nil
	dk.parentsCache = nil
	dk.childrenCache = nil
	dk.allBlocksCached = nil
	dk.blockDataCache = nil
	dk.umcVotingCache = nil
	dk.ancestorCache = nil
	dk.chainCache = nil
	dk.chainSetCache = nil
	dk.ctxPastCache = nil
	dk.ctxTopoOrderCache = nil
	dk.ctxTopoIndexCache = nil
	dk.ctxReachCache = nil
	dk.rankCache = nil
	dk.rankInProgress = nil
}

// ParentsCached returns parents of a block, with per-invocation caching.
func (dk *dagknighthelper) ParentsCached(stagingArea *model.StagingArea, b *externalapi.DomainHash) ([]*externalapi.DomainHash, error) {
	if b == nil {
		return nil, nil
	}
	key := b.String()
	if dk.parentsCache != nil {
		if cached, ok := dk.parentsCache[key]; ok {
			return cached, nil
		}
	}
	parents, err := dk.dagTopologyManager.Parents(stagingArea, b)
	if err != nil {
		return nil, err
	}
	if dk.parentsCache == nil {
		dk.parentsCache = make(map[string][]*externalapi.DomainHash)
	}
	dk.parentsCache[key] = parents
	return parents, nil
}

// ChildrenCached returns children of a block, with per-invocation caching.
func (dk *dagknighthelper) ChildrenCached(stagingArea *model.StagingArea, b *externalapi.DomainHash) ([]*externalapi.DomainHash, error) {
	if b == nil {
		return nil, nil
	}
	key := b.String()
	if dk.childrenCache != nil {
		if cached, ok := dk.childrenCache[key]; ok {
			return cached, nil
		}
	}
	children, err := dk.dagTopologyManager.Children(stagingArea, b)
	if err != nil {
		return nil, err
	}
	if dk.childrenCache == nil {
		dk.childrenCache = make(map[string][]*externalapi.DomainHash)
	}
	dk.childrenCache[key] = children
	return children, nil
}

// BlockDataCached returns GHOSTDAG data for a block, with per-invocation caching.
func (dk *dagknighthelper) BlockDataCached(stagingArea *model.StagingArea, blockHash *externalapi.DomainHash) (*externalapi.BlockGHOSTDAGData, error) {
	if blockHash == nil {
		return nil, errors.New("nil block hash")
	}
	key := blockHash.String()
	if dk.blockDataCache != nil {
		if cached, ok := dk.blockDataCache[key]; ok {
			return cached, nil
		}
	}
	data, err := dk.dataStore.Get(dk.dbAccess, stagingArea, blockHash, false)
	if err != nil {
		return nil, err
	}
	if dk.blockDataCache == nil {
		dk.blockDataCache = make(map[string]*externalapi.BlockGHOSTDAGData)
	}
	dk.blockDataCache[key] = data
	return data, nil
}

/* -------------------- Additional Hot-Path Helpers -------------------- */

// isAncestorOfCached wraps IsAncestorOf with per-invocation memoization.
func (dk *dagknighthelper) isAncestorOfCached(stagingArea *model.StagingArea, a, b *externalapi.DomainHash) (bool, error) {
	if a == nil || b == nil {
		return false, nil
	}
	key := a.String() + "|" + b.String()
	if dk.ancestorCache != nil {
		if val, ok := dk.ancestorCache[key]; ok {
			return val, nil
		}
	}
	ok, err := dk.dagTopologyManager.IsAncestorOf(stagingArea, a, b)
	if err != nil {
		return false, err
	}
	if dk.ancestorCache == nil {
		dk.ancestorCache = make(map[string]bool)
	}
	dk.ancestorCache[key] = ok
	return ok, nil
}

// ChainToGenesis returns the selected-parent chain from the given block down to genesis.
func (dk *dagknighthelper) ChainToGenesis(stagingArea *model.StagingArea, b *externalapi.DomainHash) ([]*externalapi.DomainHash, error) {
	if b == nil {
		return nil, nil
	}
	key := b.String()
	if dk.chainCache != nil {
		if cached, ok := dk.chainCache[key]; ok {
			return cached, nil
		}
	}
	chain := make([]*externalapi.DomainHash, 0, 64)
	current := b
	for current != nil {
		chain = append(chain, current)
		sp, err := dk.selectedParentOf(stagingArea, current)
		if err != nil {
			return nil, err
		}
		current = sp
	}
	if dk.chainCache == nil {
		dk.chainCache = make(map[string][]*externalapi.DomainHash)
	}
	dk.chainCache[key] = chain
	return chain, nil
}

// ChainSetToGenesis returns a set for quick membership tests along the chain to genesis.
func (dk *dagknighthelper) ChainSetToGenesis(stagingArea *model.StagingArea, b *externalapi.DomainHash) (map[string]bool, error) {
	if b == nil {
		return map[string]bool{}, nil
	}
	key := b.String()
	if dk.chainSetCache != nil {
		if cached, ok := dk.chainSetCache[key]; ok {
			return cached, nil
		}
	}
	chain, err := dk.ChainToGenesis(stagingArea, b)
	if err != nil {
		return nil, err
	}
	set := make(map[string]bool, len(chain))
	for _, h := range chain {
		set[h.String()] = true
	}
	if dk.chainSetCache == nil {
		dk.chainSetCache = make(map[string]map[string]bool)
	}
	dk.chainSetCache[key] = set
	return set, nil
}
