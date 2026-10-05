package ghostdagmanager

// This file is a verbatim copy of the DAGKnight procedures as they were at aaa2f15b8, before the
// complexity fix, with only their names prefixed "legacy". The equivalence tests run them side by
// side with the current implementation. Do not "fix" anything in here: the point is to pin the
// current implementation to exactly what these produce.

import (
	"sort"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/pkg/errors"
)

func (gm *ghostdagManager) legacyOrderDAG(stagingArea *model.StagingArea, G []*externalapi.DomainHash) (*externalapi.DomainHash, []*externalapi.DomainHash, error) {
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
		pastB, err := gm.legacygetPast(stagingArea, B, G)
		if err != nil {
			return nil, nil, err
		}
		// Recursive call to order the past
		selectedTip, order, err := gm.legacyOrderDAG(stagingArea, pastB)
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
		g, err := gm.legacylatestCommonChainAncestor(stagingArea, P, G)
		if err != nil {
			return nil, nil, err
		}

		// Step 6b: Partition P into maximal disjoint sets P1, ..., Pn where the LCA of each Pi is in future(g)
		partitions, err := gm.legacypartitionByLCAFuture(stagingArea, P, g, G)
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
			ranki, err := gm.legacyCalculateRank(stagingArea, Pi, futureG)
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

		selectedP, err := gm.legacyTieBreaking(stagingArea, futureG, tieBreakPartitions, minRank)
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
		return anticoneP[i].Less(anticoneP[j])
	})

	ordering = append(ordering, anticoneP...)

	return p, ordering, nil
}

