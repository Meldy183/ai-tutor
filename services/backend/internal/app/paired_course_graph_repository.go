package app

import (
	"context"
	"encoding/json"
	"time"

	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/competencymap"
	competencypg "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/competency/infrastructure/postgres"
	graphpg "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/coursegraph/infrastructure/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type pairedCourseGraphRepository struct {
	*graphpg.Repository
	pool     *pgxpool.Pool
	learning *competencypg.Repository
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
		var payload map[string]json.RawMessage
		if err = json.Unmarshal(graph, &payload); err != nil {
			return false, 0, err
		}
		payload["learning_revision"], _ = json.Marshal(revision)
		graph, err = json.Marshal(payload)
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

var _ interface {
	SaveMapWithLearning(context.Context, string, string, string, json.RawMessage, time.Time, string, int64, *competencymap.Map) (bool, int64, error)
} = (*pairedCourseGraphRepository)(nil)
