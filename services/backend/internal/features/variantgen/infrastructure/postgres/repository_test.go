package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"

	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/variant"
	sharedpostgres "github.com/AskarKasimov/ai-tutor/services/backend/internal/shared/postgres"
	db "github.com/AskarKasimov/ai-tutor/services/backend/internal/shared/postgres/sqlcgen"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestLearningReadVariantCandidatesPreservesCurriculumCompetencies(t *testing.T) {
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
	schema := "variantgen_test_" + hex.EncodeToString(suffix)
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
	})
	if err := sharedpostgres.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO users(id,email,password_hash,created_at) VALUES ('variant-test-user','variant-test@example.test','hash',1);
INSERT INTO subjects(id,name,created_at) VALUES ('subject:test','Тестовый предмет',1);
INSERT INTO course_map_imports(revision,imported_at,imported_by,competency_count,constituent_count,outcome_count,task_count,source_format,source_headers,subject_id)
VALUES (1,1,'variant-test-user',1,1,0,0,'paired','[]'::jsonb,'subject:test');
INSERT INTO learning_subject_state(subject_id,active_revision) VALUES ('subject:test',1);
INSERT INTO course_map_competencies(id,name,revision) VALUES ('competency:test','Компетенция',1);
INSERT INTO course_map_constituents(id,competency_id,name,topic_level_id) VALUES ('constituent:test','competency:test','Составляющая','topic:basic');
INSERT INTO learning_curriculum_sections(id,revision,code,title) VALUES ('section:test',1,'Р.1','Раздел');
INSERT INTO learning_curriculum_competencies(id,revision,code) VALUES ('curriculum:test',1,'ОПК-1');
INSERT INTO learning_constituent_sections(id,revision,constituent_id,section_id) VALUES ('constituent-section:test',1,'constituent:test','section:test');
INSERT INTO learning_constituent_section_competencies(revision,constituent_section_id,curriculum_competency_id)
VALUES (1,'constituent-section:test','curriculum:test');`); err != nil {
		t.Fatal(err)
	}
	rows, err := db.New(pool).LearningReadVariantCandidates(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("candidate rows = %d, want 1", len(rows))
	}
	var profile variant.TaskProfile
	if err := json.Unmarshal(rows[0].ProfileJson, &profile); err != nil {
		t.Fatalf("decode SQL profile JSON: %v", err)
	}
	if len(profile.Constituent.Sections) != 1 || len(profile.Constituent.Sections[0].CurriculumCompetencies) != 1 || profile.Constituent.Sections[0].CurriculumCompetencies[0] != "ОПК-1" {
		t.Fatalf("SQL query lost curriculum competencies: %s", rows[0].ProfileJson)
	}
	profile.ID, profile.Question, profile.Options, profile.Origin, profile.CreatedAt = "task:test", "Вопрос", []string{}, "authored", 1
	taskJSON, profileJSON, err := splitSnapshots(profile)
	if err != nil {
		t.Fatal(err)
	}
	got, err := mergeSnapshots(taskJSON, profileJSON, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Constituent.Sections[0].CurriculumCompetencies[0] != "ОПК-1" {
		t.Fatalf("variant snapshot lost curriculum competencies: %#v", got.Constituent.Sections)
	}
}
