package coursegraph

import (
	"cmp"
	"slices"
)

// Candidate is an outcome that can enter the course graph: it is included in
// the test, has a task of the mode and a valid Bloom rank and importance.
// Order is its position in the map.
type Candidate struct {
	Key, Name        string
	Rank, Importance int
	Order            int
}

// Pool is the candidates of one constituent, in any order.
type Pool struct {
	Key, Name  string
	Candidates []Candidate
}

// Tree is the part of the course graph a constituent gives: a top-level root
// and its two prerequisites.
type Tree struct {
	// Pool is the position of the constituent's pool in the input.
	Pool              int
	Constituent, Name string
	Root              Candidate
	Children          []Candidate
}

// Skip is a constituent left out of the graph, with how many candidates it had.
type Skip struct {
	Pool              int
	Constituent, Name string
	Eligible          int
}

// TreeSize is the outcomes a constituent needs: the root and two children.
const TreeSize = 3

// SelectTrees is the one rule both the learning diagnostic and Geek build
// their graph by, each from its own sheet (decision of 08.11.2026). In every
// constituent the root is the hardest candidate: the highest Bloom rank, then
// importance, then map order. Its children are the two simplest of the rest:
// the lowest Bloom rank, then importance descending, then map order. The other
// outcomes stay outside the traversal. A constituent with fewer than TreeSize
// candidates is skipped. Trees and skips keep the order of pools.
func SelectTrees(pools []Pool) ([]Tree, []Skip) {
	trees, skips := []Tree{}, []Skip{}
	for i, pool := range pools {
		if len(pool.Candidates) < TreeSize {
			skips = append(skips, Skip{Pool: i, Constituent: pool.Key, Name: pool.Name, Eligible: len(pool.Candidates)})
			continue
		}
		candidates := slices.Clone(pool.Candidates)
		slices.SortStableFunc(candidates, func(a, b Candidate) int { return cmp.Compare(a.Order, b.Order) })
		root := slices.MinFunc(candidates, harder)
		rest := slices.DeleteFunc(candidates, func(c Candidate) bool { return c.Key == root.Key })
		slices.SortStableFunc(rest, simpler)
		trees = append(trees, Tree{Pool: i, Constituent: pool.Key, Name: pool.Name, Root: root, Children: slices.Clone(rest[:TreeSize-1])})
	}
	return trees, skips
}

// harder orders roots first: Bloom rank and importance descending, then map
// order, name and key.
func harder(a, b Candidate) int {
	return cmp.Or(cmp.Compare(b.Rank, a.Rank), cmp.Compare(b.Importance, a.Importance), cmp.Compare(a.Order, b.Order),
		cmp.Compare(a.Name, b.Name), cmp.Compare(a.Key, b.Key))
}

// simpler orders children first: Bloom rank ascending, importance descending,
// then map order, name and key.
func simpler(a, b Candidate) int {
	return cmp.Or(cmp.Compare(a.Rank, b.Rank), cmp.Compare(b.Importance, a.Importance), cmp.Compare(a.Order, b.Order),
		cmp.Compare(a.Name, b.Name), cmp.Compare(a.Key, b.Key))
}
