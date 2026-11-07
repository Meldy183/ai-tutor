package app

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/competencymap"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/coursegraph"
	competencypg "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/competency/infrastructure/postgres"
	graphpg "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/coursegraph/infrastructure/postgres"
	db "github.com/AskarKasimov/ai-tutor/services/backend/internal/shared/postgres/sqlcgen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type pairedCourseGraphRepository struct {
	*graphpg.Repository
	pool     *pgxpool.Pool
	learning *competencypg.Repository
}

func (r *pairedCourseGraphRepository) MapGraph(ctx context.Context, subjectID string) (json.RawMessage, error) {
	metadata, err := r.Repository.MapGraph(ctx, subjectID)
	if err != nil {
		return nil, err
	}
	var mapData struct {
		Canonical bool `json:"canonical"`
	}
	if err = json.Unmarshal(metadata, &mapData); err != nil || !mapData.Canonical {
		return metadata, err
	}
	row, err := db.New(r.pool).ReadCourseGraphMapProjection(ctx, subjectID)
	return row, err
}

func (r *pairedCourseGraphRepository) Read(ctx context.Context, subjectID string) (coursegraph.Snapshot, error) {
	snapshot, err := r.Repository.Read(ctx, subjectID)
	if err != nil || snapshot.GraphSource != coursegraph.SourceMap || snapshot.GraphMapRevision == nil {
		return snapshot, err
	}
	var metadata struct {
		Canonical bool `json:"canonical"`
	}
	if err = json.Unmarshal(snapshot.Graph, &metadata); err != nil || !metadata.Canonical {
		return snapshot, err
	}
	graph, err := db.New(r.pool).ReadPublishedCourseGraph(ctx, subjectID)
	if err != nil {
		return coursegraph.Snapshot{}, err
	}
	snapshot.Graph = graph
	return snapshot, nil
}

