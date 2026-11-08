// Package exam holds the oral exam domain: the course graph, topic grading and
// the traversal rules of the diagnostic.
package exam

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"

	"github.com/AskarKasimov/ai-tutor/services/backend/internal/shared/stats"
)

// Node is a topic of the course graph. Prerequisites reference other nodes;
// outcomes and questions may be empty until they are prepared.
type Node struct {
	ID            string   `json:"id"`
	Title         string   `json:"title"`
	Content       string   `json:"content"`
	Prerequisites []string `json:"prerequisites"`
	Outcomes      []string `json:"outcomes"`
	Questions     []string `json:"questions"`
	// Rephrase is the prepared rephrase of Questions[0]; empty until prepared,
	// then Geek generates it at runtime as before.
	Rephrase string `json:"rephrase,omitempty"`
	// AssessmentGuidance is an instructor's explicit scope for this topic's question.
	AssessmentGuidance string           `json:"assessment_guidance,omitempty"`
	Sources            []SourceCitation `json:"sources"`
	// Variants are the tasks of a topic built from a competency map; the
	// exam asks one of them, picked when it starts (decision of 19.10.2026).
	// The question, rephrase, content and guidance of the node are those of
	// the first variant.
	Variants []Variant `json:"variants,omitempty"`
	// Competency groups the topics of a competency map; other graphs have none.
	Competency    string `json:"competency,omitempty"`
	CompetencyID  string `json:"competency_id,omitempty"`
	ConstituentID string `json:"constituent_id,omitempty"`
}

// Variant is one task of a topic with what grading it needs.
type Variant struct {
	// TaskID is the exam task of the course map the variant comes from;
	// empty for a graph of materials.
	TaskID   string `json:"task_id,omitempty"`
	Question string `json:"question"`
	// Rephrase is the prepared rephrase of Question; empty until prepared.
	Rephrase           string `json:"rephrase,omitempty"`
	Content            string `json:"content"`
	AssessmentGuidance string `json:"assessment_guidance,omitempty"`
}

// WithVariant is the node that asks v as its only question.
func (n Node) WithVariant(v Variant) Node {
	n.Questions = []string{v.Question}
	n.Rephrase = v.Rephrase
	n.Content = v.Content
	n.AssessmentGuidance = v.AssessmentGuidance
	n.Variants = nil
	return n
}

// pickVariants gives every node with variants one of them at random; nil rng
// takes the first.
func (g Graph) pickVariants(rng *rand.Rand) Graph {
	nodes := make([]Node, len(g.Nodes))
	for i, n := range g.Nodes {
		if len(n.Variants) > 0 {
			v := n.Variants[0]
			if rng != nil {
				v = n.Variants[rng.IntN(len(n.Variants))]
			}
			n = n.WithVariant(v)
		}
		nodes[i] = n
	}
	g.Nodes = nodes
	return g
}

// MainQuestion is the prepared main question, or "" when Geek generates it at
// runtime.
func (n Node) MainQuestion() string {
	if len(n.Questions) == 0 {
		return ""
	}
	return strings.TrimSpace(n.Questions[0])
}

// PreparedRephrase is the prepared rephrase of the main question. It belongs
// to the prepared main question only: a main question generated at runtime is
// rephrased at runtime too.
func (n Node) PreparedRephrase() string {
	if n.MainQuestion() == "" {
		return ""
	}
	return strings.TrimSpace(n.Rephrase)
}

// Prepared reports whether both the main question and its rephrase are
// prepared, so a student waits for neither; with variants, those of each.
func (n Node) Prepared() bool {
	if len(n.Variants) == 0 {
		return n.PreparedRephrase() != ""
	}
	for _, v := range n.Variants {
		if !n.WithVariant(v).Prepared() {
			return false
		}
	}
	return true
}

// Source is a provenance citation from a particular source revision.
type SourceCitation struct {
	MaterialID       string  `json:"material_id"`
	BlockID          int     `json:"block_id"`
	Start            int     `json:"start"`
	End              int     `json:"end"`
	Text             string  `json:"text"`
	Page             *int    `json:"page,omitempty"`
	Week             *int32  `json:"week"`
	Kind             *string `json:"kind"`
	Filename         *string `json:"filename"`
	KindLabel        *string `json:"kind_label"`
	MaterialRevision *string `json:"material_revision"`
}

// Graph is the course graph: nodes in study order and acyclic prerequisite
// edges. Version changes whenever the graph is rebuilt.
type Graph struct {
	Version string `json:"version"`
	// Source is "competency_map" for a graph built from a competency map,
	// otherwise empty.
	Source string `json:"source,omitempty"`
	Nodes  []Node `json:"nodes"`
}

