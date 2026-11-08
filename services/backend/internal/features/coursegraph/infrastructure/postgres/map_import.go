package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	db "github.com/AskarKasimov/ai-tutor/services/backend/internal/shared/postgres/sqlcgen"
	"github.com/jackc/pgx/v5"
)

// SaveImportedMapInTx stores the Geek sheet of a two-sheet map within the
// import transaction: its topics resolve to the outcomes of the stored map
// revision by competency, constituent and outcome, their tasks become exam
// tasks of those outcomes, and the build of the graph is queued. The saved
// graph is the Geek view of the revision, not a second structure.
func SaveImportedMapInTx(ctx context.Context, tx pgx.Tx, subjectID, filename, sheet string, graph json.RawMessage, revision int64, now time.Time) error {
	q := db.New(tx)
	outcomes, err := q.CourseMapOutcomesOfRevision(ctx, revision)
	if err != nil {
		return err
	}
	graph, tasks, err := resolveMapTopics(graph, outcomes, now)
	if err != nil {
		return err
	}
	for _, task := range tasks {
		if err := q.InsertCourseMapExamTask(ctx, task); err != nil {
			return err
		}
	}
	sum := sha256.Sum256(graph)
	if err := q.SaveCourseGraphMap(ctx, db.SaveCourseGraphMapParams{SubjectID: subjectID, Filename: filename, Sheet: sheet,
		Fingerprint: hex.EncodeToString(sum[:]), Graph: graph, Now: now.Unix(), MapRevision: &revision}); err != nil {
		return err
	}
	_, err = q.RequestCourseGraphMapBuild(ctx, db.RequestCourseGraphMapBuildParams{SubjectID: subjectID, Now: now.Unix()})
	return err
}

