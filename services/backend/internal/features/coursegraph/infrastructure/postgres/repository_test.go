package postgres

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/competencymap"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/coursegraph"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/user"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/features/coursegraph/application"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/shared/fault"
	shared "github.com/AskarKasimov/ai-tutor/services/backend/internal/shared/postgres"
	db "github.com/AskarKasimov/ai-tutor/services/backend/internal/shared/postgres/sqlcgen"
)

// repositoryPool has course s with material m: OCR done, cleaning pending.
func repositoryPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL integration tests")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	suffix := make([]byte, 8)
	_, _ = rand.Read(suffix)
	schema := "coursegraph_test_" + hex.EncodeToString(suffix)
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Close(); _, _ = admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE"); admin.Close() })
	if err := shared.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	exec(t, pool,
		`INSERT INTO subjects(id, name, created_at) VALUES ('s', 'Курс', 1)`,
		`INSERT INTO subject_materials(id, subject_id, filename, content_type, s3_raw_key, s3_parsed_key, status, created_at, updated_at, week, kind)
		 VALUES ('m', 's', 'week1.md', 'text/markdown', 'raw', 'parsed', 'parsed', 1, 1, 1, 'lecture')`,
		`INSERT INTO material_cleaning_jobs(material_id, status, updated_at) VALUES ('m', 'pending', 1)`)
	return pool
}

