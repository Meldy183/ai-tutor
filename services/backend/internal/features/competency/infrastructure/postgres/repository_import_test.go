package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/competencymap"
	sharedpostgres "github.com/AskarKasimov/ai-tutor/services/backend/internal/shared/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

func setupImportRepository(t *testing.T) (*Repository, context.Context, string) {
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
	schema := "competency_import_test_" + hex.EncodeToString(suffix)
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
	userID := "import-test-user"
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,email,password_hash,created_at) VALUES ($1,$2,$3,$4)`, userID, userID+"@example.test", "hash", 1); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
	})
	return New(pool), ctx, userID
}

func importMap(name string) competencymap.Map {
	include := true
	importance := 4
	content := "Нужно уметь применить модель"
	return competencymap.Map{
		SourceFormat:  "ml-map",
		SourceHeaders: []string{"", "Компетенция", "Уровень темы", "Составляющая", "Образовательный результат", "Что должно войти в тест", "Таксономия", "Уровень ALDs", "Важность", "Раздел РПД · компетенции РПД", "Задание1"},
		Competencies:  []competencymap.Competency{{Key: "c", Name: "Компетенция " + name}},
		Constituents: []competencymap.Constituent{{
			Key: "s", CompetencyKey: "c", Name: "Составляющая " + name,
			TopicLevelCode: "basic",
			Sections:       []competencymap.CurriculumSection{{Code: "Р.1", Title: "Основы", CompetencyCodes: []string{"ОПК-2", "ПК-2"}}},
		}},
		Outcomes: []competencymap.Outcome{{
			Key: "o", ConstituentKey: "s", Name: "ОР " + name,
			IncludeInTest: &include, TaxonomyCode: "application", ALDLevelCode: "basic",
			Importance: &importance, EducationalContent: &content, SourceRowIndexes: []int{1},
		}},
		Tasks: []competencymap.Task{{
			OutcomeKey: "o", Question: "Вопрос " + name, Options: []string{"Первый", "Второй"},
			VoiceInstruction: "Объясните выбор", ReferenceAnswer: "Первый верен",
			Column: "Задание1", Row: 2, SourceRowIndex: 1, SourceColumnIndex: 11,
		}},
		SourceRows: []competencymap.SourceRow{{Index: 1, Line: 2, Cells: []string{
			"", "Компетенция " + name, "Базовый", "Составляющая " + name, "ОР " + name,
			"TRUE", "Применение", "базовый", "4", "Р.1 Основы", "Экран: тестовое задание",
		}}},
	}
}

func TestImportAudioPersistsTypedMapAndReplacesAllMapData(t *testing.T) {
	repository, ctx, userID := setupImportRepository(t)
	pool := repository.pool
	if _, err := pool.Exec(ctx, `INSERT INTO transcriptions(id,user_id,text,created_at) VALUES ('transcription-keep',$1,'сохранить',1)`, userID); err != nil {
		t.Fatal(err)
	}

	first, err := repository.Replace(ctx, "subject:test", userID, importMap("один"), 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.Revision != 1 || first.CompetencyCount != 1 || first.ConstituentCount != 1 || first.OutcomeCount != 1 || first.TaskCount != 1 {
		t.Fatalf("first import result = %#v", first)
	}
	assertImportedProfile(t, ctx, pool, "ОР один", "Вопрос один", 2)
	var assetID, instruction, status string
	var taskAssetID *string
	if err := pool.QueryRow(ctx, `SELECT id,instruction,status FROM learning_audio_assets`).Scan(&assetID, &instruction, &status); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT audio_asset_id FROM learning_tasks`).Scan(&taskAssetID); err != nil {
		t.Fatal(err)
	}
	if assetID != "taskaudio_"+mustTaskID(t, pool, ctx) || taskAssetID == nil || *taskAssetID != assetID || instruction != "Объясните выбор" || status != "pending" {
		t.Fatalf("imported audio asset/link = id:%q task:%v instruction:%q status:%q", assetID, taskAssetID, instruction, status)
	}

	second, err := repository.Replace(ctx, "subject:test", userID, importMap("два"), 20, nil)
	if err != nil {
		t.Fatal(err)
	}
	if second.Revision != 2 {
		t.Fatalf("replacement revision = %d, want 2", second.Revision)
	}
	var names []string
	if err := pool.QueryRow(ctx, `SELECT array_agg(name ORDER BY name) FROM learning_outcomes`).Scan(&names); err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0] != "ОР два" {
		t.Fatalf("active map after replacement = %v, want only new outcome", names)
	}
	var imports, sourceRows, oldTasks, transcriptions int
	for query, target := range map[string]*int{
		`SELECT count(*) FROM learning_map_imports`:                         &imports,
		`SELECT count(*) FROM learning_map_source_rows`:                     &sourceRows,
		`SELECT count(*) FROM learning_tasks WHERE question='Вопрос один'`:  &oldTasks,
		`SELECT count(*) FROM transcriptions WHERE id='transcription-keep'`: &transcriptions,
	} {
		if err := pool.QueryRow(ctx, query).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	if imports != 1 || sourceRows != 1 || oldTasks != 0 || transcriptions != 1 {
		t.Fatalf("replacement cleanup/preservation imports=%d source_rows=%d old_tasks=%d transcriptions=%d", imports, sourceRows, oldTasks, transcriptions)
	}
}