// resolveMapTopics renames the topics and edges of a Geek map graph to the
// outcome IDs of the revision and gives every task its exam task ID.
func resolveMapTopics(graph json.RawMessage, outcomes []db.CourseMapOutcomesOfRevisionRow, now time.Time) (json.RawMessage, []db.InsertCourseMapExamTaskParams, error) {
	type vertex struct{ ID, CompetencyID, ConstituentID string }
	full, short, ambiguous := map[string]vertex{}, map[string]vertex{}, map[string]bool{}
	for _, o := range outcomes {
		v := vertex{ID: o.ID, CompetencyID: o.CompetencyID, ConstituentID: o.ConstituentID}
		key := structureKey(o.CompetencyName, o.ConstituentName, o.OutcomeName)
		if _, exists := full[key]; exists {
			return nil, nil, fmt.Errorf("duplicate course map outcome %q", o.OutcomeName)
		}
		full[key] = v
		shortKey := structureKey(o.CompetencyName, "", o.OutcomeName)
		if _, exists := short[shortKey]; exists {
			ambiguous[shortKey] = true
		}
		short[shortKey] = v
	}
	var shape map[string]json.RawMessage
	if err := json.Unmarshal(graph, &shape); err != nil {
		return nil, nil, err
	}
	var topics []map[string]json.RawMessage
	if err := json.Unmarshal(shape["topics"], &topics); err != nil {
		return nil, nil, err
	}
	text := func(topic map[string]json.RawMessage, field string) string {
		var value string
		_ = json.Unmarshal(topic[field], &value)
		return value
	}
	ids := make(map[string]string, len(topics))
	tasks := []db.InsertCourseMapExamTaskParams{}
	for _, topic := range topics {
		title, competency, constituent := text(topic, "title"), text(topic, "parent_title"), text(topic, "constituent_title")
		v, ok := full[structureKey(competency, constituent, title)]
		if !ok && constituent == "" && !ambiguous[structureKey(competency, "", title)] {
			v, ok = short[structureKey(competency, "", title)]
		}
		if !ok {
			return nil, nil, fmt.Errorf("Geek map outcome %q is not in the course map", title)
		}
		ids[text(topic, "id")] = v.ID
		var variants []map[string]json.RawMessage
		if err := json.Unmarshal(topic["variants"], &variants); err != nil {
			return nil, nil, err
		}
		for i, variant := range variants {
			id := fmt.Sprintf("%s:exam:%d", v.ID, i+1)
			var question, content, guidance string
			_ = json.Unmarshal(variant["question"], &question)
			_ = json.Unmarshal(variant["content"], &content)
			_ = json.Unmarshal(variant["assessment_guidance"], &guidance)
			task := db.InsertCourseMapExamTaskParams{ID: id, OutcomeID: v.ID, Position: int32(i + 1), Question: question, ReferenceAnswer: &content, CreatedAt: now.Unix()}
			if guidance != "" {
				task.Criteria = &guidance
			}
			tasks = append(tasks, task)
			variant["task_id"], _ = json.Marshal(id)
		}
		var err error
		if topic["variants"], err = json.Marshal(variants); err != nil {
			return nil, nil, err
		}
		topic["competency_id"], _ = json.Marshal(v.CompetencyID)
		topic["constituent_id"], _ = json.Marshal(v.ConstituentID)
	}
	rename := func(id string) (string, error) {
		if renamed, ok := ids[id]; ok {
			return renamed, nil
		}
		return "", fmt.Errorf("Geek map relation references unknown topic %q", id)
	}
	for _, topic := range topics {
		renamed, err := rename(text(topic, "id"))
		if err != nil {
			return nil, nil, err
		}
		topic["id"], _ = json.Marshal(renamed)
		var requires []string
		if err := json.Unmarshal(topic["requires"], &requires); err != nil {
			return nil, nil, err
		}
		for i := range requires {
			if requires[i], err = rename(requires[i]); err != nil {
				return nil, nil, err
			}
		}
		topic["requires"], _ = json.Marshal(requires)
	}
	var err error
	if shape["topics"], err = json.Marshal(topics); err != nil {
		return nil, nil, err
	}
	for _, field := range []string{"prerequisites", "hierarchy"} {
		var edges []map[string]json.RawMessage
		if err := json.Unmarshal(shape[field], &edges); err != nil {
			return nil, nil, err
		}
		for _, edge := range edges {
			for _, end := range []string{"source", "target"} {
				renamed, err := rename(text(edge, end))
				if err != nil {
					return nil, nil, err
				}
				edge[end], _ = json.Marshal(renamed)
			}
		}
		if shape[field], err = json.Marshal(edges); err != nil {
			return nil, nil, err
		}
	}
	out, err := json.Marshal(shape)
	return out, tasks, err
}

// keepRephrases stores the prepared rephrases of exam tasks with the tasks,
// so the published graph is reproducible from the course map.
func keepRephrases(ctx context.Context, q *db.Queries, graph json.RawMessage) error {
	var shape struct {
		Topics []struct {
			Variants []struct {
				TaskID   string `json:"task_id"`
				Rephrase string `json:"rephrase"`
			} `json:"variants"`
		} `json:"topics"`
	}
	if err := json.Unmarshal(graph, &shape); err != nil {
		return err
	}
	for _, topic := range shape.Topics {
		for _, variant := range topic.Variants {
			if variant.TaskID == "" {
				continue
			}
			n, err := q.UpdateCourseMapExamTaskRephrase(ctx, db.UpdateCourseMapExamTaskRephraseParams{ID: variant.TaskID, Rephrase: variant.Rephrase})
			if err != nil {
				return err
			}
			if n != 1 {
				return fmt.Errorf("prepared task %q is not an exam task of the course map", variant.TaskID)
			}
		}
	}
	return nil
}

func structureKey(competency, constituent, outcome string) string {
	normalize := func(value string) string { return strings.Join(strings.Fields(strings.ToLower(value)), " ") }
	return normalize(competency) + "\x00" + normalize(constituent) + "\x00" + normalize(outcome)
}
