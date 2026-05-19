package ghostdagmanager

import (
	"crypto/md5"
	"math"
	"sort"

	"github.com/Hoosat-Oy/HTND/domain/consensus/model"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/pkg/errors"
)

const paperFaithful = true

// makeUMCVotingKey creates a key for UMCVoting cache
func makeUMCVotingKey(g, u []*externalapi.DomainHash, e int) externalapi.DomainHash {
	h := md5.New()

	sortedG := make([]*externalapi.DomainHash, len(g))
	copy(sortedG, g)
	sort.Slice(sortedG, func(i, j int) bool {
		return sortedG[i].String() < sortedG[j].String()
	})
	for _, gh := range sortedG {
		h.Write(gh.ByteSlice())
	}

	sortedU := make([]*externalapi.DomainHash, len(u))
	copy(sortedU, u)
	sort.Slice(sortedU, func(i, j int) bool {
		return sortedU[i].String() < sortedU[j].String()
	})
	for _, uh := range sortedU {
		h.Write(uh.ByteSlice())
	}

	h.Write([]byte{byte(e >> 24), byte(e >> 16), byte(e >> 8), byte(e)})

	digest := h.Sum(nil)
	var keyBytes [32]byte
	copy(keyBytes[:16], digest)
	copy(keyBytes[16:], digest)
	key, _ := externalapi.NewDomainHashFromByteSlice(keyBytes[:])
	return *key
}

func filterNil(s []*externalapi.DomainHash) []*externalapi.DomainHash {
	valid := make([]*externalapi.DomainHash, 0, len(s))
	for _, x := range s {
		if x != nil {
			valid = append(valid, x)
		}
	}
	return valid
}

func (gm *ghostdagManager) getTipsInG(stagingArea *model.StagingArea, G []*externalapi.DomainHash) []*externalapi.DomainHash {
	gSet := make(map[externalapi.DomainHash]struct{})
	for _, h := range G {
		gSet[*h] = struct{}{}
	}

	var tips []*externalapi.DomainHash
	for _, b := range G {
		children, _ := gm.dagTopologyManager.Children(stagingArea, b)
		hasChildInG := false
		for _, c := range children {
			if _, ok := gSet[*c]; ok {
				hasChildInG = true
				break
			}
		}
		if !hasChildInG {
			tips = append(tips, b)
		}
	}
	return tips
}