func exec(t *testing.T, pool *pgxpool.Pool, queries ...string) {
	t.Helper()
	for _, q := range queries {
		if _, err := pool.Exec(context.Background(), q); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBuildStartsOnlyOnRequestOfReadyMaterials(t *testing.T) {
	pool := repositoryPool(t)
	repo := New(pool)
	ctx, now := context.Background(), time.Unix(100, 0)

	snapshot, err := repo.Read(ctx, "s")
	if err != nil || snapshot.Status != "not_requested" || snapshot.InputsReady {
		t.Fatalf("before: %+v %v", snapshot, err)
	}
	if queued, err := repo.Request(ctx, "s", now); err != nil || queued {
		t.Fatalf("request with unprocessed material: %v %v", queued, err)
	}

	exec(t, pool, `UPDATE material_cleaning_jobs SET status='ready', s3_report_key='r1'`)
	// Ready materials alone do not start a build: that is the button.
	if job, err := repo.Claim(ctx, "w1", now); err != nil || job.SubjectID != "" {
		t.Fatalf("claim without request: %+v %v", job, err)
	}
	if queued, err := repo.Request(ctx, "s", now); err != nil || !queued {
		t.Fatalf("request: %v %v", queued, err)
	}
	if queued, _ := repo.Request(ctx, "s", now); queued {
		t.Fatal("second request while the build is queued restarted it")
	}

	// A material changed after the request: the build takes the current set.
	exec(t, pool, `UPDATE material_cleaning_jobs SET s3_report_key='r2'`)
	job, err := repo.Claim(ctx, "w1", now)
	if err != nil || job.SubjectID != "s" {
		t.Fatalf("claim: %+v %v", job, err)
	}
	accepted, err := repo.Finish(ctx, job, json.RawMessage(`{"version":"course-graph-v1","topics":[]}`), now)
	if err != nil || !accepted {
		t.Fatalf("finish with current inputs: %v %v", accepted, err)
	}
	snapshot, err = repo.Read(ctx, "s")
	if err != nil || snapshot.Status != "ready" || snapshot.Stale || !snapshot.InputsReady {
		t.Fatalf("after build: %+v %v", snapshot, err)
	}
}

func TestBuildChangedDuringRunIsNotPublished(t *testing.T) {
	pool := repositoryPool(t)
	repo := New(pool)
	ctx, now := context.Background(), time.Unix(100, 0)
	exec(t, pool, `UPDATE material_cleaning_jobs SET status='ready', s3_report_key='r1'`)
	if queued, err := repo.Request(ctx, "s", now); err != nil || !queued {
		t.Fatalf("request: %v %v", queued, err)
	}
	job, err := repo.Claim(ctx, "w1", now)
	if err != nil || job.SubjectID != "s" {
		t.Fatalf("claim: %+v %v", job, err)
	}
	exec(t, pool, `UPDATE material_cleaning_jobs SET s3_report_key='r2'`, `UPDATE subject_materials SET week=2 WHERE id='m'`)
	if accepted, err := repo.Finish(ctx, job, json.RawMessage(`{}`), now); err != nil || accepted {
		t.Fatalf("finish with stale inputs: %v %v", accepted, err)
	}
	if err := repo.Release(ctx, job, now); err != nil {
		t.Fatal(err)
	}
	again, err := repo.Claim(ctx, "w2", now)
	if err != nil || again.SubjectID != "s" || again.Fingerprint == job.Fingerprint || again.Attempts != job.Attempts {
		t.Fatalf("reclaim: %+v (was %+v) %v", again, job, err)
	}
}

func TestInputsAreNotReadyUntilMaterialMetadataIsComplete(t *testing.T) {
	pool := repositoryPool(t)
	ctx := context.Background()
	exec(t, pool, `UPDATE material_cleaning_jobs SET status='ready', s3_report_key='r1'`,
		`UPDATE subject_materials SET week=NULL WHERE id='m'`)
	snapshot, err := New(pool).Read(ctx, "s")
	if err != nil || snapshot.InputsReady {
		t.Fatalf("snapshot = %+v err=%v", snapshot, err)
	}
	if queued, err := New(pool).Request(ctx, "s", time.Unix(100, 0)); err != nil || queued {
		t.Fatalf("queued incomplete material: %v err=%v", queued, err)
	}
}

func TestPublishedListsTheGraphsGeekExaminesBy(t *testing.T) {
	pool := repositoryPool(t)
	exec(t, pool,
		`INSERT INTO subjects(id, name, created_at) VALUES ('empty', 'Без материалов', 1)`,
		`INSERT INTO course_graphs(subject_id, fingerprint, graph, updated_at) VALUES ('s', 'f', '{"topics":[{"question":"Почему?"}]}', 2)`,
		`INSERT INTO course_graphs(subject_id, fingerprint, graph, updated_at) VALUES ('empty', 'f', '{"topics":[{"question":"Лишний?"}]}', 3)`)
	graphs, err := New(pool).Published(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// A graph of materials is not examined once the course has none.
	if len(graphs) != 1 {
		t.Fatalf("published graphs = %d", len(graphs))
	}
	var g struct{ Topics []struct{ Question string } }
	if graphs[0].SubjectID != "s" || json.Unmarshal(graphs[0].Graph, &g) != nil || len(g.Topics) != 1 || g.Topics[0].Question != "Почему?" {
		t.Fatalf("graph = %+v", graphs[0])
	}
}

func TestAMapIsBuiltWithoutMaterialsAndReplacesTheGraph(t *testing.T) {
	pool := repositoryPool(t)
	repo := New(pool)
	ctx, now := context.Background(), time.Unix(100, 0)
	graph := json.RawMessage(`{"version":"competency-map-v1","source":"competency_map","topics":[{"id":"a","title":"ОР"}]}`)
	// The materials of s are still being cleaned: a map does not wait for them.
	if queued, err := saveTestMap(ctx, repo, "s", "map.xlsx", "Карта", graph, now); err != nil || !queued {
		t.Fatalf("save map: %v %v", queued, err)
	}
	if queued, err := saveTestMap(ctx, repo, "s", "other.xlsx", "Карта", graph, now); err != nil || queued {
		t.Fatalf("a second map during the build: %v %v", queued, err)
	}
	job, err := repo.Claim(ctx, "w1", now)
	if err != nil || job.SubjectID != "s" || job.Source != "competency_map" {
		t.Fatalf("claim: %+v %v", job, err)
	}
	stored, err := repo.MapGraph(ctx, "s")
	// jsonb keeps the content, not the key order.
	if err != nil || !strings.Contains(string(stored), `"version": "competency-map-v1"`) {
		t.Fatalf("map graph: %s %v", stored, err)
	}
	if accepted, err := repo.Finish(ctx, job, graph, now); err != nil || !accepted {
		t.Fatalf("finish: %v %v", accepted, err)
	}
	snapshot, err := repo.Read(ctx, "s")
	if err != nil || snapshot.GraphSource != "competency_map" || snapshot.Stale || snapshot.Map == nil ||
		snapshot.Map.Filename != "map.xlsx" || snapshot.BuildSource == nil || *snapshot.BuildSource != "competency_map" {
		t.Fatalf("snapshot = %+v %v", snapshot, err)
	}
	// Geek reads a graph of a map after the last material is gone.
	exec(t, pool, `DELETE FROM subject_materials`)
	published, err := repo.Published(ctx)
	if err != nil || len(published) != 1 {
		t.Fatalf("published: %d %v", len(published), err)
	}
}

func TestConfirmedMapReplacementChecksPublicationAndConcurrentImport(t *testing.T) {
	for _, scenario := range []string{"current", "stale failed replacement", "published replacement", "concurrent import"} {
		t.Run(scenario, func(t *testing.T) {
			pool := repositoryPool(t)
			repo := New(pool)
			ctx, now := context.Background(), time.Unix(100, 0)
			graphA := json.RawMessage(`{"topics":[{"id":"a"}]}`)
			graphB := json.RawMessage(`{"topics":[{"id":"b"}]}`)
			excluded := json.RawMessage(`{"topics":[{"id":"kept"}],"assessment_skipped":1}`)
			if ok, err := saveTestMap(ctx, repo, "s", "a.xlsx", "sheet", graphA, now); err != nil || !ok {
				t.Fatalf("save A: %v %v", ok, err)
			}
			job, err := repo.Claim(ctx, "a", now)
			if err != nil {
				t.Fatal(err)
			}
			if ok, err := repo.Finish(ctx, job, graphA, now); err != nil || !ok {
				t.Fatalf("publish A: %v %v", ok, err)
			}
			snapshot, err := repo.Read(ctx, "s")
			if err != nil || snapshot.Map == nil || snapshot.Map.Fingerprint == "" {
				t.Fatalf("missing fingerprint: %+v %v", snapshot, err)
			}
			confirmed := snapshot.Map.Fingerprint
			if scenario == "stale failed replacement" || scenario == "published replacement" {
				if ok, err := saveTestMap(ctx, repo, "s", "b.xlsx", "sheet", graphB, now); err != nil || !ok {
					t.Fatalf("save B: %v %v", ok, err)
				}
				job, err = repo.Claim(ctx, "b", now)
				if err != nil {
					t.Fatal(err)
				}
				if scenario == "published replacement" {
					if ok, err := repo.Finish(ctx, job, graphB, now); err != nil || !ok {
						t.Fatalf("publish B: %v %v", ok, err)
					}
				} else {
					exec(t, pool, `UPDATE course_graph_jobs SET status='failed' WHERE subject_id='s'`)
					// Even a token read from the latest imported B cannot authorize changes
					// to B while the admin sees published A.
					snapshot, err = repo.Read(ctx, "s")
					if err != nil || !snapshot.Stale {
						t.Fatalf("expected stale A: %+v %v", snapshot, err)
					}
					confirmed = snapshot.Map.Fingerprint
				}
			}
			var ok bool
			if scenario == "concurrent import" {
				tx, beginErr := pool.Begin(ctx)
				if beginErr != nil {
					t.Fatal(beginErr)
				}
				defer tx.Rollback(ctx)
				// Hold the map row while another transaction attempts its guarded write.
				if _, err := tx.Exec(ctx, `UPDATE course_graph_maps SET fingerprint='b', graph=$1::jsonb, filename='b.xlsx' WHERE subject_id='s'`, graphB); err != nil {
					t.Fatal(err)
				}
				done := make(chan struct{})
				go func() {
					ok, err = repo.SaveMapIfCurrent(ctx, "s", "a.xlsx", "sheet", excluded, confirmed, now)
					close(done)
				}()
				select {
				case <-done:
					t.Fatalf("write did not wait for concurrent import: %v %v", ok, err)
				case <-time.After(100 * time.Millisecond):
				}
				if commitErr := tx.Commit(ctx); commitErr != nil {
					t.Fatal(commitErr)
				}
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Fatal("replacement blocked")
				}
			} else {
				ok, err = repo.SaveMapIfCurrent(ctx, "s", "a.xlsx", "sheet", excluded, confirmed, now)
			}
			if scenario == "current" {
				if err != nil || !ok {
					t.Fatalf("current map rejected: %v %v", ok, err)
				}
			} else {
				var f *fault.Error
				if ok || !errors.As(err, &f) || f.Kind != fault.Conflict || f.Code != "COMPETENCY_MAP_CHANGED" {
					t.Fatalf("expected 409: %v %v", ok, err)
				}
			}
			stored, err := repo.MapGraph(ctx, "s")
			if err != nil {
				t.Fatal(err)
			}
			expected := `"id": "b"`
			if scenario == "current" {
				expected = `"id": "kept"`
			}
			if !strings.Contains(string(stored), expected) {
				t.Fatalf("unexpected stored map: %s", stored)
			}
		})
	}
}

type retryQuestions struct {
	fail bool
	seen json.RawMessage
}

func (q *retryQuestions) Prepare(_ context.Context, graph json.RawMessage) (json.RawMessage, error) {
	q.seen = append(json.RawMessage(nil), graph...)
	if q.fail {
		return nil, errors.New("gateway unavailable")
	}
	return graph, nil
}

func TestFailedFilteredMapCanBePreparedAgainWithoutRestoringExcludedTasks(t *testing.T) {
	pool := repositoryPool(t)
	exec(t, pool, `DELETE FROM subject_materials`)
	repo := New(pool)
	ctx := context.Background()
	fallback, _ := json.Marshal(coursegraph.DefaultAssessmentGuidance)
	graphA := json.RawMessage(`{"version":"competency-map-v1","topics":[{"id":"a","title":"Тема","variants":[{"question":"skip","content":"","assessment_guidance":` + string(fallback) + `},{"question":"keep","content":"Эталон","assessment_guidance":"author"}]},{"id":"b","title":"Тема Б","variants":[{"question":"b","content":"Эталон","assessment_guidance":"author"}]},{"id":"c","title":"Тема В","variants":[{"question":"c","content":"Эталон","assessment_guidance":"author"}]}],"prerequisites":[],"hierarchy":[]}`)
	if ok, err := saveTestMap(ctx, repo, "s", "map.xlsx", "Карта", graphA, time.Now()); err != nil || !ok {
		t.Fatalf("save A: %v %v", ok, err)
	}
	questions := &retryQuestions{}
	worker := application.New(importlessRepo{repo}, nil, nil, nil, nil, nil)
	worker.SetQuestions(questions)
	if ok, err := worker.ProcessOne(ctx); err != nil || !ok {
		t.Fatalf("publish A: %v %v", ok, err)
	}
	a, err := repo.Read(ctx, "s")
	if err != nil {
		t.Fatal(err)
	}
	admin := user.User{Role: user.Admin}
	if _, err := worker.SkipMissingCriteria(ctx, admin, "s", a.Map.Fingerprint); err != nil {
		t.Fatal(err)
	}
	savedB, err := repo.MapGraph(ctx, "s")
	if err != nil {
		t.Fatal(err)
	}
	questions.fail = true
	for attempt := 0; attempt < 3; attempt++ {
		exec(t, pool, `UPDATE course_graph_jobs SET available_at=0`)
		if ok, err := worker.ProcessOne(ctx); err == nil || !ok {
			t.Fatalf("expected preparation failure: %v %v", ok, err)
		}
	}
	failed, err := repo.Read(ctx, "s")
	if err != nil || failed.Status != "failed" || !failed.Stale || failed.Attempts != 3 || string(failed.Graph) != string(a.Graph) {
		t.Fatalf("expected failed B and published A: %+v %v", failed, err)
	}
	retry, err := worker.RetryMap(ctx, admin, "s", failed.Map.Fingerprint)
	if err != nil || retry.Status != "pending" || retry.Attempts != 0 {
		t.Fatalf("retry: %+v %v", retry, err)
	}
	questions.fail = false
	if ok, err := worker.ProcessOne(ctx); err != nil || !ok {
		t.Fatalf("publish B: %v %v", ok, err)
	}
	published, err := repo.Read(ctx, "s")
	if err != nil || published.Status != "ready" || published.Stale || string(published.Graph) != string(savedB) || string(questions.seen) != string(savedB) {
		t.Fatalf("expected prepared B: %+v %v", published, err)
	}
	var shape struct {
		Skipped int `json:"assessment_skipped"`
		Topics  []struct{ Variants []struct{ Question string } }
	}
	if err := json.Unmarshal(published.Graph, &shape); err != nil {
		t.Fatal(err)
	}
	if shape.Skipped != 1 || len(shape.Topics) != 3 || len(shape.Topics[0].Variants) != 1 || shape.Topics[0].Variants[0].Question != "keep" {
		t.Fatalf("excluded task restored: %s", published.Graph)
	}
	if ok, err := repo.RetryMap(ctx, "s", published.Map.Fingerprint, time.Now()); err != nil || ok {
		t.Fatalf("ready map restarted: %v %v", ok, err)
	}
}

func TestMapRetryRejectsImportThatCommitsWhileWaitingForMapLock(t *testing.T) {
	pool := repositoryPool(t)
	repo := New(pool)
	ctx := context.Background()
	if ok, err := saveTestMap(ctx, repo, "s", "a.xlsx", "Карта", json.RawMessage(`{"topics":[]}`), time.Now()); err != nil || !ok {
		t.Fatalf("save A: %v %v", ok, err)
	}
	exec(t, pool, `UPDATE course_graph_jobs SET status='failed',attempts=3,error_message='failure'`)
	a, err := repo.Read(ctx, "s")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `UPDATE course_graph_maps SET fingerprint='b',filename='b.xlsx' WHERE subject_id='s'`); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var ok bool
	var retryErr error
	go func() { ok, retryErr = repo.RetryMap(ctx, "s", a.Map.Fingerprint, time.Now()); close(done) }()
	select {
	case <-done:
		t.Fatal("retry did not wait for map lock")
	case <-time.After(100 * time.Millisecond):
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("retry blocked")
	}
	var f *fault.Error
	if ok || !errors.As(retryErr, &f) || f.Code != "COMPETENCY_MAP_CHANGED" {
		t.Fatalf("expected conflict: %v %v", ok, retryErr)
	}
	b, err := repo.Read(ctx, "s")
	if err != nil || b.Status != "failed" || b.Attempts != 3 || b.ErrorMessage != "failure" || b.Map.Fingerprint != "b" {
		t.Fatalf("retry changed state: %+v %v", b, err)
	}
}

// saveTestMap stores a map of no map revision and queues its build, as an
// import does for the Geek sheet.
func saveTestMap(ctx context.Context, repo *Repository, subjectID, filename, sheet string, graph json.RawMessage, now time.Time) (bool, error) {
	tx, err := repo.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	sum := sha256.Sum256(graph)
	q := db.New(tx)
	if err := q.SaveCourseGraphMap(ctx, db.SaveCourseGraphMapParams{SubjectID: subjectID, Filename: filename, Sheet: sheet,
		Fingerprint: hex.EncodeToString(sum[:]), Graph: graph, Now: now.Unix()}); err != nil {
		return false, err
	}
	if _, err := q.RequestCourseGraphMapBuild(ctx, db.RequestCourseGraphMapBuildParams{SubjectID: subjectID, Now: now.Unix()}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return true, tx.Commit(ctx)
}

// importlessRepo is the repository of a worker that only prepares saved maps.
type importlessRepo struct{ *Repository }

func (importlessRepo) SaveMapWithLearning(context.Context, string, string, string, json.RawMessage, time.Time, string, int64, competencymap.Map) (bool, int64, error) {
	return false, 0, errors.New("imports are not part of this test")
}
