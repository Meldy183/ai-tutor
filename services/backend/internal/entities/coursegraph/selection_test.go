package coursegraph

import (
	"slices"
	"testing"
)

func TestSelectTreesTakesTheHardestRootAndTheTwoSimplestChildren(t *testing.T) {
	pool := Pool{Key: "c", Name: "Составляющая", Candidates: []Candidate{
		{Key: "o1", Rank: 1, Importance: 3, Order: 1},
		{Key: "o2", Rank: 3, Importance: 2, Order: 2},
		{Key: "o3", Rank: 3, Importance: 5, Order: 3},
		{Key: "o4", Rank: 1, Importance: 5, Order: 4},
		{Key: "o5", Rank: 2, Importance: 5, Order: 5},
	}}
	trees, skips := SelectTrees([]Pool{pool, {Key: "small", Name: "Мало", Candidates: pool.Candidates[:2]}})
	if len(trees) != 1 || len(skips) != 1 || skips[0] != (Skip{Pool: 1, Constituent: "small", Name: "Мало", Eligible: 2}) {
		t.Fatalf("trees = %+v, skips = %+v", trees, skips)
	}
	tree := trees[0]
	children := []string{tree.Children[0].Key, tree.Children[1].Key}
	// o3: rank 3 and importance 5; o4 and o1 have rank 1, o4 is more important.
	if tree.Root.Key != "o3" || !slices.Equal(children, []string{"o4", "o1"}) {
		t.Fatalf("tree = %+v", tree)
	}
	// The input order does not change the selection.
	reversed := slices.Clone(pool.Candidates)
	slices.Reverse(reversed)
	again, _ := SelectTrees([]Pool{{Key: "c", Candidates: reversed}})
	if again[0].Root.Key != "o3" || again[0].Children[0].Key != "o4" || again[0].Children[1].Key != "o1" {
		t.Fatalf("reversed tree = %+v", again[0])
	}
}

func TestSelectTreesBreaksTiesByMapOrder(t *testing.T) {
	trees, _ := SelectTrees([]Pool{{Key: "c", Candidates: []Candidate{
		{Key: "b", Rank: 2, Importance: 3, Order: 2},
		{Key: "a", Rank: 2, Importance: 3, Order: 1},
		{Key: "c", Rank: 2, Importance: 3, Order: 3},
	}}})
	if trees[0].Root.Key != "a" || trees[0].Children[0].Key != "b" || trees[0].Children[1].Key != "c" {
		t.Fatalf("tree = %+v", trees[0])
	}
}
