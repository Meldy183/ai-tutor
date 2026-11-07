package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/competencymap"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/coursegraph"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/features/coursegraph/application"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/shared/fault"
	db "github.com/AskarKasimov/ai-tutor/services/backend/internal/shared/postgres/sqlcgen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Repository { return &Repository{pool} }

func (r *Repository) Claim(ctx context.Context, token string, now time.Time) (coursegraph.Job, error) {
	job, err := db.New(r.pool).ClaimCourseGraph(ctx, db.ClaimCourseGraphParams{Token: token, Now: now.Unix()})
	if errors.Is(err, pgx.ErrNoRows) {
		return coursegraph.Job{}, nil
	}
	return coursegraph.Job{SubjectID: job.SubjectID, Fingerprint: job.Fingerprint, Token: job.LeaseToken, Source: job.Source, Attempts: int(job.Attempts)}, err
}

func (r *Repository) Renew(ctx context.Context, job coursegraph.Job, now time.Time) (bool, error) {
	n, err := db.New(r.pool).RenewCourseGraph(ctx, db.RenewCourseGraphParams{SubjectID: job.SubjectID, Token: job.Token, Now: now.Unix()})
	return n == 1, err
}

func (r *Repository) Release(ctx context.Context, job coursegraph.Job, now time.Time) error {
	_, err := db.New(r.pool).ReleaseCourseGraph(ctx, db.ReleaseCourseGraphParams{SubjectID: job.SubjectID, Token: job.Token, Now: now.Unix()})
	return err
}
func (r *Repository) Finish(ctx context.Context, job coursegraph.Job, graph json.RawMessage, now time.Time) (bool, error) {
	_, err := db.New(r.pool).FinishCourseGraph(ctx, db.FinishCourseGraphParams{SubjectID: job.SubjectID, Fingerprint: job.Fingerprint, Token: job.Token, Graph: graph, Now: now.Unix()})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}
func (r *Repository) Fail(ctx context.Context, job coursegraph.Job, message string, now time.Time) error {
	return db.New(r.pool).FailCourseGraph(ctx, db.FailCourseGraphParams{SubjectID: job.SubjectID, Token: job.Token, ErrorMessage: message, Now: now.Unix()})
}
func (r *Repository) Read(ctx context.Context, subjectID string) (coursegraph.Snapshot, error) {
	row, err := db.New(r.pool).ReadCourseGraph(ctx, subjectID)
	if errors.Is(err, pgx.ErrNoRows) {
		return coursegraph.Snapshot{}, fault.New(fault.NotFound, "SUBJECT_NOT_FOUND", "Предмет не найден.")
	}
	if err != nil {
		return coursegraph.Snapshot{}, err
	}
	snapshot := coursegraph.Snapshot{Status: row.Status, Attempts: row.Attempts, ErrorMessage: row.ErrorMessage, Graph: row.Graph,
		GraphSource: row.GraphSource, GraphFingerprint: row.GraphFingerprint, GraphMapRevision: row.GraphMapRevision,
		UpdatedAt: row.UpdatedAt, Stale: row.Stale, InputsReady: row.InputsReady}
	if row.JobSource != "" {
		snapshot.BuildSource = &row.JobSource
	}
	if row.MapFilename != "" {
		snapshot.Map = &coursegraph.MapFile{Filename: row.MapFilename, Sheet: row.MapSheet,
			Fingerprint: row.MapFingerprint, LearningRevision: row.MapLearningRevision}
	}
	return snapshot, nil
}

// SaveMap stores an imported competency map and queues a build of it in one
// transaction; false, with nothing stored, when a build is already queued or
// running.
func (r *Repository) SaveMap(ctx context.Context, subjectID, filename, sheet string, graph json.RawMessage, now time.Time) (bool, error) {
	return r.saveMap(ctx, subjectID, filename, sheet, graph, "", now)
}

func (r *Repository) SaveMapWithLearning(ctx context.Context, subjectID, filename, sheet string, graph json.RawMessage, now time.Time, _ string, _ int64, learningMap *competencymap.Map) (bool, int64, error) {
	// The composition adapter handles paired imports. This repository alone is
	// sufficient only when the workbook has no learning sheet.
	if learningMap != nil {
		return false, 0, fault.New(fault.Unavailable, "COMPETENCY_MAP_UNAVAILABLE", "Совместное сохранение учебной карты не настроено.")
	}
	queued, err := r.SaveMap(ctx, subjectID, filename, sheet, graph, now)
	return queued, 0, err
}

func (r *Repository) SaveMapInTx(ctx context.Context, tx pgx.Tx, subjectID, filename, sheet string, graph json.RawMessage, now time.Time) error {
	return persistMapTx(ctx, tx, subjectID, filename, sheet, graph, now)
}

