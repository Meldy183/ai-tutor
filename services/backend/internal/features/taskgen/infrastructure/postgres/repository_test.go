package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/competencymap"
	competencypostgres "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/competency/infrastructure/postgres"
	taskbankapp "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/taskbank/application"
	taskbankpostgres "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/taskbank/infrastructure/postgres"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/features/taskgen/application"
	sharedpostgres "github.com/AskarKasimov/ai-tutor/services/backend/internal/shared/postgres"
	db "github.com/AskarKasimov/ai-tutor/services/backend/internal/shared/postgres/sqlcgen"
	"github.com/jackc/pgx/v5/pgxpool"
)

func setupTaskgenRepository(t *testing.T) (*Repository, *competencypostgres.Repository, *pgxpool.Pool, context.Context, string) {
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
	schema := "taskgen_test_" + hex.EncodeToString(suffix)
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
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
	if err := sharedpostgres.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO subjects(id,name,created_at) VALUES ('subject:test','Тестовый предмет',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,email,password_hash,created_at) VALUES ('taskgen-test-user','taskgen-test@example.test','hash',1)`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
	})
	return New(pool), competencypostgres.New(pool), pool, ctx, "taskgen-test-user"
}

func taskgenMap(name string) competencymap.Map {
	return competencymap.Map{
		SourceFormat: "paired", SourceHeaders: []string{"Компетенция", "Составляющая", "Образовательный результат"},
		Competencies: []competencymap.Competency{{Key: "c", Name: "Компетенция " + name}},
		Constituents: []competencymap.Constituent{{Key: "s", CompetencyKey: "c", Name: "Составляющая " + name,
			Sections: []competencymap.CurriculumSection{{Code: "Р.1", Title: "Введение", CompetencyCodes: []string{"ОПК-1"}}}}},
		Outcomes: []competencymap.Outcome{{Key: "o", ConstituentKey: "s", Name: "ОР " + name}},
	}
}

func TestAudioPersistGeneratedTaskAndReimportMaterial(t *testing.T) {
	repository, maps, pool, ctx, userID := setupTaskgenRepository(t)
	if _, err := maps.Replace(ctx, "subject:test", userID, taskgenMap("один"), 10, nil); err != nil {
		t.Fatal(err)
	}
	var outcomeID string
	if err := pool.QueryRow(ctx, `SELECT id FROM course_map_outcomes WHERE name='ОР один'`).Scan(&outcomeID); err != nil {
		t.Fatal(err)
	}
	if err := repository.ImportMaterial(ctx, "subject:test", "notes", []string{outcomeID}, []string{"Материал по теме ОР один"}, 12); err != nil {
		t.Fatal(err)
	}
	snapshot, err := repository.Context(ctx, "subject:test", outcomeID)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Materials) != 1 {
		t.Fatalf("retrieved material context = %#v", snapshot.Materials)
	}
	sections := snapshot.Outcome.CurriculumSections
	if len(sections) != 1 || len(sections[0].CurriculumCompetencies) != 1 || sections[0].CurriculumCompetencies[0] != "ОПК-1" {
		t.Fatalf("retrieved curriculum context = %#v", sections)
	}
	if err := repository.ImportMaterial(ctx, "subject:test", "notes", []string{outcomeID}, []string{"Обновлённый материал по теме ОР один"}, 13); err != nil {
		t.Fatalf("reimport same material name: %v", err)
	}
	task, err := repository.Persist(ctx, snapshot, "request-1", userID, "test-model", application.Draft{
		Question: "Вопрос", Options: []string{}, VoiceInstruction: "Объясните ответ", ReferenceAnswer: "Эталон",
	}, 14)
	if err != nil {
		t.Fatal(err)
	}
	if task.Origin != "ai_generated" || task.ID == "" {
		t.Fatalf("generated task = %#v", task)
	}
	var assetID, instruction, status string
	var taskAssetID *string
	if err := pool.QueryRow(ctx, `SELECT id,instruction,status FROM learning_audio_assets`).Scan(&assetID, &instruction, &status); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT audio_asset_id FROM course_map_tasks WHERE id=$1`, task.ID).Scan(&taskAssetID); err != nil {
		t.Fatal(err)
	}
	if assetID != "taskaudio_"+task.ID || taskAssetID == nil || *taskAssetID != assetID || instruction != "Объясните ответ" || status != "pending" {
		t.Fatalf("generated audio asset/link = id:%q task:%v instruction:%q status:%q", assetID, taskAssetID, instruction, status)
	}
	replay, err := repository.Persist(ctx, snapshot, "request-1", userID, "test-model", application.Draft{
		Question: "Вопрос", Options: []string{}, VoiceInstruction: "Объясните ответ", ReferenceAnswer: "Эталон",
	}, 15)
	if err != nil || replay.ID != task.ID {
		t.Fatalf("idempotent task replay=%#v err=%v", replay, err)
	}
	var assetCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM learning_audio_assets WHERE id=$1`, assetID).Scan(&assetCount); err != nil || assetCount != 1 {
		t.Fatalf("idempotent task replay created %d audio assets: %v", assetCount, err)
	}
	var curriculumCode string
	if err := pool.QueryRow(ctx, `SELECT requested_profile->'outcome'->'CurriculumSections'->0->'curriculum_competencies'->>0 FROM learning_generation_runs`).Scan(&curriculumCode); err != nil || curriculumCode != "ОПК-1" {
		t.Fatalf("stored curriculum profile = %q, err=%v", curriculumCode, err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM learning_material_chunks WHERE material_name='notes'`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("material chunk count=%d err=%v, want immutable original and replacement", count, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM learning_generation_run_chunks`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("generation context chunk count=%d err=%v", count, err)
	}
}

func TestTaskAndGenerationCurriculumProfilesRemainConsistent(t *testing.T) {
	repository, maps, pool, ctx, userID := setupTaskgenRepository(t)
	data := competencymap.Map{
		SourceFormat: "paired", SourceHeaders: []string{"Ком", "Сост", "ОР"},
		Competencies: []competencymap.Competency{{Key: "c", Name: "Компетенция"}},
		Constituents: []competencymap.Constituent{
			{Key: "s1", CompetencyKey: "c", Name: "Составляющая 1", Sections: []competencymap.CurriculumSection{
				{Code: "Р.2", Title: "Практика", CompetencyCodes: []string{}},
				{Code: "Р.1", Title: "Введение", CompetencyCodes: []string{"ПК-2", "ОПК-1"}},
			}},
			{Key: "s2", CompetencyKey: "c", Name: "Составляющая 2", Sections: []competencymap.CurriculumSection{
				{Code: "Р.1", Title: "Введение", CompetencyCodes: []string{"ОПК-3"}},
			}},
			{Key: "s3", CompetencyKey: "c", Name: "Составляющая 3"},
		},
		Outcomes: []competencymap.Outcome{
			{Key: "o1", ConstituentKey: "s1", Name: "ОР 1"},
			{Key: "o2", ConstituentKey: "s2", Name: "ОР 2"},
			{Key: "o3", ConstituentKey: "s3", Name: "ОР 3"},
		},
	}
	if _, err := maps.Replace(ctx, "subject:test", userID, data, 10, nil); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		want []application.CurriculumSection
	}{
		{"ОР 1", []application.CurriculumSection{
			{Code: "Р.1", Title: "Введение", CurriculumCompetencies: []string{"ОПК-1", "ПК-2"}},
			{Code: "Р.2", Title: "Практика", CurriculumCompetencies: []string{}},
		}},
		{"ОР 2", []application.CurriculumSection{
			{Code: "Р.1", Title: "Введение", CurriculumCompetencies: []string{"ОПК-3"}},
		}},
		{"ОР 3", []application.CurriculumSection{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var outcomeID string
			if err := pool.QueryRow(ctx, "SELECT id FROM course_map_outcomes WHERE name=$1", tc.name).Scan(&outcomeID); err != nil {
				t.Fatal(err)
			}
			snapshot, err := repository.Context(ctx, "subject:test", outcomeID)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(snapshot.Outcome.CurriculumSections, tc.want) {
				t.Fatalf("generation profile: got=%#v want=%#v", snapshot.Outcome.CurriculumSections, tc.want)
			}
			task, err := repository.Persist(ctx, snapshot, "profile-test", userID, "model", application.Draft{
				Question: "Вопрос", Options: []string{}, VoiceInstruction: "Ответьте", ReferenceAnswer: "Ответ",
			}, 11)
			if err != nil {
				t.Fatal(err)
			}
			profile, err := db.New(pool).LearningGetTaskProfile(ctx, db.LearningGetTaskProfileParams{SubjectID: "subject:test", TaskID: task.ID})
			if err != nil {
				t.Fatal(err)
			}
			var sections []struct {
				Code         string   `json:"code"`
				Title        string   `json:"title"`
				Competencies []string `json:"curriculum_competencies"`
			}
			if err := json.Unmarshal([]byte(profile.CurriculumSections), &sections); err != nil {
				t.Fatal(err)
			}
			got := make([]application.CurriculumSection, len(sections))
			for i, section := range sections {
				got[i] = application.CurriculumSection{Code: section.Code, Title: section.Title, CurriculumCompetencies: section.Competencies}
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("catalog profile: got=%#v want=%#v", got, tc.want)
			}
		})
	}
}

func TestSubjectMaterialsWithSameNameAndGenerationContextStayIsolated(t *testing.T) {
	repository, maps, pool, ctx, userID := setupTaskgenRepository(t)
	if _, err := maps.Replace(ctx, "subject:test", userID, taskgenMap("A"), 10, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO subjects(id,name,created_at) VALUES ('subject:b','Subject B',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := maps.Replace(ctx, "subject:b", userID, taskgenMap("B"), 11, nil); err != nil {
		t.Fatal(err)
	}
	var outcomeA, outcomeB string
	if err := pool.QueryRow(ctx, `SELECT id FROM course_map_outcomes WHERE name='ОР A'`).Scan(&outcomeA); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT id FROM course_map_outcomes WHERE name='ОР B'`).Scan(&outcomeB); err != nil {
		t.Fatal(err)
	}
	var taskA, taskB string
	if err := pool.QueryRow(ctx, `SELECT id FROM course_map_tasks WHERE outcome_id=$1 LIMIT 1`, outcomeA).Scan(&taskA); err == nil {
		t.Fatal("test map unexpectedly contains authored task")
	}
	for _, item := range []struct{ outcome, task string }{{outcomeA, "task-a"}, {outcomeB, "task-b"}} {
		if _, err := pool.Exec(ctx, `INSERT INTO course_map_tasks(id,outcome_id,question,options,created_at) VALUES ($1,$2,$3,'[]',1)`, item.task, item.outcome, item.task+" question"); err != nil {
			t.Fatal(err)
		}
	}
	taskA, taskB = "task-a", "task-b"
	course_map_tasks := taskbankpostgres.New(pool)
	for _, tc := range []struct{ subject, want string }{{"subject:test", taskA}, {"subject:b", taskB}} {
		items, err := course_map_tasks.Search(ctx, taskbankapp.SearchFilter{SubjectID: tc.subject, Limit: 10})
		if err != nil || len(items) != 1 || items[0].ID != tc.want {
			t.Fatalf("task search subject %s = %#v, err=%v", tc.subject, items, err)
		}
	}
	if _, err := course_map_tasks.Profile(ctx, "subject:b", taskA); err == nil {
		t.Fatal("task profile accepted task from another subject")
	}
	if err := repository.ImportMaterial(ctx, "subject:test", "shared-name", []string{outcomeA}, []string{"Альфа уникальный материал для ОР A"}, 12); err != nil {
		t.Fatal(err)
	}
	if err := repository.ImportMaterial(ctx, "subject:b", "shared-name", []string{outcomeB}, []string{"Бета материал предмета B"}, 13); err != nil {
		t.Fatal(err)
	}
	snapshotA, err := repository.Context(ctx, "subject:test", outcomeA)
	if err != nil {
		t.Fatal(err)
	}
	snapshotB, err := repository.Context(ctx, "subject:b", outcomeB)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshotA.Materials) != 1 || snapshotA.Materials[0].Content != "Альфа уникальный материал для ОР A" {
		t.Fatalf("subject A context contains foreign material: %#v", snapshotA.Materials)
	}
	if len(snapshotB.Materials) != 1 || snapshotB.Materials[0].Content != "Бета материал предмета B" {
		t.Fatalf("subject B context contains foreign material: %#v", snapshotB.Materials)
	}
	if err := repository.ImportMaterial(ctx, "subject:test", "shared-name", []string{outcomeA}, []string{"Новое альфа содержание"}, 14); err != nil {
		t.Fatal(err)
	}
	var linksB, chunksB int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM learning_material_chunk_outcomes link JOIN learning_material_chunks chunk ON chunk.id=link.chunk_id WHERE chunk.subject_id='subject:b' AND chunk.material_name='shared-name' AND link.outcome_id=$1`, outcomeB).Scan(&linksB); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM learning_material_chunks WHERE subject_id='subject:b' AND material_name='shared-name'`).Scan(&chunksB); err != nil {
		t.Fatal(err)
	}
	if linksB != 1 || chunksB != 1 {
		t.Fatalf("reimport A damaged B material: links=%d chunks=%d", linksB, chunksB)
	}
	if _, err := repository.Context(ctx, "subject:test", outcomeB); err == nil {
		t.Fatal("generation context accepted another subject's outcome")
	}
	if err := repository.ImportMaterial(ctx, "subject:test", "invalid-link", []string{outcomeB}, []string{"чужая связь"}, 15); err == nil {
		t.Fatal("material import accepted another subject's outcome")
	}
}