func (gm *ghostdagManager) legacylatestCommonChainAncestor(stagingArea *model.StagingArea, P, G []*externalapi.DomainHash) (*externalapi.DomainHash, error) {
	if len(P) == 0 {
		return nil, errors.New("empty set P")
	}
	if len(P) == 1 {
		return P[0], nil
	}

	// Start from the first block and find the chain (selected parent path)
	chain1, err := gm.legacygetChainPath(stagingArea, P[0])
	if err != nil {
		return nil, err
	}

	// Find intersection of all chains
	commonAncestors := chain1
	for _, block := range P[1:] {
		chain, err := gm.legacygetChainPath(stagingArea, block)
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

func (gm *ghostdagManager) legacygetChainPath(stagingArea *model.StagingArea, block *externalapi.DomainHash) ([]*externalapi.DomainHash, error) {
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

func (gm *ghostdagManager) legacypartitionByLCAFuture(stagingArea *model.StagingArea, P []*externalapi.DomainHash, g *externalapi.DomainHash, G []*externalapi.DomainHash) ([][]*externalapi.DomainHash, error) {
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
				if !gm.legacyagreesOnFuture(stagingArea, existing, other, futureG) {
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

func (gm *ghostdagManager) legacyagreesOnFuture(stagingArea *model.StagingArea, A, B *externalapi.DomainHash, futureG []*externalapi.DomainHash) bool {
	// Get latest common chain ancestor
	lca, err := gm.legacylatestCommonChainAncestor(stagingArea, []*externalapi.DomainHash{A, B}, nil)
	if err != nil {
		return false
	}

	// They agree w.r.t. future(g) if their LCA is NOT in future(g)
	// (meaning the disagreement happened before or at g)
	return !contains(futureG, lca)
}

func (gm *ghostdagManager) legacyCalculateRank(stagingArea *model.StagingArea, P, G []*externalapi.DomainHash) (int, error) {
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
	// Sample representatives deterministically (paper allows sampling for efficiency)
	// Sort by hash string (lex order) → consistent across runs
	reps := make([]*externalapi.DomainHash, len(P))
	copy(reps, P)

	sort.Slice(reps, func(i, j int) bool {
		return reps[i].Less(reps[j])
	})

	// Step 2: For k = 0, 1, 2, 4, 6, ... until a winning k is found
	currentVote := -1
	votePassed := false

	k := 0
	for {
		// Step 3: For each block r in P
		for _, r := range reps {
			// Step 3a: Compute the k-colouring Ck of past_G(r)
			res, err := gm.legacyKColouring(stagingArea, r, G, k, false, nil)
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

			// Step 3d: g(k) = k
			var gk int = k

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
			res, err := gm.legacyKColouring(stagingArea, r, G, k, false, nil)
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

			// Step 3d: g(k) = k
			var gk int = k

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

func (gm *ghostdagManager) legacyTieBreaking(stagingArea *model.StagingArea, G []*externalapi.DomainHash, Ps []*externalapi.DomainHash, k int) (*externalapi.DomainHash, error) {
	Ps = filterNil(Ps)
	if len(Ps) == 0 {
		return nil, errors.New("no tips")
	}
	if len(Ps) == 1 {
		return Ps[0], nil // trivial case
	}

	virtual := model.VirtualGenesisBlockHash
	// Global k-colouring (ignore error for now – we handle empty below)
	F, _ := gm.legacyKColouring(stagingArea, virtual, G, k, true, nil)

	bestIdx := 0
	bestScore := "" // lexicographically smallest wins

	for i, Pi := range Ps {
		Ci := make(map[externalapi.DomainHash]struct{})

		for kp := k / 2; kp <= k; kp++ {
			res, _ := gm.legacyKColouring(stagingArea, virtual, G, kp, false, Pi)
			chain := res.Chain

			for _, B := range F.Blues {
				anticoneB, _ := gm.getAnticone(stagingArea, B, G)
				if len(intersect(anticoneB, chain)) >= kp {
					Ci[*B] = struct{}{}
				}
			}
		}

		// Handle empty Ci
		var score string
		if len(Ci) == 0 {
			// No distinguishing blues → fall back to Pi hash only.
			// This keeps the tie-breaker deterministic and stable.
			score = Pi.String()
		} else {
			// max B in Ci by hash (as before)
			var maxB *externalapi.DomainHash
			for b := range Ci {
				bb := b
				if maxB == nil || maxB.Less(&bb) {
					maxB = &bb
				}
			}
			score = maxB.String() + Pi.String()
		}

		if score < bestScore || bestScore == "" {
			bestScore = score
			bestIdx = i
		}
	}

	return Ps[bestIdx], nil
}

func (gm *ghostdagManager) legacyKColouring(stagingArea *model.StagingArea, C *externalapi.DomainHash, G []*externalapi.DomainHash, k int, freeSearch bool, conditioning *externalapi.DomainHash) (KColouringResult, error) {
	// Step 1: Compute past_G(C)
	pastC, err := gm.legacygetPast(stagingArea, C, G)
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
		pastB, err := gm.legacygetPast(stagingArea, B, G)
		if err != nil {
			return KColouringResult{}, err
		}
		// Note: past(B) ∩ G = pastB since pastB ⊆ G

		// Step 2b: Check if B agrees with C (with conditioning)
		agrees, err := gm.legacyagrees(stagingArea, B, C, conditioning)
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
			res, err := gm.legacyKColouring(stagingArea, B, pastB, k, nextFreeSearch, conditioning)
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
		if len(parentResults[*b].blues) > maxBlues || (len(parentResults[*b].blues) == maxBlues && Bmax.Less(b)) {
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
		return anticone[i].Less(anticone[j])
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

func (gm *ghostdagManager) legacyagrees(stagingArea *model.StagingArea, B, C *externalapi.DomainHash, conditioning *externalapi.DomainHash) (bool, error) {
	if B.Equal(C) {
		return true, nil
	}

	lca, err := gm.legacylatestCommonChainAncestor(stagingArea, []*externalapi.DomainHash{B, C}, nil)
	if err != nil {
		return false, err
	}

	if conditioning != nil {
		// Avoid deep recursion with simple check
		condLCA, _ := gm.legacylatestCommonChainAncestor(stagingArea, []*externalapi.DomainHash{B, conditioning}, nil)
		if !lca.Equal(condLCA) { // stricter chain-descendant check
			return false, nil
		}
	}

	gdB, _ := gm.ghostdagDataStore.Get(gm.databaseContext, stagingArea, B, false)
	gdC, _ := gm.ghostdagDataStore.Get(gm.databaseContext, stagingArea, C, false)

	// Core of Def 3: LCA should be chain-descendant (no split after relevant point)
	return gdB.SelectedParent().Equal(gdC.SelectedParent()) || lca.Equal(gdB.SelectedParent()) || lca.Equal(gdC.SelectedParent()), nil
}

func (gm *ghostdagManager) legacygetPast(stagingArea *model.StagingArea, block *externalapi.DomainHash, G []*externalapi.DomainHash) ([]*externalapi.DomainHash, error) {
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
