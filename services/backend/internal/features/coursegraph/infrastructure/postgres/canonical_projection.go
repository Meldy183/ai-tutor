package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

type graphQueryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

// CanonicalizeMapProjection maps the imported Geek vertices and relationships
// to the exact vertex IDs of one persisted learning-map revision.
func CanonicalizeMapProjection(ctx context.Context, queryer graphQueryer, graph json.RawMessage, revision int64) (json.RawMessage, error) {
	type topic struct {
		ID            string   `json:"id"`
		Title         string   `json:"title"`
		Parent        string   `json:"parent_title"`
		Constituent   string   `json:"constituent_title"`
		CompetencyID  string   `json:"competency_id"`
		ConstituentID string   `json:"constituent_id"`
		Requires      []string `json:"requires"`
		Role          string   `json:"role"`
		Variants      []struct {
			Question           string `json:"question"`
			Content            string `json:"content"`
			AssessmentGuidance string `json:"assessment_guidance"`
		} `json:"variants"`
	}
	type vertex struct{ ID, CompetencyID, ConstituentID string }
	type edge struct {
		Source string `json:"source"`
		Target string `json:"target"`
		Reason string `json:"reason"`
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(graph, &payload); err != nil {
		return nil, err
	}
	var projection struct {
		Topics        []topic `json:"topics"`
		Prerequisites []edge  `json:"prerequisites"`
		Hierarchy     []edge  `json:"hierarchy"`
	}
	if err := json.Unmarshal(payload["topics"], &projection.Topics); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(payload["prerequisites"], &projection.Prerequisites); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(payload["hierarchy"], &projection.Hierarchy); err != nil {
		return nil, err
	}
	rows, err := queryer.Query(ctx, `SELECT outcome.id, competency.id, constituent.id, competency.name, constituent.name, outcome.name
		FROM learning_outcomes outcome
		JOIN learning_constituents constituent ON constituent.id=outcome.constituent_id
		JOIN learning_competencies competency ON competency.id=constituent.competency_id
		WHERE competency.revision=$1`, revision)
	if err != nil {
		return nil, err
	}
	canonical := make(map[string]vertex)
	canonicalByCompetencyOutcome := make(map[string]vertex)
	ambiguousShortKeys := make(map[string]bool)
	for rows.Next() {
		var id, competencyID, constituentID, competency, constituent, outcome string
		if err = rows.Scan(&id, &competencyID, &constituentID, &competency, &constituent, &outcome); err != nil {
			rows.Close()
			return nil, err
		}
		key := mapStructureKey(competency, constituent, outcome)
		if _, exists := canonical[key]; exists {
			rows.Close()
			return nil, fmt.Errorf("duplicate canonical course outcome structure")
		}
		canonical[key] = vertex{ID: id, CompetencyID: competencyID, ConstituentID: constituentID}
		shortKey := mapStructureKey(competency, "", outcome)
		if canonicalByCompetencyOutcome[shortKey].ID != "" {
			ambiguousShortKeys[shortKey] = true
		}
		canonicalByCompetencyOutcome[shortKey] = canonical[key]
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	projectionIDs := make(map[string]string, len(projection.Topics))
	for i := range projection.Topics {
		item := &projection.Topics[i]
		key := mapStructureKey(item.Parent, item.Constituent, item.Title)
		vertex := canonical[key]
		if item.Constituent == "" {
			shortKey := mapStructureKey(item.Parent, "", item.Title)
			if !ambiguousShortKeys[shortKey] {
				vertex = canonicalByCompetencyOutcome[shortKey]
			}
		}
		if vertex.ID == "" {
			return nil, fmt.Errorf("Geek map outcome does not resolve to canonical course graph: %q", item.Title)
		}
		projectionIDs[item.ID] = vertex.ID
		item.CompetencyID = vertex.CompetencyID
		item.ConstituentID = vertex.ConstituentID
	}
	remap := func(id string) (string, error) {
		canonicalID := projectionIDs[id]
		if canonicalID == "" {
			return "", fmt.Errorf("Geek map relation references unknown course graph vertex %q", id)
		}
		return canonicalID, nil
	}
	for i := range projection.Topics {
		item := &projection.Topics[i]
		oldID := item.ID
		item.ID = projectionIDs[oldID]
		for j, required := range item.Requires {
			item.Requires[j], err = remap(required)
			if err != nil {
				return nil, err
			}
		}
	}
	for _, list := range [][]edge{projection.Prerequisites, projection.Hierarchy} {
		for i := range list {
			list[i].Source, err = remap(list[i].Source)
			if err != nil {
				return nil, err
			}
			list[i].Target, err = remap(list[i].Target)
			if err != nil {
				return nil, err
			}
		}
	}
	payload["topics"], err = json.Marshal(projection.Topics)
	if err != nil {
		return nil, err
	}
	payload["prerequisites"], err = json.Marshal(projection.Prerequisites)
	if err != nil {
		return nil, err
	}
	payload["hierarchy"], err = json.Marshal(projection.Hierarchy)
	if err != nil {
		return nil, err
	}
	return json.Marshal(payload)
}

func mapStructureKey(competency, constituent, outcome string) string {
	normalize := func(value string) string { return strings.Join(strings.Fields(strings.ToLower(value)), " ") }
	return normalize(competency) + "\x00" + normalize(constituent) + "\x00" + normalize(outcome)
}
