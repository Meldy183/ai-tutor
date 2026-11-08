// Package graph supplies course graphs to Geek from the database: the graph
// built from the course materials or from a competency map. A course without
// one has no graph.
package graph

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/coursegraph"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/exam"
	db "github.com/AskarKasimov/ai-tutor/services/backend/internal/shared/postgres/sqlcgen"
)

func orEmpty(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

// Stored reads the course graph from PostgreSQL.
type Stored struct {
	pool *pgxpool.Pool
}

func NewStored(pool *pgxpool.Pool) Stored {
	return Stored{pool: pool}
}

// Current is the published graph of the course. A database failure is an
// error, not a missing graph.
// The version is the hash of the published graph: any rebuild that changes it
// is a new version, which cancels active diagnostics. raw.Version is the
// format of the course-graph result, not the build.
func (s Stored) Current(ctx context.Context, subjectID string) (exam.Graph, bool, error) {
	if s.pool == nil {
		return exam.Graph{}, false, nil
	}
	data, err := db.New(s.pool).ReadPublishedCourseGraph(ctx, subjectID)
	if errors.Is(err, pgx.ErrNoRows) {
		return exam.Graph{}, false, nil
	}
	if err != nil {
		return exam.Graph{}, false, fmt.Errorf("read stored graph: %w", err)
	}
	g, ok, err := Decode(data)
	if err != nil || !ok {
		return exam.Graph{}, false, err
	}
	sum := sha256.Sum256(data)
	g.Version = "pg:" + hex.EncodeToString(sum[:8])
	return g, true, nil
}

// Decode reads a graph as it is stored: the examined graph (nodes) or a
// course-graph build (topics and prerequisite edges). ok is false when there
// is no graph. The version is left empty.
func Decode(data []byte) (exam.Graph, bool, error) {
	if len(data) == 0 || string(data) == "null" {
		return exam.Graph{}, false, nil
	}
	var raw struct {
		Version string      `json:"version"`
		Source  string      `json:"source"`
		Nodes   []exam.Node `json:"nodes"`
		Topics  []struct {
			ID            string                `json:"id"`
			Title         string                `json:"title"`
			Text          string                `json:"text"`
			Content       string                `json:"content"`
			Question      string                `json:"question"`
			Rephrase      string                `json:"rephrase"`
			Guidance      string                `json:"assessment_guidance"`
			Variants      []exam.Variant        `json:"variants"`
			Provides      []string              `json:"provides"`
			Requires      []string              `json:"requires"`
			CompetencyID  string                `json:"competency_id"`
			ConstituentID string                `json:"constituent_id"`
			Sources       []exam.SourceCitation `json:"sources"`
			Parent        string                `json:"parent_title"`
		} `json:"topics"`
		Prerequisites []struct {
			Source string `json:"source"`
			Target string `json:"target"`
		} `json:"prerequisites"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return exam.Graph{}, false, fmt.Errorf("decode stored graph: %w", err)
	}
	g := exam.Graph{Source: raw.Source}
	if len(raw.Nodes) > 0 {
		g.Nodes = raw.Nodes
	} else if len(raw.Topics) > 0 {
		prereqsByTarget := make(map[string][]string)
		for _, edge := range raw.Prerequisites {
			prereqsByTarget[edge.Target] = append(prereqsByTarget[edge.Target], edge.Source)
		}
		g.Nodes = make([]exam.Node, len(raw.Topics))
		for i, t := range raw.Topics {
			content := t.Content
			if content == "" {
				content = t.Text
			}
			var questions []string
			if strings.TrimSpace(t.Question) != "" {
				questions = []string{strings.TrimSpace(t.Question)}
			}
			g.Nodes[i] = exam.Node{
				ID:            t.ID,
				Title:         t.Title,
				Content:       content,
				Prerequisites: prereqsByTarget[t.ID],
				Outcomes:      t.Provides,
				Questions:     questions,
				Rephrase:      strings.TrimSpace(t.Rephrase),
				// A graph built from a competency map carries the rubric of
				// its task as the teacher's rule for the grader.
				AssessmentGuidance: strings.TrimSpace(t.Guidance),
				Sources:            t.Sources,
				// Its tasks, one of which the exam asks.
				Variants: t.Variants,
			}
			if raw.Source == coursegraph.SourceMap {
				g.Nodes[i].Competency = strings.TrimSpace(t.Parent)
				g.Nodes[i].CompetencyID = strings.TrimSpace(t.CompetencyID)
				g.Nodes[i].ConstituentID = strings.TrimSpace(t.ConstituentID)
			}
		}
	} else {
		return exam.Graph{}, false, nil
	}

	if err := g.Validate(); err != nil {
		return exam.Graph{}, false, err
	}
	for i := range g.Nodes {
		n := &g.Nodes[i]
		n.Prerequisites, n.Outcomes, n.Questions = orEmpty(n.Prerequisites), orEmpty(n.Outcomes), orEmpty(n.Questions)
	}
	return g, true, nil
}