var ErrInvalidGraph = errors.New("invalid course graph")

// Validate checks that IDs are unique and non-empty, prerequisites exist and
// the prerequisite graph has no cycles.
func (g Graph) Validate() error {
	if len(g.Nodes) == 0 {
		return fmt.Errorf("%w: no nodes", ErrInvalidGraph)
	}
	index := make(map[string]int, len(g.Nodes))
	for i, n := range g.Nodes {
		if strings.TrimSpace(n.ID) == "" || strings.TrimSpace(n.Title) == "" {
			return fmt.Errorf("%w: node %d needs an id and a title", ErrInvalidGraph, i)
		}
		if _, dup := index[n.ID]; dup {
			return fmt.Errorf("%w: duplicate node %q", ErrInvalidGraph, n.ID)
		}
		index[n.ID] = i
	}
	for _, n := range g.Nodes {
		for _, p := range n.Prerequisites {
			if _, ok := index[p]; !ok {
				return fmt.Errorf("%w: node %q needs unknown prerequisite %q", ErrInvalidGraph, n.ID, p)
			}
		}
	}
	const (
		visiting = 1
		done     = 2
	)
	state := make(map[string]int, len(g.Nodes))
	var visit func(id string) error
	visit = func(id string) error {
		switch state[id] {
		case visiting:
			return fmt.Errorf("%w: prerequisite cycle through %q", ErrInvalidGraph, id)
		case done:
			return nil
		}
		state[id] = visiting
		for _, p := range g.Nodes[index[id]].Prerequisites {
			if err := visit(p); err != nil {
				return err
			}
		}
		state[id] = done
		return nil
	}
	for _, n := range g.Nodes {
		if err := visit(n.ID); err != nil {
			return err
		}
	}
	return nil
}

// Node returns the node with the given ID.
func (g Graph) Node(id string) (Node, bool) {
	for _, n := range g.Nodes {
		if n.ID == id {
			return n, true
		}
	}
	return Node{}, false
}

// TopLevel returns, in study order, the nodes that are not a prerequisite of
// any other node. The exam starts from them and the course grade averages them.
// competencyShares normalizes the shares of the scored topics: a top-level topic
// with its prerequisites is one constituent, the mean of its topics; a competency
// is the mean of its constituents. A graph without competencies treats every
// top-level topic as one. A top-level topic without its own result is no
// constituent, even if a prerequisite it shares was scored; a prerequisite of two
// scored topics counts in both.
func (g Graph) competencyShares(topics map[string]float64) []float64 {
	nodes := make(map[string]Node, len(g.Nodes))
	for _, n := range g.Nodes {
		nodes[n.ID] = n
	}
	order := []string{}
	constituents := map[string][]float64{}
	for _, top := range g.TopLevel() {
		if _, scored := topics[top]; !scored {
			continue
		}
		shares := []float64{}
		seen := map[string]bool{}
		var walk func(id string)
		walk = func(id string) {
			if seen[id] {
				return
			}
			seen[id] = true
			if share, ok := topics[id]; ok {
				shares = append(shares, share)
			}
			for _, p := range nodes[id].Prerequisites {
				walk(p)
			}
		}
		walk(top)
		key := nodes[top].Competency
		if key == "" {
			key = "\x00" + top
		}
		if _, ok := constituents[key]; !ok {
			order = append(order, key)
		}
		constituents[key] = append(constituents[key], stats.Mean(shares))
	}
	out := make([]float64, len(order))
	for i, key := range order {
		out[i] = stats.Mean(constituents[key])
	}
	return out
}

func (g Graph) TopLevel() []string {
	required := map[string]bool{}
	for _, n := range g.Nodes {
		for _, p := range n.Prerequisites {
			required[p] = true
		}
	}
	top := []string{}
	for _, n := range g.Nodes {
		if !required[n.ID] {
			top = append(top, n.ID)
		}
	}
	return top
}

// Closure returns the transitive prerequisites of a node, without the node,
// in depth-first order.
func (g Graph) Closure(id string) []string { return closure(g.prerequisites(), id) }

// prerequisites indexes the prerequisites of every node by its ID, so that
// many closures share one index instead of a linear search per node.
func (g Graph) prerequisites() map[string][]string {
	index := make(map[string][]string, len(g.Nodes))
	for _, n := range g.Nodes {
		index[n.ID] = n.Prerequisites
	}
	return index
}

func closure(prerequisites map[string][]string, id string) []string {
	seen := map[string]bool{id: true}
	out := []string{}
	var walk func(string)
	walk = func(id string) {
		for _, p := range prerequisites[id] {
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
				walk(p)
			}
		}
	}
	walk(id)
	return out
}