// SaveMapIfCurrent atomically replaces only the confirmed, published map.
func (r *Repository) SaveMapIfCurrent(ctx context.Context, subjectID, filename, sheet string, graph json.RawMessage, expectedFingerprint string, now time.Time) (bool, error) {
	if expectedFingerprint == "" {
		return false, mapChanged()
	}
	return r.saveMap(ctx, subjectID, filename, sheet, graph, expectedFingerprint, now)
}

func mapChanged() error {
	return fault.New(fault.Conflict, "COMPETENCY_MAP_CHANGED", "Карта изменилась или ещё не опубликована. Обновите граф и подтвердите исключение заново.")
}

func (r *Repository) saveMap(ctx context.Context, subjectID, filename, sheet string, graph json.RawMessage, expectedFingerprint string, now time.Time) (bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	sum := sha256.Sum256(graph)
	q := db.New(tx)
	if expectedFingerprint != "" {
		n, err := q.ReplaceCurrentCourseGraphMap(ctx, db.ReplaceCurrentCourseGraphMapParams{
			SubjectID: subjectID, Filename: filename, Sheet: sheet, Graph: graph,
			Fingerprint: hex.EncodeToString(sum[:]), ExpectedFingerprint: expectedFingerprint, Now: now.Unix(),
		})
		if err != nil {
			return false, err
		}
		if n != 1 {
			return false, mapChanged()
		}
	} else if err = q.SaveCourseGraphMap(ctx, db.SaveCourseGraphMapParams{SubjectID: subjectID, Filename: filename, Sheet: sheet,
		Fingerprint: hex.EncodeToString(sum[:]), Graph: graph, Now: now.Unix()}); err != nil {
		return false, err
	}
	_, err = q.RequestCourseGraphMapBuild(ctx, db.RequestCourseGraphMapBuildParams{SubjectID: subjectID, Now: now.Unix()})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

func persistMapTx(ctx context.Context, tx pgx.Tx, subjectID, filename, sheet string, graph json.RawMessage, now time.Time) error {
	q := db.New(tx)
	sum := sha256.Sum256(graph)
	if err := q.SaveCourseGraphMap(ctx, db.SaveCourseGraphMapParams{SubjectID: subjectID, Filename: filename, Sheet: sheet, Fingerprint: hex.EncodeToString(sum[:]), Graph: graph, Now: now.Unix()}); err != nil {
		return err
	}
	_, err := q.RequestCourseGraphMapBuild(ctx, db.RequestCourseGraphMapBuildParams{SubjectID: subjectID, Now: now.Unix()})
	return err
}

// MapGraph is the graph of the imported competency map of a course.
func (r *Repository) MapGraph(ctx context.Context, subjectID string) (json.RawMessage, error) {
	graph, err := db.New(r.pool).ReadCourseGraphMap(ctx, subjectID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("course %s has no competency map", subjectID)
	}
	return graph, err
}

// Published lists the graphs Geek examines by, newest first.
func (r *Repository) Published(ctx context.Context) ([]application.PublishedGraph, error) {
	rows, err := db.New(r.pool).ListPublishedCourseGraphsForAudio(ctx)
	if err != nil {
		return nil, fmt.Errorf("list published course graphs: %w", err)
	}
	graphs := make([]application.PublishedGraph, len(rows))
	for i, row := range rows {
		graphs[i] = application.PublishedGraph{SubjectID: row.SubjectID, Graph: row.Graph}
	}
	return graphs, nil
}

// Request queues a build of the current materials; false when they are not
// ready or a build is already queued or running.
func (r *Repository) Request(ctx context.Context, subjectID string, now time.Time) (bool, error) {
	_, err := db.New(r.pool).RequestCourseGraphBuild(ctx, db.RequestCourseGraphBuildParams{SubjectID: subjectID, Now: now.Unix()})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// RetryMap holds the map lock until the failed build has been requeued, so a
// concurrent import cannot change the source between confirmation and retry.
func (r *Repository) RetryMap(ctx context.Context, subjectID, expectedFingerprint string, now time.Time) (bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)
	fingerprint, err := q.LockCourseGraphMapFingerprint(ctx, subjectID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (expectedFingerprint == "" || fingerprint != expectedFingerprint)) {
		return false, fault.New(fault.Conflict, "COMPETENCY_MAP_CHANGED", "Карта изменилась. Обновите страницу и повторите подготовку.")
	}
	if err != nil {
		return false, err
	}
	n, err := q.RetryFailedCourseGraphMapBuild(ctx, db.RetryFailedCourseGraphMapBuildParams{SubjectID: subjectID, ExpectedFingerprint: expectedFingerprint, Now: now.Unix()})
	if err != nil || n != 1 {
		return false, err
	}
	return true, tx.Commit(ctx)
}