// OrderDAG implements Algorithm 2: KNIGHT DAG ordering algorithm from the DAGKnight paper
// This algorithm orders the blocks in a DAG by iteratively selecting the "best" tip based on rank and tie-breaking.
// Input: G - a block DAG represented as a set of block hashes
// Output: The selected tip of G, and a total ordering over all blocks in G
func (gm *ghostdagManager) OrderDAG(stagingArea *model.StagingArea, G []*externalapi.DomainHash) (*externalapi.DomainHash, []*externalapi.DomainHash, error) {
	// Step 1: Filter out any nil blocks from G to ensure validity
	G = filterNil(G)

	// Step 2: Base case - if G is empty (only genesis), return genesis as tip and ordering
	if len(G) == 0 {
		genesis := model.VirtualGenesisBlockHash
		return genesis, []*externalapi.DomainHash{genesis}, nil
	}

	// Step 3: Get the current tips of the DAG from consensus state
	tips := gm.getTipsInG(stagingArea, G)

	// Step 4: For each tip B, recursively compute the ordering of past(B) ∩ G
	// This corresponds to building the chain orders for each tip
	chainParents := make(map[externalapi.DomainHash]*externalapi.DomainHash)
	orders := make(map[externalapi.DomainHash][]*externalapi.DomainHash)

	for _, B := range tips {
		// Compute past(B) ∩ G
		pastB, err := gm.getPast(stagingArea, B, G)
		if err != nil {
			return nil, nil, err
		}
		// Recursive call to order the past
		selectedTip, order, err := gm.OrderDAG(stagingArea, pastB)
		if err != nil {
			return nil, nil, err
		}
		chainParents[*B] = selectedTip
		orders[*B] = order
	}

	// Step 5: Initialize P as the set of all tips
	P := make([]*externalapi.DomainHash, len(tips))
	copy(P, tips)

	// Step 6: While |P| > 1, iteratively reduce P to a single element
	for len(P) > 1 {
		// Step 6a: Find the latest common chain ancestor g of all blocks in P
		g, err := gm.latestCommonChainAncestor(stagingArea, P, G)
		if err != nil {
			return nil, nil, err
		}

		// Step 6b: Partition P into maximal disjoint sets P1, ..., Pn where the LCA of each Pi is in future(g)
		partitions, err := gm.partitionByLCAFuture(stagingArea, P, g, G)
		if err != nil {
			return nil, nil, err
		}

		// Step 6c: For each partition Pi, calculate its rank using CalculateRank(Pi, future(g))
		minRank := -1
		minRankPartitions := make([][]*externalapi.DomainHash, 0)

		futureG, err := gm.getFuture(stagingArea, g, G)
		if err != nil {
			return nil, nil, err
		}

		for _, Pi := range partitions {
			ranki, err := gm.CalculateRank(stagingArea, Pi, futureG)
			if err != nil {
				return nil, nil, err
			}
			// Collect partitions with minimum rank
			if minRank == -1 || ranki < minRank {
				minRank = ranki
				minRankPartitions = [][]*externalapi.DomainHash{Pi}
			} else if ranki == minRank {
				minRankPartitions = append(minRankPartitions, Pi)
			}
		}

		// Step 6d: Among partitions with minimum rank, perform tie-breaking to select one partition
		tieBreakPartitions := make([]*externalapi.DomainHash, 0)
		for _, partition := range minRankPartitions {
			tieBreakPartitions = append(tieBreakPartitions, partition...)
		}

		selectedP, err := gm.TieBreaking(stagingArea, futureG, tieBreakPartitions, minRank)
		if err != nil {
			return nil, nil, err
		}
		// Step 6e: Set P to {selectedP}
		P = []*externalapi.DomainHash{selectedP}
	}

	// Step 7: p is the single remaining element in P
	p := P[0]

	// Step 8: Build the final ordering as order_p ∥ p ∥ anticone(p)
	// where anticone(p) is iterated in hash-based bottom-up topological order
	orderP := orders[*p]
	ordering := make([]*externalapi.DomainHash, 0, len(orderP)+1)
	ordering = append(ordering, orderP...)
	ordering = append(ordering, p)

	anticoneP, err := gm.getAnticone(stagingArea, p, G)
	if err != nil {
		return nil, nil, err
	}

	// Sort anticone in hash-based bottom-up topological order
	// The paper specifies a topological order; we use hash string comparison as a proxy
	sort.Slice(anticoneP, func(i, j int) bool {
		return anticoneP[i].String() < anticoneP[j].String()
	})

	ordering = append(ordering, anticoneP...)

	return p, ordering, nil
}

// latestCommonChainAncestor finds the latest common chain ancestor of all blocks in P
// In DAGKnight, this refers to the deepest block that is a chain ancestor of all blocks in P
func (gm *ghostdagManager) latestCommonChainAncestor(stagingArea *model.StagingArea, P, G []*externalapi.DomainHash) (*externalapi.DomainHash, error) {
	if len(P) == 0 {
		return nil, errors.New("empty set P")
	}
	if len(P) == 1 {
		return P[0], nil
	}

	// Start from the first block and find the chain (selected parent path)
	chain1, err := gm.getChainPath(stagingArea, P[0])
	if err != nil {
		return nil, err
	}

	// Find intersection of all chains
	commonAncestors := chain1
	for _, block := range P[1:] {
		chain, err := gm.getChainPath(stagingArea, block)
		if err != nil {
			return nil, err
		}
		commonAncestors = intersect(commonAncestors, chain)
	}

	if len(commonAncestors) == 0 {
		return model.VirtualGenesisBlockHash, nil
	}

	// Return the "latest" (deepest) common ancestor
	// Assuming the chain is ordered from tip to genesis, the first one is the latest
	return commonAncestors[0], nil
}

// getChainPath returns the chain path from block to genesis (selected parent chain)
func (gm *ghostdagManager) getChainPath(stagingArea *model.StagingArea, block *externalapi.DomainHash) ([]*externalapi.DomainHash, error) {
	path := []*externalapi.DomainHash{block}
	current := block

	for !current.Equal(model.VirtualGenesisBlockHash) {
		gd, err := gm.ghostdagDataStore.Get(gm.databaseContext, stagingArea, current, false)
		if err != nil {
			return nil, err
		}
		current = gd.SelectedParent()
		path = append(path, current)
	}

	return path, nil
}