func TestReplaceAllowsDeletingImportReferencedBySubjectActiveRevision(t *testing.T) {
	repository, ctx, userID := setupImportRepository(t)
	first, err := repository.Replace(ctx, "subject:test", userID, importMap("до миграции"), 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.pool.Exec(ctx, `INSERT INTO learning_subject_state(subject_id,active_revision) VALUES ('subject:test',$1) ON CONFLICT(subject_id) DO UPDATE SET active_revision=EXCLUDED.active_revision`, first.Revision); err != nil {
		t.Fatal(err)
	}

	second, err := repository.Replace(ctx, "subject:test", userID, importMap("после миграции"), 20, nil)
	if err != nil {
		t.Fatalf("replace failed while active_revision referenced old import: %v", err)
	}
	if second.Revision != first.Revision+1 {
		t.Fatalf("replacement revision=%d, want %d", second.Revision, first.Revision+1)
	}
	var outcome string
	if err := repository.pool.QueryRow(ctx, `SELECT name FROM learning_outcomes`).Scan(&outcome); err != nil || outcome != "ОР после миграции" {
		t.Fatalf("active outcome=%q err=%v", outcome, err)
	}
	var activeRevision *int64
	if err := repository.pool.QueryRow(ctx, `SELECT active_revision FROM learning_subject_state WHERE subject_id='subject:test'`).Scan(&activeRevision); err != nil || activeRevision == nil || *activeRevision != second.Revision {
		t.Fatalf("active_revision=%v, want replacement revision %d (err=%v)", activeRevision, second.Revision, err)
	}
}

func TestSubjectReplacementRollbackRestoresSelectedAndOtherMaps(t *testing.T) {
	repository, ctx, userID := setupImportRepository(t)
	pool := repository.pool
	firstA, err := repository.Replace(ctx, "subject:test", userID, importMap("A-сохранена"), 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	const subjectB = "subject:rollback-b"
	if _, err := pool.Exec(ctx, `INSERT INTO subjects(id,name,created_at) VALUES ($1,'Предмет B',2)`, subjectB); err != nil {
		t.Fatal(err)
	}
	firstB, err := repository.Replace(ctx, subjectB, userID, importMap("B-сохранён"), 20, nil)
	if err != nil {
		t.Fatal(err)
	}
	invalid := importMap("A-ошибка")
	invalid.Tasks[0].SourceColumnIndex = len(invalid.SourceHeaders) + 1
	if _, err := repository.Replace(ctx, "subject:test", userID, invalid, 30, nil); err == nil {
		t.Fatal("invalid A replacement unexpectedly succeeded")
	}
	var activeA, activeB, globalRevision int64
	for id, target := range map[string]*int64{"subject:test": &activeA, subjectB: &activeB} {
		if err := pool.QueryRow(ctx, `SELECT active_revision FROM learning_subject_state WHERE subject_id=$1`, id).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	if err := pool.QueryRow(ctx, `SELECT revision FROM learning_map_state WHERE singleton=true`).Scan(&globalRevision); err != nil {
		t.Fatal(err)
	}
	if activeA != firstA.Revision || activeB != firstB.Revision || globalRevision != firstB.Revision {
		t.Fatalf("rollback revisions: A=%d B=%d global=%d, want %d %d %d", activeA, activeB, globalRevision, firstA.Revision, firstB.Revision, firstB.Revision)
	}
	var imports, outcomeA, outcomeB, cancelledA int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM learning_map_imports`).Scan(&imports); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM learning_outcomes WHERE name='ОР A-сохранена'`).Scan(&outcomeA); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM learning_outcomes WHERE name='ОР B-сохранён'`).Scan(&outcomeB); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM learning_audio_assets WHERE status='cancelled'`).Scan(&cancelledA); err != nil {
		t.Fatal(err)
	}
	if imports != 2 || outcomeA != 1 || outcomeB != 1 || cancelledA != 0 {
		t.Fatalf("rollback changed data: imports=%d A=%d B=%d cancelled=%d", imports, outcomeA, outcomeB, cancelledA)
	}
}

func TestConcurrentSubjectImportsAllocateUniqueGlobalRevisions(t *testing.T) {
	repository, ctx, userID := setupImportRepository(t)
	const subjectB = "subject:concurrent-b"
	if _, err := repository.pool.Exec(ctx, `INSERT INTO subjects(id,name,created_at) VALUES ($1,'Предмет B',2)`, subjectB); err != nil {
		t.Fatal(err)
	}
	type result struct {
		subjectID string
		imported  competencymap.ImportResult
		err       error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	importCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for _, id := range []string{"subject:test", subjectB} {
		go func(subjectID string) {
			<-start
			imported, err := repository.Replace(importCtx, subjectID, userID, importMap(subjectID), 10, nil)
			results <- result{subjectID: subjectID, imported: imported, err: err}
		}(id)
	}
	close(start)
	completed := make(map[string]competencymap.ImportResult, 2)
	for range 2 {
		select {
		case got := <-results:
			if got.err != nil {
				t.Fatalf("import %s: %v", got.subjectID, got.err)
			}
			completed[got.subjectID] = got.imported
		case <-importCtx.Done():
			t.Fatalf("parallel imports did not finish before deadline: %v", importCtx.Err())
		}
	}
	first, second := completed["subject:test"], completed[subjectB]
	if (first.Revision != 1 || second.Revision != 2) && (first.Revision != 2 || second.Revision != 1) {
		t.Fatalf("parallel imports received non-monotonic revisions: ML=%d B=%d", first.Revision, second.Revision)
	}
	var globalRevision int64
	if err := repository.pool.QueryRow(ctx, `SELECT revision FROM learning_map_state WHERE singleton=true`).Scan(&globalRevision); err != nil || globalRevision != 2 {
		t.Fatalf("global revision=%d, want 2 (err=%v)", globalRevision, err)
	}
	var activeA, activeB int64
	if err := repository.pool.QueryRow(ctx, `SELECT active_revision FROM learning_subject_state WHERE subject_id=$1`, "subject:test").Scan(&activeA); err != nil {
		t.Fatal(err)
	}
	if err := repository.pool.QueryRow(ctx, `SELECT active_revision FROM learning_subject_state WHERE subject_id=$1`, subjectB).Scan(&activeB); err != nil {
		t.Fatal(err)
	}
	if activeA != first.Revision || activeB != second.Revision {
		t.Fatalf("subject active revisions ML=%d/%d B=%d/%d", activeA, first.Revision, activeB, second.Revision)
	}
}

func TestImportAudioRollsBackInvalidMapWithoutLosingActiveMap(t *testing.T) {
	repository, ctx, userID := setupImportRepository(t)
	if _, err := repository.Replace(ctx, "subject:test", userID, importMap("сохранённая"), 10, nil); err != nil {
		t.Fatal(err)
	}
	invalid := importMap("повреждённая")
	invalid.Outcomes[0].ConstituentKey = "missing-constituent"
	if _, err := repository.Replace(ctx, "subject:test", userID, invalid, 20, nil); err == nil {
		t.Fatal("map with a missing constituent was imported")
	}
	var revision int64
	var outcomeName, question string
	if err := repository.pool.QueryRow(ctx, `SELECT revision FROM learning_map_state WHERE singleton=true`).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if err := repository.pool.QueryRow(ctx, `SELECT outcome.name, task.question FROM learning_outcomes outcome JOIN learning_tasks task ON task.outcome_id=outcome.id`).Scan(&outcomeName, &question); err != nil {
		t.Fatal(err)
	}
	var assets int
	if err := repository.pool.QueryRow(ctx, `SELECT count(*) FROM learning_audio_assets`).Scan(&assets); err != nil {
		t.Fatal(err)
	}
	if revision != 1 || outcomeName != "ОР сохранённая" || question != "Вопрос сохранённая" || assets != 1 {
		t.Fatalf("failed replacement changed active data/assets: revision=%d outcome=%q task=%q assets=%d", revision, outcomeName, question, assets)
	}
}

func TestTaskSourceCoordinatesCannotBePartiallyNull(t *testing.T) {
	repository, ctx, userID := setupImportRepository(t)
	if _, err := repository.Replace(ctx, "subject:test", userID, importMap("координаты"), 10, nil); err != nil {
		t.Fatal(err)
	}
	var outcomeID string
	var revision int64
	if err := repository.pool.QueryRow(ctx, `SELECT outcome_id, source_revision FROM learning_tasks LIMIT 1`).Scan(&outcomeID, &revision); err != nil {
		t.Fatal(err)
	}
	_, err := repository.pool.Exec(ctx, `
INSERT INTO learning_tasks(id,outcome_id,question,criteria,source_row,source_column,origin,
                  source_revision,source_row_index,source_column_index,created_at)
VALUES ('partial-coordinate-task',$1,'invalid',NULL,2,'Задание1','authored',$2,NULL,11,11)`, outcomeID, revision)
	if err == nil {
		t.Fatal("task with only source_revision and source_column_index was accepted")
	}
}

func mustTaskID(t *testing.T, pool *pgxpool.Pool, ctx context.Context) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(ctx, `SELECT id FROM learning_tasks`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func assertImportedProfile(t *testing.T, ctx context.Context, pool *pgxpool.Pool, outcomeName, question string, rowLine int) {
	t.Helper()
	var gotOutcome, gotQuestion, taxonomy, ald, topic, section, codes, constituentName, sourceCell string
	var included bool
	var importance int16
	var rowIndex, columnIndex, sourceLine int
	var options []byte
	if err := pool.QueryRow(ctx, `
SELECT outcome.name, outcome.include_in_test, taxonomy.code, ald.code, outcome.importance,
       constituent.name, topic.code, section.code, source.cells->>10,
       task.question, task.options, task.source_row_index, task.source_column_index, source.source_line,
       codes.codes
FROM learning_outcomes outcome
JOIN learning_constituents constituent ON constituent.id=outcome.constituent_id
LEFT JOIN learning_topic_levels topic ON topic.id=constituent.topic_level_id
LEFT JOIN learning_taxonomies taxonomy ON taxonomy.id=outcome.taxonomy_id
LEFT JOIN learning_ald_levels ald ON ald.id=outcome.ald_level_id
JOIN learning_constituent_sections link ON link.constituent_id=constituent.id
JOIN learning_curriculum_sections section ON section.id=link.section_id
JOIN learning_tasks task ON task.outcome_id=outcome.id
JOIN learning_map_source_rows source ON source.revision=task.source_revision AND source.row_index=task.source_row_index
JOIN LATERAL (SELECT string_agg(curriculum.code, ',' ORDER BY curriculum.code) AS codes
              FROM learning_constituent_section_competencies mapped
              JOIN learning_curriculum_competencies curriculum ON curriculum.id=mapped.curriculum_competency_id
              WHERE mapped.constituent_section_id=link.id) codes ON true
WHERE outcome.name=$1`, outcomeName).Scan(&gotOutcome, &included, &taxonomy, &ald, &importance, &constituentName, &topic, &section, &sourceCell, &gotQuestion, &options, &rowIndex, &columnIndex, &sourceLine, &codes); err != nil {
		t.Fatal(err)
	}
	if gotOutcome != outcomeName || !included || taxonomy != "application" || ald != "basic" || importance != 4 || topic != "basic" || section != "Р.1" {
		t.Fatalf("stored profile fields mismatch for %q: taxonomy=%s ALD=%s importance=%d topic=%s section=%s", gotOutcome, taxonomy, ald, importance, topic, section)
	}
	if gotQuestion != question || rowIndex != 1 || columnIndex != 11 || sourceLine != rowLine {
		t.Fatalf("stored task/source mapping mismatch: question=%q row=%d col=%d physical=%d", gotQuestion, rowIndex, columnIndex, sourceLine)
	}
	if constituentName == "" || !strings.HasPrefix(sourceCell, "Экран:") {
		t.Fatalf("stored hierarchy/source cell are missing: constituent=%q cell=%q", constituentName, sourceCell)
	}
	var gotOptions []string
	if err := json.Unmarshal(options, &gotOptions); err != nil || len(gotOptions) != 2 || gotOptions[0] != "Первый" {
		t.Fatalf("stored options = %s, error=%v", options, err)
	}
	if codes != "ОПК-2,ПК-2" {
		t.Fatalf("stored curriculum competency codes = %q", codes)
	}
}

func TestConditionalImportRejectsStaleRevision(t *testing.T) {
	repository, ctx, userID := setupImportRepository(t)
	zero := int64(0)
	if _, err := repository.Replace(ctx, "subject:test", userID, importMap("first"), 10, &zero); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Replace(ctx, "subject:test", userID, importMap("stale"), 20, &zero); err == nil {
		t.Fatal("stale revision was accepted")
	}
	snapshot, err := repository.Read(ctx, "subject:test")
	if err != nil || snapshot.Revision == 0 {
		t.Fatalf("map lost: %+v %v", snapshot, err)
	}
}