func (r *pairedCourseGraphRepository) Finish(ctx context.Context, job coursegraph.Job, graph json.RawMessage, now time.Time) (bool, error) {
	if job.Source != coursegraph.SourceMap {
		return r.Repository.Finish(ctx, job, graph, now)
	}
	var metadata struct {
		Canonical        bool  `json:"canonical"`
		LearningRevision int64 `json:"learning_revision"`
		ExamRevision     int64 `json:"exam_revision"`
	}
	if err := json.Unmarshal(graph, &metadata); err != nil || !metadata.Canonical || metadata.LearningRevision <= 0 || metadata.ExamRevision <= 0 {
		return r.Repository.Finish(ctx, job, graph, now)
	}
	var prepared struct {
		Topics []struct {
			ID       string `json:"id"`
			Variants []struct {
				Rephrase string `json:"rephrase"`
			} `json:"variants"`
		} `json:"topics"`
	}
	if err := json.Unmarshal(graph, &prepared); err != nil {
		return false, err
	}
	pointer, err := canonicalMapMetadata(graph, metadata.LearningRevision, metadata.ExamRevision)
	if err != nil {
		return false, err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := db.New(tx)
	for _, topic := range prepared.Topics {
		for position, task := range topic.Variants {
			updated, err := queries.CourseGraphUpdateExamTaskRephrase(ctx, db.CourseGraphUpdateExamTaskRephraseParams{
				ExamRevision: metadata.ExamRevision, OutcomeID: topic.ID, Position: int32(position + 1), Rephrase: task.Rephrase,
			})
			if err != nil {
				return false, err
			}
			if updated != 1 {
				return false, fmt.Errorf("Geek prepared task does not resolve to the canonical course graph")
			}
		}
	}
	if _, err = queries.FinishCourseGraph(ctx, db.FinishCourseGraphParams{
		SubjectID: job.SubjectID, Fingerprint: job.Fingerprint, Token: job.Token, Graph: pointer, Now: now.Unix(),
	}); err != nil {
		if err == pgx.ErrNoRows {
			return false, tx.Rollback(ctx)
		}
		return false, err
	}
	return true, tx.Commit(ctx)
}

func (r *pairedCourseGraphRepository) SaveMapIfCurrent(ctx context.Context, subjectID, filename, sheet string, graph json.RawMessage, expectedFingerprint string, now time.Time) (bool, error) {
	var metadata struct {
		Canonical        bool  `json:"canonical"`
		LearningRevision int64 `json:"learning_revision"`
		ExamRevision     int64 `json:"exam_revision"`
	}
	if err := json.Unmarshal(graph, &metadata); err != nil || !metadata.Canonical || metadata.LearningRevision <= 0 || metadata.ExamRevision <= 0 {
		return r.Repository.SaveMapIfCurrent(ctx, subjectID, filename, sheet, graph, expectedFingerprint, now)
	}
	projection, err := graphpg.CanonicalizeMapProjection(ctx, r.pool, graph, metadata.LearningRevision)
	if err != nil {
		return false, err
	}
	return r.Repository.SaveCanonicalMapIfCurrent(ctx, subjectID, filename, sheet, projection,
		metadata.LearningRevision, expectedFingerprint, now)
}

func (r *pairedCourseGraphRepository) SaveMapWithLearning(ctx context.Context, subjectID, filename, sheet string, graph json.RawMessage, now time.Time, actorID string, expectedRevision int64, learningMap *competencymap.Map) (bool, int64, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var revision int64
	if learningMap != nil {
		result, err := r.learning.ReplaceInTx(ctx, tx, subjectID, actorID, *learningMap, now.Unix(), expectedRevision)
		if err != nil {
			return false, 0, err
		}
		revision = result.Revision
		examRevision, err := db.New(tx).CourseGraphCreateExamVersion(ctx, db.CourseGraphCreateExamVersionParams{
			MapRevision: revision, CreatedAt: now.Unix(),
		})
		if err != nil {
			return false, 0, err
		}
		graph, err = graphpg.CanonicalizeMapProjection(ctx, tx, graph, revision)
		if err != nil {
			return false, 0, err
		}
		if err = saveCanonicalMapData(ctx, tx, graph, revision, examRevision); err != nil {
			return false, 0, err
		}
		graph, err = canonicalMapMetadata(graph, revision, examRevision)
		if err != nil {
			return false, 0, err
		}
	}
	if err = r.Repository.SaveMapInTx(ctx, tx, subjectID, filename, sheet, graph, now); err != nil {
		if err == pgx.ErrNoRows {
			return false, 0, nil
		}
		return false, 0, err
	}
	if err = tx.Commit(ctx); err != nil {
		return false, 0, err
	}
	return true, revision, nil
}

func saveCanonicalMapData(ctx context.Context, tx pgx.Tx, graph json.RawMessage, revision, examRevision int64) error {
	var projection struct {
		Topics []struct {
			ID       string `json:"id"`
			Role     string `json:"role"`
			Variants []struct {
				Question           string `json:"question"`
				Content            string `json:"content"`
				AssessmentGuidance string `json:"assessment_guidance"`
			} `json:"variants"`
		} `json:"topics"`
		Prerequisites []struct {
			Source string `json:"source"`
			Target string `json:"target"`
			Reason string `json:"reason"`
		} `json:"prerequisites"`
	}
	if err := json.Unmarshal(graph, &projection); err != nil {
		return err
	}
	queries := db.New(tx)
	for topicIndex, topic := range projection.Topics {
		if topic.ID == "" || len(topic.Variants) == 0 {
			return fmt.Errorf("canonical Geek topic is missing its outcome ID or tasks")
		}
		if err := queries.CourseGraphInsertExamTopic(ctx, db.CourseGraphInsertExamTopicParams{
			MapRevision: revision, ExamRevision: examRevision, OutcomeID: topic.ID, TopicOrder: int32(topicIndex + 1), Role: topic.Role,
		}); err != nil {
			return err
		}
		for taskIndex, task := range topic.Variants {
			if err := queries.CourseGraphInsertExamTask(ctx, db.CourseGraphInsertExamTaskParams{
				MapRevision: revision, ExamRevision: examRevision, OutcomeID: topic.ID, Position: int32(taskIndex + 1), Question: task.Question,
				Content: task.Content, AssessmentGuidance: task.AssessmentGuidance,
			}); err != nil {
				return err
			}
		}
	}
	for _, edge := range projection.Prerequisites {
		if err := queries.CourseGraphInsertPrerequisite(ctx, db.CourseGraphInsertPrerequisiteParams{
			MapRevision: revision, SourceOutcomeID: edge.Source, TargetOutcomeID: edge.Target, Reason: edge.Reason,
		}); err != nil {
			return err
		}
	}
	return nil
}

func canonicalMapMetadata(graph json.RawMessage, revision, examRevision int64) (json.RawMessage, error) {
	var data map[string]json.RawMessage
	if err := json.Unmarshal(graph, &data); err != nil {
		return nil, err
	}
	for _, key := range []string{"topics", "prerequisites", "hierarchy"} {
		delete(data, key)
	}
	data["canonical"] = json.RawMessage("true")
	data["learning_revision"], _ = json.Marshal(revision)
	data["exam_revision"], _ = json.Marshal(examRevision)
	return json.Marshal(data)
}

var _ interface {
	SaveMapWithLearning(context.Context, string, string, string, json.RawMessage, time.Time, string, int64, *competencymap.Map) (bool, int64, error)
} = (*pairedCourseGraphRepository)(nil)
