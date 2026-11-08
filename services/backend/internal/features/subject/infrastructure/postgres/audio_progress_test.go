package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"testing"

	sharedpostgres "github.com/AskarKasimov/ai-tutor/services/backend/internal/shared/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

func progressPool(t *testing.T) (*pgxpool.Pool, context.Context) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL repository integration tests")
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
	schema := "subject_audio_test_" + hex.EncodeToString(suffix)
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
	if err := sharedpostgres.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO subjects(id,name,created_at) VALUES ('subject:test','Тестовый курс',1)`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Close(); _, _ = admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE"); admin.Close() })
	return pool, ctx
}

func TestLearningAudioPreparationReadsOnlyOneActiveRevision(t *testing.T) {
	pool, ctx := progressPool(t)
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,email,password_hash,role,created_at) VALUES ('admin','admin@example.test','test','admin',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO subjects(id,name,created_at) VALUES ('subject:other','Другой курс',1)`); err != nil {
		t.Fatal(err)
	}
	for revision, competency := range map[int]string{1: "old-competency", 2: "current-competency"} {
		subjectID := map[int]string{1: "subject:other", 2: "subject:test"}[revision]
		if _, err := pool.Exec(ctx, `INSERT INTO course_map_imports(revision,subject_id,imported_at,imported_by,competency_count,constituent_count,outcome_count,task_count,source_format,source_headers) VALUES ($1,$2,1,'admin',1,1,1,1,'paired','[]')`, revision, subjectID); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO course_map_competencies(id,name,revision) VALUES ($1,$2,$3)`, competency, competency, revision); err != nil {
			t.Fatal(err)
		}
		constituent := competency + "-constituent"
		outcome := competency + "-outcome"
		if _, err := pool.Exec(ctx, `INSERT INTO course_map_constituents(id,competency_id,name) VALUES ($1,$2,$2)`, constituent, competency); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO course_map_outcomes(id,constituent_id,name) VALUES ($1,$2,$2)`, outcome, constituent); err != nil {
			t.Fatal(err)
		}
		assetID := competency + "-asset"
		if _, err := pool.Exec(ctx, `INSERT INTO learning_audio_assets(id,instruction,object_key,status,bucket,storage_uri,audio_url) VALUES ($1,'Speak this',$1,$2,CASE WHEN $2='ready' THEN 'bucket' END,CASE WHEN $2='ready' THEN 's3://bucket/'||$1 END,CASE WHEN $2='ready' THEN '/learn/task-audio/'||$1||'/file' END)`, assetID, map[int]string{1: "pending", 2: "ready"}[revision]); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO course_map_tasks(id,outcome_id,question,voice_instruction,created_at,audio_asset_id) VALUES ($1,$2,'Question','Speak this',1,$3)`, competency+"-task", outcome, assetID); err != nil {
			t.Fatal(err)
		}
		if revision == 2 {
			if _, err := pool.Exec(ctx, `INSERT INTO course_map_tasks(id,outcome_id,question,voice_instruction,created_at) VALUES ('missing-asset-task',$1,'Question','Speak this',2)`, outcome); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO learning_subject_state(subject_id,active_revision) VALUES ('subject:test',2)`); err != nil {
		t.Fatal(err)
	}
	revision, assets, err := NewLearningAudioProgress(pool).Snapshot(ctx, "subject:test")
	if err != nil {
		t.Fatal(err)
	}
	if revision != 2 || len(assets) != 2 {
		t.Fatalf("revision=%d assets=%+v", revision, assets)
	}
	if assets[0].AssetID == nil || assets[0].Status == nil || *assets[0].Status != "ready" {
		t.Fatalf("active asset = %+v", assets[0])
	}
	if assets[1].AssetID != nil {
		t.Fatalf("missing audio link unexpectedly has asset: %+v", assets[1])
	}
}