func TestAudioPersistRejectsChangedMapWithoutAddingGeneratedAudio(t *testing.T) {
	repository, maps, pool, ctx, userID := setupTaskgenRepository(t)
	if _, err := maps.Replace(ctx, "subject:test", userID, taskgenMap("до"), 10, nil); err != nil {
		t.Fatal(err)
	}
	var outcomeID string
	if err := pool.QueryRow(ctx, `SELECT id FROM course_map_outcomes WHERE name='ОР до'`).Scan(&outcomeID); err != nil {
		t.Fatal(err)
	}
	snapshot, err := repository.Context(ctx, "subject:test", outcomeID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := maps.Replace(ctx, "subject:test", userID, taskgenMap("после"), 12, nil); err != nil {
		t.Fatal(err)
	}
	var before int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM learning_audio_assets`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	_, err = repository.Persist(ctx, snapshot, "stale-request", userID, "test-model", application.Draft{
		Question: "Сгенерированный вопрос", Options: []string{}, VoiceInstruction: "Скажите ответ", ReferenceAnswer: "Ответ",
	}, 13)
	if err == nil {
		t.Fatal("persist accepted a stale competency map")
	}
	var after, generated int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM learning_audio_assets`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM course_map_tasks WHERE origin='ai_generated'`).Scan(&generated); err != nil {
		t.Fatal(err)
	}
	if after != before || generated != 0 {
		t.Fatalf("stale persist added generated data: assets %d->%d course_map_tasks=%d", before, after, generated)
	}
}
