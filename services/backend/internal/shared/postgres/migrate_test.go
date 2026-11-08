package postgres

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

func TestEmbeddedMigrationsInitializeWithoutDatabase(t *testing.T) {
	files, err := fs.Sub(migrations, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	// Provider initialization checks migration versions without connecting to PostgreSQL.
	db, err := sql.Open("pgx", "postgres://localhost/unused")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := goose.NewProvider(goose.DialectPostgres, db, files,
		goose.WithDisableGlobalRegistry(true)); err != nil {
		t.Fatalf("initialize embedded migrations: %v", err)
	}
}

func migrationPool(t *testing.T) *pgxpool.Pool {
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
	if _, err := rand.Read(suffix); err != nil {
		t.Fatal(err)
	}
	schema := "migration_test_" + hex.EncodeToString(suffix)
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
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
	})
	return pool
}

func assertGooseVersion(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	var count int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM goose_db_version WHERE version_id=1 AND is_applied").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("initial migration recorded %d times, want 1", count)
	}
}

func TestMigrateFreshAndRepeat(t *testing.T) {
	pool := migrationPool(t)
	ctx := context.Background()
	for range 2 {
		if err := Migrate(ctx, pool); err != nil {
			t.Fatal(err)
		}
	}
	assertGooseVersion(t, pool)
	var subjects int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM subjects").Scan(&subjects); err != nil || subjects != 0 {
		t.Fatalf("fresh subjects=%d err=%v", subjects, err)
	}
	var modeColumn, stateTable bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.columns
WHERE table_schema=current_schema() AND table_name='subjects' AND column_name='mode'),
	to_regclass('learning_subject_state') IS NOT NULL`).Scan(&modeColumn, &stateTable); err != nil {
		t.Fatal(err)
	}
	if modeColumn || !stateTable {
		t.Fatalf("shared course schema: obsolete mode column=%t learning state table=%t", modeColumn, stateTable)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO subjects(id, name, created_at) VALUES
('course:duplicate-name-a', 'Одинаковый курс', 1),
('course:duplicate-name-b', 'Одинаковый курс', 1)`); err != nil {
		t.Fatalf("distinct shared courses with equal names should remain distinct: %v", err)
	}
	var versions int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM goose_db_version WHERE is_applied AND version_id > 0").Scan(&versions); err != nil || versions != migrationCount(t) {
		t.Fatalf("applied migrations=%d err=%v, want %d", versions, err, migrationCount(t))
	}
}

