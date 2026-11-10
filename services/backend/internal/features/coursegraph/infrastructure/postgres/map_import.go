package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/audioasset"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/coursegraph"
	pgshared "github.com/AskarKasimov/ai-tutor/services/backend/internal/shared/postgres"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/shared/security"

	db "github.com/AskarKasimov/ai-tutor/services/backend/internal/shared/postgres/sqlcgen"
	"github.com/jackc/pgx/v5"
)

// SaveImportedMapInTx stores the Geek sheet of a two-sheet map within the
// import transaction: its complete bank owns exam outcomes in the same schema,
// independently of the diagnostic sheet. All variants become exam tasks and
// the graph build is queued. The saved
// graph is the Geek view of the revision, not a second structure.
//
// A graph of a unified map shares the outcomes of the diagnostic: no exam
// structure is stored, and the exam tasks belong to the same outcomes as the
// diagnostic ones, so a gap found in either mode is the same outcome.
func SaveImportedMapInTx(ctx context.Context, tx pgx.Tx, subjectID, filename, sheet string, graph json.RawMessage, revision int64, now time.Time) error {
	q := db.New(tx)
	source := "diagnostic"
	if !coursegraph.SharesOutcomes(graph) {
		source = "exam"
		if err := saveExamOutcomes(ctx, q, graph, revision); err != nil {
			return err
		}
	}
	outcomes, err := q.CourseMapOutcomesOfRevision(ctx, db.CourseMapOutcomesOfRevisionParams{Revision: revision, SourceMode: source})
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
		if audioasset.CanSynthesize(task.Question) {
			assetID := audioasset.IDForTask(task.ID)
			if _, err := pgshared.EnqueueTaskAudio(ctx, q, task.ID, assetID, audioasset.ObjectKey(assetID), task.Question); err != nil {
				return err
			}
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
	full := map[string]vertex{}
	for _, o := range outcomes {
		v := vertex{ID: o.ID, CompetencyID: o.CompetencyID, ConstituentID: o.ConstituentID}
		key := structureKey(o.CompetencyName, o.ConstituentName, o.OutcomeName)
		if _, exists := full[key]; exists {
			return nil, nil, fmt.Errorf("duplicate course map outcome %q", o.OutcomeName)
		}
		full[key] = v

	}
	var shape map[string]json.RawMessage
	if err := json.Unmarshal(graph, &shape); err != nil {
		return nil, nil, err
	}
	var topics, bank []map[string]json.RawMessage
	if err := json.Unmarshal(shape["topics"], &topics); err != nil {
		return nil, nil, err
	}
	if err := json.Unmarshal(shape["bank"], &bank); err != nil {
		return nil, nil, err
	}

	text := func(topic map[string]json.RawMessage, field string) string {
		var value string
		_ = json.Unmarshal(topic[field], &value)
		return value
	}
	ids := make(map[string]string, len(topics))
	tasks := []db.InsertCourseMapExamTaskParams{}
	for _, topic := range bank {
		title, competency, constituent := text(topic, "title"), text(topic, "parent_title"), text(topic, "constituent_title")
		v, ok := full[structureKey(competency, constituent, title)]

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
	byID := map[string]map[string]json.RawMessage{}
	for _, topic := range bank {
		byID[text(topic, "id")] = topic
	}
	for _, topic := range topics {
		source, ok := byID[text(topic, "id")]
		if !ok {
			return nil, nil, fmt.Errorf("selected topic has no bank outcome")
		}
		for _, field := range []string{"variants", "competency_id", "constituent_id"} {
			topic[field] = source[field]
		}
	}

	rename := func(id string) (string, error) {
		if renamed, ok := ids[id]; ok {
			return renamed, nil
		}
		return "", fmt.Errorf("Geek map relation references unknown topic %q", id)
	}
	for _, topic := range append(append([]map[string]json.RawMessage{}, topics...), bank...) {
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
	if shape["bank"], err = json.Marshal(bank); err != nil {
		return nil, nil, err
	}
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

// saveExamOutcomes stores the complete first sheet, independently of the
// diagnostic sheet's names and selection, in the same neutral schema.
func saveExamOutcomes(ctx context.Context, q *db.Queries, graph json.RawMessage, revision int64) error {
	var shape struct {
		Bank []struct {
			ID          string `json:"id"`
			Title       string `json:"title"`
			Competency  string `json:"parent_title"`
			Constituent string `json:"constituent_title"`
			Content     string `json:"text"`
			Included    bool   `json:"include_in_test"`
			Importance  int    `json:"importance"`
			Bloom       int    `json:"bloom"`
		} `json:"bank"`
	}
	if err := json.Unmarshal(graph, &shape); err != nil {
		return err
	}
	if len(shape.Bank) == 0 {
		return fmt.Errorf("exam map has no outcome bank")
	}
	competencies, constituents := map[string]string{}, map[string]string{}
	seen := map[string]bool{}
	for _, o := range shape.Bank {
		key := structureKey(o.Competency, o.Constituent, o.Title)
		if seen[key] {
			return fmt.Errorf("duplicate exam outcome %q", o.Title)
		}
		seen[key] = true
		compKey := structureKey(o.Competency, "", "")
		compID := competencies[compKey]
		if compID == "" {
			var err error
			compID, err = security.ID("competency")
			if err != nil {
				return err
			}
			if err = q.InsertCourseMapExamCompetency(ctx, db.InsertCourseMapExamCompetencyParams{ID: compID, Name: o.Competency, Revision: revision}); err != nil {
				return err
			}
			competencies[compKey] = compID
		}
		partKey := structureKey(o.Competency, o.Constituent, "")
		partID := constituents[partKey]
		if partID == "" {
			var err error
			partID, err = security.ID("constituent")
			if err != nil {
				return err
			}
			if err = q.LearningInsertConstituent(ctx, db.LearningInsertConstituentParams{ID: partID, CompetencyID: compID, Name: o.Constituent}); err != nil {
				return err
			}
			constituents[partKey] = partID
		}
		id, err := security.ID("outcome")
		if err != nil {
			return err
		}
		taxonomy := map[int]string{1: "knowledge", 2: "understanding", 3: "application", 4: "analysis"}[o.Bloom]
		if err = q.LearningInsertOutcome(ctx, db.LearningInsertOutcomeParams{ID: id, ConstituentID: partID, Name: o.Title, IncludeInTest: &o.Included, Importance: int16(o.Importance), TaxonomyCode: taxonomy, EducationalContent: o.Content}); err != nil {
			return err
		}
	}
	return nil
}