// partitionByLCAFuture partitions P into maximal disjoint sets where LCA of each set is in future(g)
func (gm *ghostdagManager) partitionByLCAFuture(stagingArea *model.StagingArea, P []*externalapi.DomainHash, g *externalapi.DomainHash, G []*externalapi.DomainHash) ([][]*externalapi.DomainHash, error) {
	futureG, err := gm.getFuture(stagingArea, g, G)
	if err != nil {
		return nil, err
	}

	// We will build maximal groups where every pair agrees on the chain after g
	var partitions [][]*externalapi.DomainHash
	used := make(map[externalapi.DomainHash]bool)

	for _, block := range P {
		if used[*block] {
			continue
		}

		group := []*externalapi.DomainHash{block}
		used[*block] = true

		// Try to add every other unused block that agrees with the whole group
		for _, other := range P {
			if used[*other] {
				continue
			}

			// Check if other agrees with ALL blocks already in the group w.r.t. future(g)
			agreesWithGroup := true
			for _, existing := range group {
				if !gm.agreesOnFuture(stagingArea, existing, other, futureG) {
					agreesWithGroup = false
					break
				}
			}

			if agreesWithGroup {
				group = append(group, other)
				used[*other] = true
			}
		}

		partitions = append(partitions, group)
	}

	return partitions, nil
}

// Helper: checks if two blocks agree after g
func (gm *ghostdagManager) agreesOnFuture(stagingArea *model.StagingArea, A, B *externalapi.DomainHash, futureG []*externalapi.DomainHash) bool {
	// Get latest common chain ancestor
	lca, err := gm.latestCommonChainAncestor(stagingArea, []*externalapi.DomainHash{A, B}, nil)
	if err != nil {
		return false
	}

	// They agree w.r.t. future(g) if their LCA is NOT in future(g)
	// (meaning the disagreement happened before or at g)
	return !contains(futureG, lca)
}

// contains checks if slice contains the element
// contains checks if a slice of DomainHash contains a specific element.
// This is a utility function used throughout the DAGKnight algorithms for set membership tests,
// such as checking if a block is in a particular set (e.g., past, future, anticone).
func contains(slice []*externalapi.DomainHash, element *externalapi.DomainHash) bool {
	for _, item := range slice {
		if item.Equal(element) {
			return true
		}
	}
	return false
}

// difference returns the set difference a - b, i.e., elements in a that are not in b.
// This implements the set difference operation used in Algorithm 2 (OrderDAG) and other algorithms
// for computing relative complements, such as finding blocks not in certain sets.
func difference(a, b []*externalapi.DomainHash) []*externalapi.DomainHash {
	setB := make(map[externalapi.DomainHash]struct{})
	for _, h := range b {
		setB[*h] = struct{}{}
	}
	result := make([]*externalapi.DomainHash, 0, len(a))
	for _, item := range a {
		if _, ok := setB[*item]; !ok {
			result = append(result, item)
		}
	}
	return result
}

// intersect returns the intersection of two slices of DomainHash, i.e., elements common to both.
// This implements the set intersection operation used in Algorithm 2 (OrderDAG) and Algorithm 6 (UMCVoting)
// for finding common elements between sets, such as shared past blocks or voting agreements.
func intersect(a, b []*externalapi.DomainHash) []*externalapi.DomainHash {
	setB := make(map[externalapi.DomainHash]struct{})
	for _, h := range b {
		setB[*h] = struct{}{}
	}
	var res []*externalapi.DomainHash
	for _, h := range a {
		if _, ok := setB[*h]; ok {
			res = append(res, h)
		}
	}
	return res
}

// KColouringResult holds the result of a k-colouring computation in Algorithm 5.
// It contains the blue set (blocks coloured blue) and the chain (selected chain blocks).
type KColouringResult struct {
	Blues []*externalapi.DomainHash
	Chain []*externalapi.DomainHash
}

