package ghostdagmanager

import (
	"sort"

	"github.com/Hoosat-Oy/HTND/domain/consensus/model"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/pkg/errors"
)

// OrderDAG implements Algorithm 2: KNIGHT DAG ordering algorithm from the DAGKnight paper
// This algorithm orders the blocks in a DAG by iteratively selecting the "best" tip based on rank and tie-breaking.
// Input: G - a block DAG represented as a set of block hashes
// Output: The selected tip of G, and a total ordering over all blocks in G
func (gm *ghostdagManager) OrderDAG(stagingArea *model.StagingArea, G []*externalapi.DomainHash) (*externalapi.DomainHash, []*externalapi.DomainHash, error) {
	// Step 1: Filter out any nil blocks from G to ensure validity
	validG := make([]*externalapi.DomainHash, 0, len(G))
	for _, g := range G {
		if g != nil {
			validG = append(validG, g)
		}
	}
	G = validG

	// Step 2: Base case - if G is empty (only genesis), return genesis as tip and ordering
	if len(G) == 0 {
		genesis := model.VirtualGenesisBlockHash
		return genesis, []*externalapi.DomainHash{genesis}, nil
	}

	// Step 3: Get the current tips of the DAG from consensus state
	tips, err := gm.consensusStateStore.Tips(stagingArea, gm.databaseContext)
	if err != nil {
		return nil, nil, err
	}

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

	// Group blocks by their LCA with other blocks
	groups := make(map[externalapi.DomainHash][]*externalapi.DomainHash)
	used := make(map[externalapi.DomainHash]bool)

	for _, block := range P {
		if used[*block] {
			continue
		}

		group := []*externalapi.DomainHash{block}
		used[*block] = true

		// Find other blocks that have the same LCA pattern
		for _, other := range P {
			if used[*other] {
				continue
			}

			// Check if LCA of block and other is in future(g)
			lca, err := gm.latestCommonChainAncestor(stagingArea, []*externalapi.DomainHash{block, other}, G)
			if err != nil {
				continue
			}

			if contains(futureG, lca) {
				group = append(group, other)
				used[*other] = true
			}
		}

		// Use the first block's hash as group key
		groups[*block] = group
	}

	partitions := make([][]*externalapi.DomainHash, 0, len(groups))
	for _, group := range groups {
		partitions = append(partitions, group)
	}

	return partitions, nil
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

	// Step 2: For k = 0, 1, 2, ... until a winning k is found
	currentVote := -1
	votePassed := false
	for k := 0; ; k++ {
		// Step 3: For each block r in P
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

			// Step 3d: Assume g(k) = k (as per paper's assumption)
			gk := k

			// Step 3e: Run UMC voting on (G \ future_G(r), Ck, g(k))
			vote, err := gm.UMCVoting(stagingArea, GMinusFutureR, Ck, gk)
			if err != nil {
				return 0, err
			}

			// Step 3f: If vote > 0, return k as the rank
			if vote > 0 {
				currentVote = vote
				votePassed = true
				break
			}
		}
		// Increment K by 2 every loop if vote does not pass after K has been 2.
		if k >= 2 {
			k++
		}
		// Stop the loop if we found a solution.
		if votePassed == true {
			break
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

			// Step 3d: Assume g(k) = k (as per paper's assumption)
			gk := k

			// Step 3e: Run UMC voting on (G \ future_G(r), Ck, g(k))
			vote, err := gm.UMCVoting(stagingArea, GMinusFutureR, Ck, gk)
			if err != nil {
				return 0, err
			}

			// Step 3f: If vote > 0, return k as the rank
			if vote > 0 {
				currentVote = vote
				votePassed = true
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
	// Step 1: Filter out any nil tips from Ps
	validPs := make([]*externalapi.DomainHash, 0, len(Ps))
	for _, p := range Ps {
		if p != nil {
			validPs = append(validPs, p)
		}
	}
	Ps = validPs
	if len(Ps) == 0 {
		return nil, errors.New("TieBreaking: no valid tips")
	}

	// Step 2: Compute the global k-colouring F of G (with freeSearch=true)
	virtual := model.VirtualGenesisBlockHash
	gk := k // assume g(k) = k
	F, err := gm.KColouring(stagingArea, virtual, G, gk, true, nil)
	if err != nil {
		return nil, err
	}

	// Step 3: For each tip Pi in Ps, compute Ci as the set of blocks B in F.Blues
	// such that there exists kp in [k/2, k] where |anticone_G(B) ∩ chain_kp(virtual, G, Pi)| > kp
	type ciResult struct {
		maxB *externalapi.DomainHash
	}
	results := make([]ciResult, len(Ps))

	for i, Pi := range Ps {
		Ci := make(map[externalapi.DomainHash]struct{})
		for kp := k / 2; kp <= k; kp++ {
			// Compute k-colouring with conditioning on Pi
			res, err := gm.KColouring(stagingArea, virtual, G, kp, false, Pi)
			if err != nil {
				return nil, err
			}
			chain := res.Chain
			for _, B := range F.Blues {
				anticoneB, err := gm.getAnticone(stagingArea, B, G)
				if err != nil {
					return nil, err
				}
				intersection := intersect(anticoneB, chain)
				if len(intersection) > kp {
					Ci[*B] = struct{}{}
				}
			}
		}

		// Step 4: For each Ci, find the maximum B in Ci (by hash order)
		if len(Ci) == 0 {
			results[i] = ciResult{maxB: nil}
		} else {
			var maxB *externalapi.DomainHash
			for b := range Ci {
				bb := b
				if maxB == nil || bb.String() > maxB.String() {
					maxB = &bb
				}
			}
			results[i] = ciResult{maxB: maxB}
		}
	}

	// Step 5: Return the Pi with the lexicographically smallest (maxB, Pi) pair
	minI := 0
	minMaxB := results[0].maxB
	for i := 1; i < len(results); i++ {
		curr := results[i].maxB
		if minMaxB == nil && curr != nil {
			continue
		}
		if curr == nil && minMaxB != nil {
			minI = i
			minMaxB = curr
			continue
		}
		if curr != nil && minMaxB != nil {
			if curr.String() < minMaxB.String() || (curr.String() == minMaxB.String() && Ps[i].String() < Ps[minI].String()) {
				minI = i
				minMaxB = curr
			}
		}
	}

	return Ps[minI], nil
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
		if agrees {
			res, err := gm.KColouring(stagingArea, B, pastB, k, freeSearch, conditioning)
			if err != nil {
				return KColouringResult{}, err
			}
			parentResults[*B] = parentResult{blues: res.Blues, chain: res.Chain}
			P = append(P, B)
		} else if freeSearch || k > rankC {
			res, err := gm.KColouring(stagingArea, B, pastB, k, true, conditioning)
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
		chainGIntersectAnticoneB := intersect(chainG, anticoneB)
		if len(chainGIntersectAnticoneB) <= k {
			// Check condition: |blues_G ∩ anticone_G(Bmax)| < k
			anticoneBmax, err := gm.getAnticone(stagingArea, Bmax, G)
			if err != nil {
				return KColouringResult{}, err
			}
			bluesGIntersectAnticoneBmax := intersect(bluesG, anticoneBmax)
			if len(bluesGIntersectAnticoneBmax) < k {
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
	if v-gMinusU+e >= 0 {
		return 1, nil
	}
	return -1, nil
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
		future = append(future, current)

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
	// Remove the block itself from future, as future typically excludes the block
	if len(future) > 0 && future[0].Equal(block) {
		future = future[1:]
	}
	return future, nil
}

// agrees checks if B agrees with C based on selected parent, with optional conditioning
// Used in KColouring to determine parent relationships.
// If conditioning is provided, recursively checks agreement with the conditioning block.
func (gm *ghostdagManager) agrees(stagingArea *model.StagingArea, B, C *externalapi.DomainHash, conditioning *externalapi.DomainHash) (bool, error) {
	gdB, err := gm.ghostdagDataStore.Get(gm.databaseContext, stagingArea, B, false)
	if err != nil {
		return false, err
	}
	gdC, err := gm.ghostdagDataStore.Get(gm.databaseContext, stagingArea, C, false)
	if err != nil {
		return false, err
	}
	original := gdB.SelectedParent().Equal(gdC.SelectedParent())
	if conditioning != nil {
		virtual := model.VirtualGenesisBlockHash
		cond, err := gm.agrees(stagingArea, virtual, conditioning, nil) // recursive with nil to avoid loop
		if err != nil {
			return false, err
		}
		return original && cond, nil
	}
	return original, nil
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
		past = append(past, current)

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
	// Remove the block itself
	if len(past) > 0 && past[0].Equal(block) {
		past = past[1:]
	}
	return past, nil
}

// getAnticone returns blocks in G that are in anticone of block
// Used in KColouring and TieBreaking for anticone computations.
func (gm *ghostdagManager) getAnticone(stagingArea *model.StagingArea, block *externalapi.DomainHash, G []*externalapi.DomainHash) ([]*externalapi.DomainHash, error) {
	return gm.dagTraversalManager.AnticoneFromBlocks(stagingArea, G, block, 0)
}