func TestCurrentExamSchemaUpgradesWithoutChangingExamData(t *testing.T) {
	pool := migrationPool(t)
	ctx := context.Background()
	if err := migrate(ctx, pool, migrationsUpTo(t, 10)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,email,password_hash,created_at)
VALUES ('user:exam','exam@example.test','hash',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO subjects(id,name,created_at)
VALUES ('subject:exam','Экзамен',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO geek_sessions(id,owner_id,subject_id,status,version,started_at,updated_at,state)
VALUES ('session:exam','user:exam','subject:exam','completed',1,1,2,'{}')`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var email, status, archivedMode string
	if err := pool.QueryRow(ctx, `SELECT u.email, g.status, rollback.mode
FROM users u JOIN geek_sessions g ON g.owner_id=u.id
	JOIN subjects s ON s.id=g.subject_id JOIN subject_mode_rollback rollback ON rollback.subject_id=s.id
WHERE g.id='session:exam'`).Scan(&email, &status, &archivedMode); err != nil {
		t.Fatal(err)
	}
	if email != "exam@example.test" || archivedMode != "exam" || status != "completed" {
		t.Fatalf("exam data changed after shared-course migration: email=%q prior_mode=%q status=%q", email, archivedMode, status)
	}
}

func TestSharedCourseMigrationDoesNotMergeSameNamedDemoCourses(t *testing.T) {
	pool := migrationPool(t)
	ctx := context.Background()
	if err := migrate(ctx, pool, migrationsUpTo(t, 12)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO subjects(id,name,created_at,mode) VALUES
('demo:exam-course','Одинаковое имя',1,'exam'),
('demo:learn-course','Одинаковое имя',1,'learn')`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM subjects WHERE name='Одинаковое имя'`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("same-name legacy courses count=%d err=%v, want both IDs retained", count, err)
	}
	var learnID, examID string
	if err := pool.QueryRow(ctx, `SELECT subject_id FROM subject_mode_rollback WHERE mode='learn'`).Scan(&learnID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT subject_id FROM subject_mode_rollback WHERE mode='exam'`).Scan(&examID); err != nil {
		t.Fatal(err)
	}
	if learnID != "demo:learn-course" || examID != "demo:exam-course" {
		t.Fatalf("rollback labels were reassigned: learn=%q exam=%q", learnID, examID)
	}
}

func migrationsUpTo(t *testing.T, max int) fstest.MapFS {
	t.Helper()
	files := fstest.MapFS{}
	entries, err := fs.ReadDir(migrations, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		var version int
		if _, err := fmt.Sscanf(entry.Name(), "%5d_", &version); err != nil {
			continue
		}
		if version > max {
			continue
		}
		data, err := fs.ReadFile(migrations, "migrations/"+entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		files[entry.Name()] = &fstest.MapFile{Data: data}
	}
	return files
}

func TestMigrateDownRemovesSchema(t *testing.T) {
	pool := migrationPool(t)
	ctx := context.Background()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	db := stdlib.OpenDB(*pool.Config().ConnConfig)
	defer db.Close()
	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		t.Fatal(err)
	}
	files, err := fs.Sub(migrations, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, db, files,
		goose.WithSessionLocker(locker), goose.WithDisableGlobalRegistry(true))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.DownTo(ctx, 0); err != nil {
		t.Fatalf("rollback all migrations: %v", err)
	}
	var remaining int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables
WHERE table_schema=current_schema() AND table_name IN
('users','auth_sessions','transcriptions','subjects','subject_materials','geek_sessions','geek_requests',
 'material_cleaning_jobs','course_graph_jobs','course_graphs','material_ocr_jobs')`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("schema tables remain after rollback: %d", remaining)
	}
}

func TestMigrateConcurrent(t *testing.T) {
	pool := migrationPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for range 4 {
		wg.Go(func() { errs <- Migrate(ctx, pool) })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	assertGooseVersion(t, pool)
}

func TestMigratePendingRollbackAndRetry(t *testing.T) {
	pool := migrationPool(t)
	ctx := context.Background()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	files := fstest.MapFS{"99999_probe.sql": {Data: []byte("-- +goose Up\nCREATE TABLE migration_probe(id integer);\nSELECT * FROM nonexistent_migration_table;\n")}}
	if err := migrate(ctx, pool, files); err == nil {
		t.Fatal("invalid migration succeeded")
	}
	var exists bool
	if err := pool.QueryRow(ctx, "SELECT to_regclass('migration_probe') IS NOT NULL").Scan(&exists); err != nil || exists {
		t.Fatalf("failed migration not rolled back: exists=%v err=%v", exists, err)
	}
	files["99999_probe.sql"].Data = []byte("-- +goose Up\nCREATE TABLE migration_probe(id integer);\n")
	if err := migrate(ctx, pool, files); err != nil {
		t.Fatal(err)
	}
}

func TestMigrateWaitsForLockAndCanRetryAfterCancellation(t *testing.T) {
	pool := migrationPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", lock.DefaultLockID); err != nil {
		t.Fatal(err)
	}
	lockedCtx, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
	defer cancel()
	if err := Migrate(lockedCtx, pool); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("migration did not wait for lock: %v", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	assertGooseVersion(t, pool)
}

// migrationCount is the number of embedded migrations, so adding one does not
// need this test to change.
func migrationCount(t *testing.T) int {
	t.Helper()
	files, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	return len(files)
}

func TestMigrateQueuesExistingMaterialsForOCR(t *testing.T) {
	pool := migrationPool(t)
	ctx := context.Background()
	db := stdlib.OpenDB(*pool.Config().ConnConfig)
	defer db.Close()
	files, err := fs.Sub(migrations, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, db, files, goose.WithDisableGlobalRegistry(true))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 4); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO subjects(id, name, created_at) VALUES ('s', 'Курс', 1)`,
		`INSERT INTO subject_materials(id, subject_id, filename, content_type, s3_raw_key, s3_parsed_key, parsed_text, status, created_at, updated_at)
		 VALUES ('m', 's', 'week1.pdf', 'application/pdf', 'raw', 'parsed', 'Old OCR', 'parsed', 1, 1)`,
	} {
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var job, material string
	if err := pool.QueryRow(ctx, `SELECT j.status, m.status FROM material_ocr_jobs j JOIN subject_materials m ON m.id = j.material_id WHERE m.id = 'm'`).Scan(&job, &material); err != nil {
		t.Fatalf("existing material was not queued for OCR: %v", err)
	}
	if job != "pending" || material != "pending" {
		t.Fatalf("job=%s material=%s, want pending", job, material)
	}
}