// CalculateRank implements Algorithm 3: Rank calculation procedure from the DAGKnight paper
// This procedure finds the smallest k such that the set P "wins" against its future in G.
// Input: P - a set of blocks in G, G - a block DAG
// Output: The rank of P in G, which is the smallest k where P has a winning k-colouring
func (gm *ghostdagManager) CalculateRank(stagingArea *model.StagingArea, P, G []*externalapi.DomainHash) (int, error) {
	// Step 1: Filter out any nil blocks from P
	validP := make([]*externalapi.DomainHash, 0, len(P))
	for _, p := range P {
		if p != nil {
			validP = append(validP, p)
		}
	}
	P = validP
	if len(P) == 0 {
		return 0, errors.New("CalculateRank: no valid blocks in P")
	}
	// paper-aligned small constant;
	const maxReps = 5
	// Sample representatives deterministically (paper allows sampling for efficiency)
	// Sort by hash string (lex order) → consistent across runs
	reps := P
	if len(P) > maxReps && paperFaithful {
		sorted := make([]*externalapi.DomainHash, len(P))
		copy(sorted, P)

		sort.Slice(sorted, func(i, j int) bool {
			return sorted[i].String() < sorted[j].String()
		})

		reps = sorted[:maxReps]
	}

	// Step 2: For k = 0, 1, 2, 4, 6, ... until a winning k is found
	currentVote := -1
	votePassed := false

	k := 0
	for {
		// Step 3: For each block r in P
		for _, r := range reps {
			// Step 3a: Compute the k-colouring Ck of past_G(r)
			res, err := gm.KColouring(stagingArea, r, G, k, false, nil)
			if err != nil {
				return 0, err
			}
			Ck := res.Blues

			// Step 3b: Compute future_G(r)
			futureR, err := gm.getFuture(stagingArea, r, G)
			if err != nil {
				return 0, err
			}

			// Step 3c: Compute G \ future_G(r)
			GMinusFutureR := difference(G, futureR)

			var gk int
			if paperFaithful == true {
				// Step 3d: g(k) = |sqrt(k)|
				gk = int(math.Floor(math.Sqrt(float64(k))))
			} else {
				// Step 3d: Assume g(k) = k
				gk = k
			}

			// Step 3e: Run UMC voting on (G \ future_G(r), Ck, g(k))
			vote, err := gm.UMCVoting(stagingArea, GMinusFutureR, Ck, gk)
			if err != nil {
				return 0, err
			}

			// Step 3f: If vote > 0, set currentVote to k
			if vote > 0 {
				currentVote = k
				votePassed = true
				break
			}
		}
		if votePassed {
			break
		}
		// Increment: +1 for k=0,1; +2 for k>=2
		if k < 2 {
			k++
		} else {
			k += 2
		}
	}
	if currentVote >= 4 {
		k := currentVote - 1
		// Step 3 again: For backtracking one block r in P
		for _, r := range P {
			// Step 3a: Compute the k-colouring Ck of past_G(r)
			res, err := gm.KColouring(stagingArea, r, G, k, false, nil)
			if err != nil {
				return 0, err
			}
			Ck := res.Blues

			// Step 3b: Compute future_G(r)
			futureR, err := gm.getFuture(stagingArea, r, G)
			if err != nil {
				return 0, err
			}

			// Step 3c: Compute G \ future_G(r)
			GMinusFutureR := difference(G, futureR)

			var gk int
			if paperFaithful == true {
				// Step 3d: g(k) = |sqrt(k)|
				gk = int(math.Floor(math.Sqrt(float64(k))))
			} else {
				// Step 3d: Assume g(k) = k
				gk = k
			}

			// Step 3e: Run UMC voting on (G \ future_G(r), Ck, g(k))
			vote, err := gm.UMCVoting(stagingArea, GMinusFutureR, Ck, gk)
			if err != nil {
				return 0, err
			}

			// Step 3f: If vote > 0, set currentVote to k
			if vote > 0 {
				currentVote = k
				break
			}
		}
	}
	if currentVote < 0 {
		return 0, errors.New("Vote did not pass for unknown reason.")
	}
	return currentVote, nil
}

