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

// pairedCourseGraphRepository saves both sheets of a competency map in one
// transaction: the learning sheet as the course structure and the diagnostic
// tasks of a new map revision, the Geek sheet as the exam tasks of the same
// outcomes with the build of the Geek graph queued. The learning revision is
// active at once; Geek keeps its published graph until the build finishes.
type pairedCourseGraphRepository struct {
	*graphpg.Repository
	pool     *pgxpool.Pool
	learning *competencypg.Repository
}

func (r *pairedCourseGraphRepository) SaveMapWithLearning(ctx context.Context, subjectID, filename, sheet string, graph json.RawMessage, now time.Time, actorID string, expectedRevision int64, learningMap competencymap.Map) (bool, int64, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	result, err := r.learning.ReplaceInTx(ctx, tx, subjectID, actorID, learningMap, now.Unix(), expectedRevision)
	if err != nil {
		return false, 0, err
	}
	if err = graphpg.SaveImportedMapInTx(ctx, tx, subjectID, filename, sheet, graph, result.Revision, now); err != nil {
		if err == pgx.ErrNoRows {
			return false, 0, nil
		}
		return false, 0, err
	}
	if err = tx.Commit(ctx); err != nil {
		return false, 0, err
	}
	return true, result.Revision, nil
}