// TieBreaking implements Algorithm 4: Rank tie-breaking procedure from the DAGKnight paper
// This procedure breaks ties between tips with the same rank by finding which tip has the "best" relationship
// with the global k-colouring of the DAG.
// Input: G - a block DAG, Ps - list of tips P1, ..., Pm with the same rank k
// Output: The winning tip Pi among Ps
func (gm *ghostdagManager) TieBreaking(stagingArea *model.StagingArea, G []*externalapi.DomainHash, Ps []*externalapi.DomainHash, k int) (*externalapi.DomainHash, error) {
	Ps = filterNil(Ps)
	if len(Ps) == 0 {
		return nil, errors.New("no tips")
	}

	virtual := model.VirtualGenesisBlockHash
	F, _ := gm.KColouring(stagingArea, virtual, G, k, true, nil) // global

	bestIdx := 0
	bestScore := "" // for lex compare

	for i, Pi := range Ps {
		Ci := map[externalapi.DomainHash]struct{}{}
		for kp := k / 2; kp <= k; kp++ {
			// Compute k-colouring with conditioning on Pi
			res, _ := gm.KColouring(stagingArea, virtual, G, kp, false, Pi) // conditioned
			chain := res.Chain
			for _, B := range F.Blues {
				anticoneB, _ := gm.getAnticone(stagingArea, B, G)
				if len(intersect(anticoneB, chain)) >= kp {
					Ci[*B] = struct{}{}
				}
			}
		}

		// max B in Ci by hash
		var maxB *externalapi.DomainHash
		for b := range Ci {
			bb := b
			if maxB == nil || bb.String() > maxB.String() {
				maxB = &bb
			}
		}

		score := maxB.String() + Pi.String() // lex (maxB, Pi)
		if score < bestScore || bestScore == "" {
			bestScore = score
			bestIdx = i
		}
	}
	return Ps[bestIdx], nil
}

// KColouring implements Algorithm 5: k-colouring algorithm from the DAGKnight paper
// This algorithm computes a k-colouring of the past of C and a k-chain within that past.
// Input: C - a block in G, G - a block DAG, k - non-negative integer, freeSearch - boolean flag,
//
//	conditioning - optional block for agrees conditioning
//
// Output: (Blues, Chain) where Blues is the k-colouring of past_G(C), Chain is the k-chain
func (gm *ghostdagManager) KColouring(stagingArea *model.StagingArea, C *externalapi.DomainHash, G []*externalapi.DomainHash, k int, freeSearch bool, conditioning *externalapi.DomainHash) (KColouringResult, error) {
	// Step 1: Compute past_G(C)
	pastC, err := gm.getPast(stagingArea, C, G)
	if err != nil {
		return KColouringResult{}, err
	}
	if len(pastC) == 0 {
		return KColouringResult{Blues: []*externalapi.DomainHash{}, Chain: []*externalapi.DomainHash{}}, nil
	}

	// Step 2: Initialize P as the set of parents of C that satisfy the conditions
	P := make([]*externalapi.DomainHash, 0)
	type parentResult struct {
		blues []*externalapi.DomainHash
		chain []*externalapi.DomainHash
	}
	parentResults := make(map[externalapi.DomainHash]parentResult)

	parents, err := gm.dagTopologyManager.Parents(stagingArea, C)
	if err != nil {
		return KColouringResult{}, err
	}

	for _, B := range parents {
		// Step 2a: Compute past_G(B)
		pastB, err := gm.getPast(stagingArea, B, G)
		if err != nil {
			return KColouringResult{}, err
		}
		// Note: past(B) ∩ G = pastB since pastB ⊆ G

		// Step 2b: Check if B agrees with C (with conditioning)
		agrees, err := gm.agrees(stagingArea, B, C, conditioning)
		if err != nil {
			return KColouringResult{}, err
		}

		// Step 2c: Get rank of C
		rankC, err := gm.rank(stagingArea, C)
		if err != nil {
			return KColouringResult{}, err
		}

		// Step 2d: If B agrees with C, or freeSearch is true, or k > rank(C)
		if agrees || freeSearch || k > rankC {
			nextFreeSearch := freeSearch || !agrees
			res, err := gm.KColouring(stagingArea, B, pastB, k, nextFreeSearch, conditioning)
			if err != nil {
				return KColouringResult{}, err
			}
			parentResults[*B] = parentResult{blues: res.Blues, chain: res.Chain}
			P = append(P, B)
		}
	}

	// Step 3: If P is empty, return empty colouring
	if len(P) == 0 {
		return KColouringResult{Blues: []*externalapi.DomainHash{}, Chain: []*externalapi.DomainHash{}}, nil
	}

	// Step 4: Find Bmax = argmax_{B∈P} |blues_B|, break ties by largest hash
	Bmax := P[0]
	maxBlues := len(parentResults[*Bmax].blues)
	for _, b := range P[1:] {
		if len(parentResults[*b].blues) > maxBlues || (len(parentResults[*b].blues) == maxBlues && b.String() > Bmax.String()) {
			Bmax = b
			maxBlues = len(parentResults[*b].blues)
		}
	}

	// Step 5: Initialize blues_G = blues_{Bmax} ∪ {Bmax}, chain_G = chain_{Bmax} ∪ {Bmax}
	bluesG := append(parentResults[*Bmax].blues, Bmax)
	chainG := append(parentResults[*Bmax].chain, Bmax)

	// Step 6: Compute anticone of Bmax in G
	anticone, err := gm.getAnticone(stagingArea, Bmax, G)
	if err != nil {
		return KColouringResult{}, err
	}

	// Step 7: Sort anticone in topological order (using hash order as proxy)
	sort.Slice(anticone, func(i, j int) bool {
		return anticone[i].String() < anticone[j].String()
	})

	// Step 8: For each B in anticone of Bmax (in order)
	for _, B := range anticone {
		// Compute anticone of B in G
		anticoneB, err := gm.getAnticone(stagingArea, B, G)
		if err != nil {
			return KColouringResult{}, err
		}

		// Check condition: |chain_G ∩ anticone_G(B)| ≤ k
		if len(intersect(chainG, anticoneB)) <= k {
			// Check condition: |blues_G ∩ anticone_G(Bmax)| < k
			anticoneBmax, err := gm.getAnticone(stagingArea, Bmax, G)
			if err != nil {
				return KColouringResult{}, err
			}
			if len(intersect(bluesG, anticoneBmax)) < k {
				// Add B to blues_G
				bluesG = append(bluesG, B)
			}
		}
	}

	// Step 9: Return (blues_G, chain_G)
	return KColouringResult{Blues: bluesG, Chain: chainG}, nil
}

// UMCVoting implements Algorithm 6: UMC cascade voting procedure from the DAGKnight paper
// This recursive voting procedure determines if a set U "wins" against the deficit in G.
// Input: G - a block DAG, U ⊆ G (typically a k-colouring), e - deficit threshold (gk in the paper)
// Output: vote ∈ {-1, 1} where 1 means U wins, -1 means U loses
func (gm *ghostdagManager) UMCVoting(stagingArea *model.StagingArea, G, U []*externalapi.DomainHash, e int) (int, error) {
	key := makeUMCVotingKey(G, U, e)
	if value, ok := gm.umcVotingCache.Get(&key); ok {
		return value, nil
	}
	// Step 1: Initialize vote accumulator v = 0
	v := 0

	// Step 2: For each block b in U
	for _, b := range U {
		// Step 2a: Compute future_G(b)
		futureB, err := gm.getFuture(stagingArea, b, G)
		if err != nil {
			return 0, err
		}

		// Step 2b: Compute U ∩ future_G(b)
		uFuture := intersect(U, futureB)

		// Step 2c: Recursively call UMCVoting on (future_G(b), U ∩ future_G(b), e)
		vote, err := gm.UMCVoting(stagingArea, futureB, uFuture, e)
		if err != nil {
			return 0, err
		}

		// Step 2d: Accumulate the vote
		v += vote
	}

	// Step 3: Compute |G| - |U|
	gMinusU := len(G) - len(U)

	// Step 4: If v - (|G| - |U|) + e >= 0, return 1 (win), else return -1 (lose)
	var result int
	if v-gMinusU+e >= 0 {
		result = 1
	} else {
		result = -1
	}
	gm.umcVotingCache.Add(&key, result)
	return result, nil
}

// getFuture returns all blocks in the future of the given block (all descendants) within G.
// This is computed by BFS traversal using the Children method, filtered to G.
// Used in CalculateRank and UMCVoting to compute future sets.
func (gm *ghostdagManager) getFuture(stagingArea *model.StagingArea, block *externalapi.DomainHash, G []*externalapi.DomainHash) ([]*externalapi.DomainHash, error) {
	// Create a set for fast lookup of G
	gSet := make(map[externalapi.DomainHash]struct{})
	for _, g := range G {
		gSet[*g] = struct{}{}
	}

	visited := make(map[externalapi.DomainHash]struct{})
	queue := []*externalapi.DomainHash{block}
	visited[*block] = struct{}{}
	var future []*externalapi.DomainHash

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if !current.Equal(block) {
			future = append(future, current)
		}

		children, err := gm.dagTopologyManager.Children(stagingArea, current)
		if err != nil {
			return nil, err
		}
		for _, child := range children {
			if _, inG := gSet[*child]; inG {
				if _, ok := visited[*child]; !ok {
					visited[*child] = struct{}{}
					queue = append(queue, child)
				}
			}
		}
	}
	return future, nil
}

// agrees checks if B agrees with C based on selected parent, with optional conditioning
// Used in KColouring to determine parent relationships.
// If conditioning is provided, recursively checks agreement with the conditioning block.
func (gm *ghostdagManager) agrees(stagingArea *model.StagingArea, B, C *externalapi.DomainHash, conditioning *externalapi.DomainHash) (bool, error) {
	if B.Equal(C) {
		return true, nil
	}

	lca, err := gm.latestCommonChainAncestor(stagingArea, []*externalapi.DomainHash{B, C}, nil)
	if err != nil {
		return false, err
	}

	if conditioning != nil {
		// Avoid deep recursion with simple check
		condLCA, _ := gm.latestCommonChainAncestor(stagingArea, []*externalapi.DomainHash{B, conditioning}, nil)
		if !lca.Equal(condLCA) { // stricter chain-descendant check
			return false, nil
		}
	}

	gdB, _ := gm.ghostdagDataStore.Get(gm.databaseContext, stagingArea, B, false)
	gdC, _ := gm.ghostdagDataStore.Get(gm.databaseContext, stagingArea, C, false)

	// Core of Def 3: LCA should be chain-descendant (no split after relevant point)
	return gdB.SelectedParent().Equal(gdC.SelectedParent()) || lca.Equal(gdB.SelectedParent()) || lca.Equal(gdC.SelectedParent()), nil
}

// rank returns the blue score of C as rank
// Used in KColouring to determine if freeSearch should be enabled.
// In DAGKnight, rank is the blue score, which indicates the "strength" of a block.
func (gm *ghostdagManager) rank(stagingArea *model.StagingArea, C *externalapi.DomainHash) (int, error) {
	gd, err := gm.ghostdagDataStore.Get(gm.databaseContext, stagingArea, C, false)
	if err != nil {
		return 0, err
	}
	return int(gd.BlueScore()), nil
}

// getPast returns all ancestors of block that are in G
// Used extensively in DAGKnight algorithms to compute past sets.
func (gm *ghostdagManager) getPast(stagingArea *model.StagingArea, block *externalapi.DomainHash, G []*externalapi.DomainHash) ([]*externalapi.DomainHash, error) {
	// Create a set for G for fast lookup
	gSet := make(map[externalapi.DomainHash]struct{})
	for _, g := range G {
		gSet[*g] = struct{}{}
	}

	visited := make(map[externalapi.DomainHash]struct{})
	queue := []*externalapi.DomainHash{block}
	visited[*block] = struct{}{}
	var past []*externalapi.DomainHash

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if !current.Equal(block) {
			past = append(past, current)
		}

		parents, err := gm.dagTopologyManager.Parents(stagingArea, current)
		if err != nil {
			return nil, err
		}
		for _, parent := range parents {
			if _, ok := visited[*parent]; !ok && contains(G, parent) {
				visited[*parent] = struct{}{}
				queue = append(queue, parent)
			}
		}
	}
	return past, nil
}

// getAnticone returns blocks in G that are in anticone of block
// Used in KColouring and TieBreaking for anticone computations.
func (gm *ghostdagManager) getAnticone(stagingArea *model.StagingArea, block *externalapi.DomainHash, G []*externalapi.DomainHash) ([]*externalapi.DomainHash, error) {
	result, err := gm.dagTraversalManager.AnticoneFromBlocks(stagingArea, G, block, 0)
	if err != nil {
		return nil, err
	}
	return result, nil
}
