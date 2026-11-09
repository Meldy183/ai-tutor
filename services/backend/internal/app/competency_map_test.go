package app

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"

	coursegraphentity "github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/coursegraph"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/features/competency/infrastructure/csvparser"
	competencypg "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/competency/infrastructure/postgres"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/features/coursegraph/infrastructure/postgres"
	geekgraph "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/geek/infrastructure/graph"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/shared/fault"
	"github.com/jackc/pgx/v5/pgxpool"
)

// mapBook is a competency map book of one sheet in the course template:
// one competency with three outcomes for the test, so its graph has a main
// task and two basic ones.
// mapBook is a course map as it is imported: the Geek sheet and the
// learning sheet with the same structure.
func mapBook(t *testing.T) []byte {
	t.Helper()
	return pairedMapBook(t, false)
}

func newPairedGraphRepository(pool *pgxpool.Pool) *pairedCourseGraphRepository {
	return &pairedCourseGraphRepository{Repository: postgres.New(pool), pool: pool, learning: competencypg.New(pool)}
}

// Set REAL_COMPETENCY_MAP_PATH to run the supplied workbook through the real
// PostgreSQL import and both mode readers. The fixture is isolated per test and
// the workbook itself stays outside the repository.
func TestRealPairedWorkbookUsesBothModeBanks(t *testing.T) {
	path := os.Getenv("REAL_COMPETENCY_MAP_PATH")
	if path == "" {
		t.Skip("REAL_COMPETENCY_MAP_PATH is not set")
	}
	book, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	assertPairedWorkbookBanks(t, book)
}

// Keep the import/worker/read path unconditional, independently of the local book.
func TestPairedWorkbookUsesBothModeBanks(t *testing.T) {
	assertPairedWorkbookBanks(t, pairedMapBook(t, true))
}

func assertPairedWorkbookBanks(t *testing.T, book []byte) *fixture {
	t.Helper()
	f := newFixture(t)
	admin := f.admin(t)
	response := uploadWithHeader(f, "/admin/subjects/subject:test/graph/competency-map", "file", "map.xlsx", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", book, admin, "X-Expected-Learning-Revision", "0")
	if response.Code != http.StatusAccepted {
		t.Fatalf("import: %d %s", response.Code, response.Body.String())
	}
	ctx := context.Background()
	for _, mode := range []string{"diagnostic", "exam"} {
		var outcomes, tasks int
		if err := f.pool.QueryRow(ctx, `SELECT count(DISTINCT o.id),count(t.id) FROM course_map_competencies c JOIN course_map_constituents p ON p.competency_id=c.id JOIN course_map_outcomes o ON o.constituent_id=p.id LEFT JOIN course_map_tasks t ON t.outcome_id=o.id AND t.mode=$1 WHERE c.revision=1 AND c.source_mode=$1`, mode).Scan(&outcomes, &tasks); err != nil {
			t.Fatal(err)
		}
		if outcomes == 0 || tasks == 0 {
			t.Fatalf("%s bank empty: outcomes=%d tasks=%d", mode, outcomes, tasks)
		}
		t.Logf("%s bank: outcomes=%d tasks=%d", mode, outcomes, tasks)
	}
	learning, err := competencypg.New(f.pool).Read(ctx, "subject:test")
	if err != nil || len(learning.Competencies) == 0 {
		t.Fatalf("learning read: %v", err)
	}
	f.app.graphWorker.SetQuestions(mapTestQuestions{})
	if worked, err := f.app.graphWorker.ProcessOne(ctx); err != nil || !worked {
		t.Fatalf("ProcessOne: %v %v", worked, err)
	}
	graph, ok, err := geekgraph.NewStored(f.pool).Current(ctx, "subject:test")
	if err != nil || !ok || len(graph.Nodes) == 0 {
		t.Fatalf("Geek read: %v %v", ok, err)
	}
	if err := graph.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, n := range graph.Nodes {
		var mode string
		if err := f.pool.QueryRow(ctx, `SELECT c.source_mode FROM course_map_outcomes o JOIN course_map_constituents p ON p.id=o.constituent_id JOIN course_map_competencies c ON c.id=p.competency_id WHERE o.id=$1`, n.ID).Scan(&mode); err != nil || mode != "exam" {
			t.Fatalf("exam vertex %s mode=%s: %v", n.ID, mode, err)
		}
		for _, v := range n.Variants {
			var rephrase string
			if err := f.pool.QueryRow(ctx, `SELECT rephrase FROM course_map_tasks WHERE id=$1 AND mode='exam'`, v.TaskID).Scan(&rephrase); err != nil || rephrase == "" {
				t.Fatalf("prepared task %s: %v", v.TaskID, err)
			}
		}
	}
	return f
}

// Set REAL_UNIFIED_COMPETENCY_MAP_PATH to import a one-sheet unified map.
func TestRealUnifiedWorkbookUsesBothModeBanks(t *testing.T) {
	path := os.Getenv("REAL_UNIFIED_COMPETENCY_MAP_PATH")
	if path == "" {
		t.Skip("REAL_UNIFIED_COMPETENCY_MAP_PATH is not set")
	}
	book, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	f := assertUnifiedWorkbook(t, book)
	var tasks, withCriteria int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*), count(*) FILTER (WHERE criteria <> '') FROM course_map_tasks WHERE mode='diagnostic'`).Scan(&tasks, &withCriteria); err != nil {
		t.Fatal(err)
	}
	t.Logf("diagnostic tasks with criteria: %d of %d", withCriteria, tasks)
}

func TestUnifiedWorkbookUsesBothModeBanks(t *testing.T) {
	f := assertUnifiedWorkbook(t, unifiedMapBook(t))
	var withCriteria, levels int
	if err := f.pool.QueryRow(context.Background(), `
		SELECT (SELECT count(*) FROM course_map_tasks WHERE mode='diagnostic' AND criteria LIKE 'Эталон:%'),
		       (SELECT count(*) FROM course_map_outcomes o JOIN course_map_constituents p ON p.id=o.constituent_id JOIN course_map_competencies c ON c.id=p.competency_id
		        WHERE c.source_mode='diagnostic' AND o.ald_level_id IS NOT NULL)`).Scan(&withCriteria, &levels); err != nil {
		t.Fatal(err)
	}
	if withCriteria != 3 {
		t.Fatalf("diagnostic tasks keep their criteria: %d of 3", withCriteria)
	}
	if levels != 2 {
		t.Fatalf("only the outcomes with ALDs in the book have one: %d", levels)
	}
}

// assertUnifiedWorkbook imports a unified map and checks that each mode got
// the tasks of its own columns only, with one structure of outcomes.
func assertUnifiedWorkbook(t *testing.T, book []byte) *fixture {
	t.Helper()
	f := assertPairedWorkbookBanks(t, book)
	ctx := context.Background()
	var examOutcomes, diagnosticOutcomes, shared int
	if err := f.pool.QueryRow(ctx, `
		WITH o AS (SELECT c.source_mode, c.name AS competency, p.name AS constituent, o.name AS outcome
		           FROM course_map_competencies c JOIN course_map_constituents p ON p.competency_id=c.id JOIN course_map_outcomes o ON o.constituent_id=p.id
		           WHERE c.revision=1)
		SELECT (SELECT count(*) FROM o WHERE source_mode='exam'), (SELECT count(*) FROM o WHERE source_mode='diagnostic'),
		       (SELECT count(*) FROM o e JOIN o d USING (competency, constituent, outcome) WHERE e.source_mode='exam' AND d.source_mode='diagnostic')`).
		Scan(&examOutcomes, &diagnosticOutcomes, &shared); err != nil {
		t.Fatal(err)
	}
	if examOutcomes == 0 || examOutcomes != diagnosticOutcomes || shared != examOutcomes {
		t.Fatalf("one sheet must give one structure: exam=%d diagnostic=%d shared=%d", examOutcomes, diagnosticOutcomes, shared)
	}
	return f
}

// unifiedMapBook is the paired book of pairedMapBook on one sheet, followed by
// a reference sheet that must not be read as the learning map.
func unifiedMapBook(t *testing.T) []byte {
	t.Helper()
	book := excelize.NewFile()
	defer book.Close()
	if err := book.SetSheetName(book.GetSheetName(0), "Карта компетенций"); err != nil {
		t.Fatal(err)
	}
	if _, err := book.NewSheet("Сводка"); err != nil {
		t.Fatal(err)
	}
	test := func(question, answer string) string {
		return question + "\n\n\n[]\n\n\nОтветьте устно.\n\n\n" + answer
	}
	rows := [][]interface{}{
		{"№", "Компетенция", "Составляющая", "Образовательный результат", "Что должно войти в тест", "Таксономия", "Уровень ALDs", "Важность", "Учебный текст",
			"Задание диагностики 1", "Критерии к заданию диагностики 1", "Задание экзамена 1", "Критерии к заданию экзамена 1"},
		{"1", "К1. Агент", "1.1 Описывает агента", "Называет части агента", "TRUE", "Помнить", "базовый", "4", "Агент воспринимает среду сенсорами.",
			test("Назовите части агента.", "Сенсоры и исполнительные механизмы."), "Эталон: сенсоры и исполнительные механизмы.", "Назовите части агента и приведите пример.", "Эталон: сенсоры и исполнительные механизмы."},
		{"2", "", "", "Объясняет рациональность", "TRUE", "Понимать", "", "3", "Рациональный агент максимизирует меру эффективности.",
			test("Объясните рациональность агента.", "Максимизирует ожидаемую меру эффективности."), "Эталон: выбирает действие, максимизирующее ожидаемую меру.", "Объясните, что такое рациональный агент.", "Эталон: максимизирует меру эффективности."},
		{"3", "", "", "Выбирает тип агента", "TRUE", "Анализировать", "средний", "5", "Типы агентов: рефлекторный, с моделью, целевой.",
			test("Выберите тип агента для робота-уборщика.", "Агент с моделью мира."), "Эталон: агент с моделью мира, потому что среда наблюдается частично.", "Какой агент нужен роботу-уборщику и почему?", "Эталон: агент с моделью мира.\nБалл (макс. 1): 1 — тип и причина."},
	}
	for i, row := range rows {
		if err := book.SetSheetRow("Карта компетенций", fmt.Sprintf("A%d", i+1), &row); err != nil {
			t.Fatal(err)
		}
	}
	if err := book.SetSheetRow("Сводка", "A1", &[]interface{}{"Составляющая", "Всего ОР"}); err != nil {
		t.Fatal(err)
	}
	buffer, err := book.WriteToBuffer()
	if err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

type mapTestQuestions struct{}

func (mapTestQuestions) Prepare(_ context.Context, data json.RawMessage) (json.RawMessage, error) {
	var shape map[string]json.RawMessage
	if err := json.Unmarshal(data, &shape); err != nil {
		return nil, err
	}
	var topics []map[string]json.RawMessage
	if err := json.Unmarshal(shape["topics"], &topics); err != nil {
		return nil, err
	}
	for _, topic := range topics {
		var variants []map[string]json.RawMessage
		if err := json.Unmarshal(topic["variants"], &variants); err != nil {
			return nil, err
		}
		for _, v := range variants {
			v["rephrase"], _ = json.Marshal("Тестовая переформулировка.")
		}
		topic["variants"], _ = json.Marshal(variants)
		topic["rephrase"], _ = json.Marshal("Тестовая переформулировка.")
	}
	shape["topics"], _ = json.Marshal(topics)
	return json.Marshal(shape)
}

func pairedMapBook(t *testing.T, mismatch bool) []byte {
	t.Helper()
	book := excelize.NewFile()
	defer book.Close()
	first := book.GetSheetName(0)
	if err := book.SetSheetName(first, "Карта компетенций"); err != nil {
		t.Fatal(err)
	}
	if _, err := book.NewSheet("Учебная карта"); err != nil {
		t.Fatal(err)
	}
	geekRows := [][]interface{}{
		{"№", "Субкомпетенция (Proficiency)", "Составляющая (Observe)", "Образовательный результат (Observe Behaviour)", "Уровень по Блуму", "Важность (1–5)", "Что должно войти в тест", "Задание (устная инструкция)", "Критерии оценивания"},
		{"1", "К1. Агент", "1.1 Описывает агента", "Называет части агента", "Помнить", "4", "TRUE", "Назовите части агента.", "Эталон: сенсоры и исполнительные механизмы."},
		{"2", "", "", "Объясняет рациональность", "Понимать", "3", "TRUE", "Объясните, что такое рациональный агент.", "Эталон: максимизирует меру эффективности."},
		{"3", "", "", "Выбирает тип агента", "Анализировать", "5", "TRUE", "Какой агент нужен роботу-уборщику и почему?", "Эталон: агент с моделью мира.\nБалл (макс. 1): 1 — тип и причина."},
	}
	learningRows := [][]interface{}{
		{"Компетенция", "Уровень темы", "Составляющая", "Образовательный результат", "Что должно войти в тест", "Таксономия", "Уровень ALDs", "Важность", "Раздел РПД · компетенции РПД", "Задание 1", "ОС"},
		{"К1. Агент", "базовый", "1.1 Описывает агента", "Называет части агента", "TRUE", "Знание", "базовый", "4", "", "Назовите части агента.\n\n\n[]\n\n\nОтветьте устно.\n\n\nСенсоры и исполнительные механизмы.", "Учебный эталон частей агента."},
		{"", "", "", "Объясняет рациональность", "TRUE", "Понимание", "базовый", "3", "", "Объясните рациональность агента.\n\n\n[]\n\n\nОтветьте устно.\n\n\nОн выбирает действие, максимизирующее ожидаемую меру эффективности.", "Учебный эталон рациональности."},
		{"", "", "", "Выбирает тип агента", "TRUE", "Анализ", "средний", "5", "", "Выберите тип агента для робота-уборщика.\n\n\n[]\n\n\nОтветьте устно.\n\n\nАгент с моделью мира учитывает состояние помещения.", "Учебный эталон типа агента."},
	}
	if mismatch {
		learningRows[1][0] = "К2. Другая компетенция"
		learningRows[1][2] = "Называет части модели"
	}
	for i, row := range geekRows {
		if err := book.SetSheetRow("Карта компетенций", fmt.Sprintf("A%d", i+1), &row); err != nil {
			t.Fatal(err)
		}
	}
	for i, row := range learningRows {
		if err := book.SetSheetRow("Учебная карта", fmt.Sprintf("A%d", i+1), &row); err != nil {
			t.Fatal(err)
		}
	}
	buffer, err := book.WriteToBuffer()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := csvparser.ParseXLSXSheet(buffer.Bytes(), 1)
	if err != nil {
		t.Fatalf("generated learning sheet: %+v", err)
	}
	if len(parsed.Tasks) != 3 || len(parsed.Outcomes) != 3 {
		t.Fatalf("generated learning sheet must contain three usable tasks and outcomes: tasks=%d outcomes=%d warnings=%+v", len(parsed.Tasks), len(parsed.Outcomes), parsed.Warnings)
	}
	for _, task := range parsed.Tasks {
		if task.VoiceInstruction == "" || task.ReferenceAnswer == "" {
			t.Fatalf("generated learning task is incomplete: %+v", task)
		}
	}
	return buffer.Bytes()
}

func pairedMapBookWithMissingGeekCriteria(t *testing.T) []byte {
	t.Helper()
	book, err := excelize.OpenReader(bytes.NewReader(pairedMapBook(t, false)))
	if err != nil {
		t.Fatal(err)
	}
	defer book.Close()
	rows, err := book.GetRows("Карта компетенций")
	if err != nil {
		t.Fatal(err)
	}
	// A second constituent keeps a full tree when the first one loses a task.
	for i, row := range rows[1:] {
		copied := slices.Clone(row)
		copied[0] = fmt.Sprint(i + len(rows))
		if copied[1] != "" {
			copied[1], copied[2] = "К2. Среда", "2.1 Описывает среду"
		}
		copied[3] = "Среда: " + copied[3]
		copied[7] = "Среда: " + copied[7]
		if err := book.SetSheetRow("Карта компетенций", fmt.Sprintf("A%d", i+len(rows)+1), &copied); err != nil {
			t.Fatal(err)
		}
	}
	if err := book.SetCellValue("Карта компетенций", "I2", ""); err != nil {
		t.Fatal(err)
	}
	buffer, err := book.WriteToBuffer()
	if err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestImportIgnoresSheetsAfterTheTwoMapSheets(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	admin := f.admin(t)
	book, err := excelize.OpenReader(bytes.NewReader(pairedMapBook(t, false)))
	if err != nil {
		t.Fatal(err)
	}
	defer book.Close()
	if _, err := book.NewSheet("Справочные данные"); err != nil {
		t.Fatal(err)
	}
	// Column 702 is far past the column limit of a map sheet.
	if err := book.SetCellValue("Справочные данные", "ZZ1", "справка"); err != nil {
		t.Fatal(err)
	}
	buffer, err := book.WriteToBuffer()
	if err != nil {
		t.Fatal(err)
	}
	response := uploadWithHeader(f, "/admin/subjects/subject:test/graph/competency-map", "file", "map.xlsx",
		"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", buffer.Bytes(), admin,
		"X-Expected-Learning-Revision", "0")
	if response.Code != http.StatusAccepted {
		t.Fatalf("import with a reference sheet: %d %s", response.Code, response.Body.String())
	}
}

func TestSkippingGeekTaskKeepsThePublishedGraphAndTheCourseStructure(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	admin := f.admin(t)
	response := uploadWithHeader(f, "/admin/subjects/subject:test/graph/competency-map", "file", "map.xlsx",
		"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", pairedMapBookWithMissingGeekCriteria(t), admin,
		"X-Expected-Learning-Revision", "0")
	if response.Code != http.StatusAccepted {
		t.Fatalf("import paired map with one fallback rubric: %d %s", response.Code, response.Body.String())
	}
	repo := newPairedGraphRepository(f.pool)
	projection, err := repo.MapGraph(context.Background(), "subject:test")
	if err != nil {
		t.Fatal(err)
	}
	job, err := repo.Claim(context.Background(), "skip-map-worker", time.Now())
	if err != nil || job.SubjectID != "subject:test" {
		t.Fatalf("claim Geek map build: %+v %v", job, err)
	}
	if ok, err := repo.Finish(context.Background(), job, projection, time.Now()); err != nil || !ok {
		t.Fatalf("publish initial Geek map: ok=%v err=%v", ok, err)
	}
	var before struct {
		Map struct {
			Fingerprint string `json:"fingerprint"`
		} `json:"map"`
		Graph json.RawMessage `json:"graph"`
	}
	adminSnapshot := f.request(http.MethodGet, "/admin/subjects/subject:test/graph", "", admin)
	if adminSnapshot.Code != http.StatusOK || json.Unmarshal(adminSnapshot.Body.Bytes(), &before) != nil || before.Map.Fingerprint == "" {
		t.Fatalf("read published graph and fingerprint: %d %s", adminSnapshot.Code, adminSnapshot.Body.String())
	}
	var publishedRevision *int64
	if err := f.pool.QueryRow(context.Background(), `SELECT graph, map_revision FROM course_graphs WHERE subject_id='subject:test'`).Scan(&before.Graph, &publishedRevision); err != nil {
		t.Fatal(err)
	}
	if publishedRevision == nil || *publishedRevision != 1 {
		t.Fatalf("published Geek graph has no map revision: %v", publishedRevision)
	}
	var originalOutcomes int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM course_map_outcomes o JOIN course_map_constituents s ON s.id=o.constituent_id JOIN course_map_competencies c ON c.id=s.competency_id WHERE c.revision=1 AND c.source_mode='diagnostic'`).Scan(&originalOutcomes); err != nil || originalOutcomes != 3 {
		t.Fatalf("canonical learning outcomes before filtering: %d err=%v", originalOutcomes, err)
	}
	request := httptest.NewRequest(http.MethodPost, "https://api.example"+examTestPath("/admin/subjects/subject:test/graph/competency-map/skip-missing-criteria"), nil)
	request.AddCookie(admin)
	request.Header.Set("X-Expected-Map-Fingerprint", before.Map.Fingerprint)
	filtered := httptest.NewRecorder()
	f.app.Handler().ServeHTTP(filtered, request)
	if filtered.Code != http.StatusAccepted {
		t.Fatalf("queue Geek task exclusion: %d %s", filtered.Code, filtered.Body.String())
	}
	var current struct {
		Status string `json:"status"`
		Map    struct {
			LearningRevision *int64 `json:"learning_revision"`
		} `json:"map"`
	}
	if err := json.Unmarshal(filtered.Body.Bytes(), &current); err != nil {
		t.Fatal(err)
	}
	if current.Status != "pending" || current.Map.LearningRevision == nil {
		t.Fatalf("filtered map should be preparing while learning stays active: %+v", current)
	}
	var published json.RawMessage
	if err := f.pool.QueryRow(context.Background(), `SELECT graph FROM course_graphs WHERE subject_id='subject:test'`).Scan(&published); err != nil {
		t.Fatal(err)
	}
	if string(published) != string(before.Graph) {
		t.Fatal("filtering changed the published Geek graph before its build")
	}
	oldGeek, ok, err := geekgraph.NewStored(f.pool).Current(context.Background(), "subject:test")
	if err != nil || !ok || len(oldGeek.Nodes) != 6 {
		t.Fatalf("published Geek must stay on its old six-topic version while preparing: nodes=%d ok=%v err=%v", len(oldGeek.Nodes), ok, err)
	}
	newProjection, err := repo.MapGraph(context.Background(), "subject:test")
	if err != nil {
		t.Fatal(err)
	}
	var shape struct {
		Topics []json.RawMessage `json:"topics"`
	}
	if err := json.Unmarshal(newProjection, &shape); err != nil || len(shape.Topics) != 3 {
		t.Fatalf("new Geek projection should omit the constituent left without a full tree: topics=%d err=%v", len(shape.Topics), err)
	}
	var preparedGraph map[string]json.RawMessage
	var topics []map[string]any
	if err := json.Unmarshal(newProjection, &preparedGraph); err != nil || json.Unmarshal(preparedGraph["topics"], &topics) != nil {
		t.Fatalf("decode filtered projection: %v", err)
	}
	var prepared []string
	for _, topic := range topics {
		variants, _ := topic["variants"].([]any)
		for _, item := range variants {
			variant := item.(map[string]any)
			id, _ := variant["task_id"].(string)
			if id == "" {
				t.Fatalf("filtered projection lost a task_id: %s", newProjection)
			}
			variant["rephrase"] = "prepared " + id
			prepared = append(prepared, id)
		}
	}
	if len(prepared) == 0 {
		t.Fatalf("filtered projection has no variants: %s", newProjection)
	}
	var err2 error
	if preparedGraph["topics"], err2 = json.Marshal(topics); err2 != nil {
		t.Fatal(err2)
	}
	preparedRaw, err := json.Marshal(preparedGraph)
	if err != nil {
		t.Fatal(err)
	}
	rebuildJob, err := repo.Claim(context.Background(), "skip-map-rebuild", time.Now())
	if err != nil || rebuildJob.SubjectID != "subject:test" {
		t.Fatalf("claim filtered map build: %+v %v", rebuildJob, err)
	}
	if ok, err := repo.Finish(context.Background(), rebuildJob, preparedRaw, time.Now()); err != nil || !ok {
		t.Fatalf("publish filtered Geek map: ok=%v err=%v", ok, err)
	}
	for _, id := range prepared {
		var rephrase string
		if err := f.pool.QueryRow(context.Background(), `SELECT rephrase FROM course_map_tasks WHERE id=$1 AND mode='exam'`, id).Scan(&rephrase); err != nil || rephrase != "prepared "+id {
			t.Fatalf("rephrase of task %s after filtering: %q err=%v", id, rephrase, err)
		}
	}
	var afterOutcomes int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM course_map_outcomes o JOIN course_map_constituents s ON s.id=o.constituent_id JOIN course_map_competencies c ON c.id=s.competency_id WHERE c.revision=1 AND c.source_mode='diagnostic'`).Scan(&afterOutcomes); err != nil || afterOutcomes != originalOutcomes {
		t.Fatalf("Geek filtering changed the canonical full structure: before=%d after=%d err=%v", originalOutcomes, afterOutcomes, err)
	}

}

func TestImportMapAnswersTooLargeForAnOversizedBody(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	admin := f.admin(t)
	big := bytes.Repeat([]byte{'x'}, 27<<20)
	w := upload(f, "/admin/subjects/subject:test/graph/competency-map", "file", "map.xlsx", "application/octet-stream", big, admin)
	if w.Code != http.StatusRequestEntityTooLarge || !strings.Contains(w.Body.String(), "PAYLOAD_TOO_LARGE") {
		t.Fatalf("27 MiB map: %d %s", w.Code, w.Body.String())
	}
}

func TestImportMapStartsForSharedCourseWithoutMaterials(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	admin := f.admin(t)
	created := f.request(http.MethodPost, "/admin/subjects", `{"name":"Курс только с картой"}`, admin)
	var course struct {
		ID string `json:"id"`
	}
	if created.Code != http.StatusCreated || json.Unmarshal(created.Body.Bytes(), &course) != nil || course.ID == "" {
		t.Fatalf("create shared course: %d %s", created.Code, created.Body.String())
	}
	var materialCount int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM subject_materials WHERE subject_id=$1`, course.ID).Scan(&materialCount); err != nil || materialCount != 0 {
		t.Fatalf("expected a new shared course without materials: count=%d err=%v", materialCount, err)
	}
	response := uploadWithHeader(f, "/admin/subjects/"+course.ID+"/graph/competency-map", "file", "map.xlsx", "application/octet-stream", mapBook(t), admin, "X-Expected-Learning-Revision", "0")
	if response.Code != http.StatusAccepted {
		t.Fatalf("map import for material-free course: %d %s", response.Code, response.Body.String())
	}
	var maps, builds int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM course_graph_maps WHERE subject_id=$1`, course.ID).Scan(&maps); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM course_graph_jobs WHERE subject_id=$1 AND source='competency_map'`, course.ID).Scan(&builds); err != nil {
		t.Fatal(err)
	}
	if maps != 1 || builds != 1 {
		t.Fatalf("map import should save its own input and queue map build: maps=%d builds=%d", maps, builds)
	}
}

func TestCourseGraphSnapshotReturnsEmptyLearningWarningsArray(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	admin := f.admin(t)
	response := f.request(http.MethodGet, "/admin/subjects/subject:test/graph", "", admin)
	if response.Code != http.StatusOK {
		t.Fatalf("read graph snapshot: %d %s", response.Code, response.Body.String())
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	raw, ok := body["learning_warnings"]
	if !ok || string(raw) != "[]" {
		t.Fatalf("empty learning_warnings = %s, want []", raw)
	}
}

func TestPairedMapActivatesLearningBeforeGeekPreparation(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	admin := f.admin(t)
	response := uploadWithHeader(f, "/admin/subjects/subject:test/graph/competency-map", "file", "map.xlsx", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", pairedMapBook(t, false), admin, "X-Expected-Learning-Revision", "0")
	if response.Code != http.StatusAccepted {
		t.Fatalf("paired map import: %d %s", response.Code, response.Body.String())
	}
	var active *int64
	if err := f.pool.QueryRow(context.Background(), `SELECT active_revision FROM learning_subject_state WHERE subject_id='subject:test'`).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active == nil {
		t.Fatal("learning map should activate after the paired version is saved")
	}
	var mapRevision *int64
	if err := f.pool.QueryRow(context.Background(), `SELECT map_revision FROM course_graph_maps WHERE subject_id='subject:test'`).Scan(&mapRevision); err != nil || mapRevision == nil || *mapRevision != *active {
		t.Fatalf("paired revisions differ: map=%v active=%v err=%v", mapRevision, active, err)
	}
	var examTasks int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM course_map_tasks task JOIN course_map_outcomes o ON o.id=task.outcome_id
JOIN course_map_constituents s ON s.id=o.constituent_id JOIN course_map_competencies c ON c.id=s.competency_id
WHERE task.mode='exam' AND c.revision=$1`, *mapRevision).Scan(&examTasks); err != nil || examTasks != 3 {
		t.Fatalf("exam tasks of the revision = %d err=%v", examTasks, err)
	}
	subjectsResponse := f.request(http.MethodGet, "/subjects", "", admin)
	var subjects []struct {
		ID            string `json:"id"`
		LearningReady bool   `json:"learning_ready"`
		ExamReady     bool   `json:"exam_ready"`
	}
	if subjectsResponse.Code != http.StatusOK || json.Unmarshal(subjectsResponse.Body.Bytes(), &subjects) != nil {
		t.Fatalf("read course readiness: %d %s", subjectsResponse.Code, subjectsResponse.Body.String())
	}
	if len(subjects) != 1 || subjects[0].ID != "subject:test" || !subjects[0].LearningReady || subjects[0].ExamReady {
		t.Fatalf("first paired map should make only learning ready before Geek preparation: %+v", subjects)
	}
	graphRepo := newPairedGraphRepository(f.pool)
	beforeGeek, err := graphRepo.Read(context.Background(), "subject:test")
	if err != nil || (beforeGeek.GraphSource != "" && beforeGeek.GraphSource != "none") || beforeGeek.Map == nil || beforeGeek.Map.LearningRevision == nil || *beforeGeek.Map.LearningRevision != *active || beforeGeek.GraphMapRevision != nil {
		t.Fatalf("versions/readiness before Geek preparation: %+v err=%v", beforeGeek, err)
	}
	job, err := graphRepo.Claim(context.Background(), "paired-map-worker", time.Now())
	if err != nil || job.SubjectID != "subject:test" {
		t.Fatalf("claim paired map: %+v %v", job, err)
	}
	graphWithRevision, err := graphRepo.MapGraph(context.Background(), "subject:test")
	if err != nil {
		t.Fatal(err)
	}
	var graphShape struct {
		Topics []struct {
			ID            string   `json:"id"`
			Requires      []string `json:"requires"`
			CompetencyID  string   `json:"competency_id"`
			ConstituentID string   `json:"constituent_id"`
		} `json:"topics"`
		Prerequisites []struct {
			Source string `json:"source"`
			Target string `json:"target"`
		} `json:"prerequisites"`
	}
	if err := json.Unmarshal(graphWithRevision, &graphShape); err != nil {
		t.Fatal(err)
	}
	canonicalIDs := map[string]bool{}
	for _, topic := range graphShape.Topics {
		var competencyID, constituentID string
		if err := f.pool.QueryRow(context.Background(), `SELECT c.id, s.id FROM course_map_outcomes o JOIN course_map_constituents s ON s.id=o.constituent_id JOIN course_map_competencies c ON c.id=s.competency_id WHERE o.id=$1 AND c.revision=$2`, topic.ID, *mapRevision).Scan(&competencyID, &constituentID); err != nil {
			t.Fatalf("Geek projection vertex %q must be a canonical learning outcome: %v", topic.ID, err)
		}
		if topic.CompetencyID != competencyID || topic.ConstituentID != constituentID {
			t.Fatalf("Geek grouping IDs do not point to canonical rows for %q: Geek=%q/%q canonical=%q/%q", topic.ID, topic.CompetencyID, topic.ConstituentID, competencyID, constituentID)
		}
		canonicalIDs[topic.ID] = true
	}
	for _, topic := range graphShape.Topics {
		for _, required := range topic.Requires {
			if !canonicalIDs[required] {
				t.Fatalf("Geek projection prerequisite %q is not a canonical vertex", required)
			}
		}
	}
	for _, relation := range graphShape.Prerequisites {
		if !canonicalIDs[relation.Source] || !canonicalIDs[relation.Target] {
			t.Fatalf("Geek projection relation must use canonical outcome IDs: %+v", relation)
		}
	}
	if ok, err := graphRepo.Finish(context.Background(), job, graphWithRevision, time.Now()); err != nil || !ok {
		t.Fatalf("publish paired map: ok=%v err=%v", ok, err)
	}
	if err := f.pool.QueryRow(context.Background(), `SELECT active_revision FROM learning_subject_state WHERE subject_id='subject:test'`).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active == nil || *active != *mapRevision {
		t.Fatalf("learning version changed with the Geek graph: active=%v map=%v", active, mapRevision)
	}
	learningSnapshot, err := competencypg.New(f.pool).Read(context.Background(), "subject:test")
	if err != nil || len(learningSnapshot.Competencies) != 1 || len(learningSnapshot.Competencies[0].Constituents) != 1 || len(learningSnapshot.Competencies[0].Constituents[0].Outcomes) != 3 {
		t.Fatalf("learning consumer did not read the paired map: %#v err=%v", learningSnapshot, err)
	}
	geekSnapshot, err := graphRepo.Read(context.Background(), "subject:test")
	if err != nil || geekSnapshot.GraphMapRevision == nil || *geekSnapshot.GraphMapRevision != *mapRevision {
		t.Fatalf("published revision: %v", err)
	}

}

func TestPairedMapImportUsesSingleConnectionAndRollsBackAtomically(t *testing.T) {
	t.Parallel()
	f := newFixtureWithMaxConns(t, 1)
	admin := f.admin(t)
	endpoint := "/admin/subjects/subject:test/graph/competency-map"
	importBook := func() *httptest.ResponseRecorder {
		t.Helper()
		return uploadWithHeader(f, endpoint, "file", "map.xlsx", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", pairedMapBook(t, false), admin, "X-Expected-Learning-Revision", "0")
	}

	if _, err := f.pool.Exec(context.Background(), `
CREATE FUNCTION fail_course_graph_job() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'forced build queue failure'; END $$;
CREATE TRIGGER fail_course_graph_job BEFORE INSERT ON course_graph_jobs FOR EACH ROW EXECUTE FUNCTION fail_course_graph_job();`); err != nil {
		t.Fatal(err)
	}
	response := importBook()
	if response.Code == http.StatusAccepted {
		t.Fatal("forced Geek queue failure was reported as a successful paired import")
	}
	var stateRows, imports, maps, jobs int
	if err := f.pool.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM learning_subject_state WHERE subject_id='subject:test'), (SELECT count(*) FROM course_map_imports WHERE subject_id='subject:test'), (SELECT count(*) FROM course_graph_maps WHERE subject_id='subject:test'), (SELECT count(*) FROM course_graph_jobs WHERE subject_id='subject:test')`).Scan(&stateRows, &imports, &maps, &jobs); err != nil {
		t.Fatal(err)
	}
	if stateRows != 0 || imports != 0 || maps != 0 || jobs != 0 {
		t.Fatalf("failed paired import left partial state: states=%d imports=%d maps=%d jobs=%d", stateRows, imports, maps, jobs)
	}
	if _, err := f.pool.Exec(context.Background(), `DROP TRIGGER fail_course_graph_job ON course_graph_jobs; DROP FUNCTION fail_course_graph_job()`); err != nil {
		t.Fatal(err)
	}
	response = importBook()
	if response.Code != http.StatusAccepted {
		t.Fatalf("paired map import with MaxConns=1: %d %s", response.Code, response.Body.String())
	}
	var active *int64
	if err := f.pool.QueryRow(context.Background(), `SELECT active_revision FROM learning_subject_state WHERE subject_id='subject:test'`).Scan(&active); err != nil || active == nil {
		t.Fatalf("learning projection was not committed: revision=%v err=%v", active, err)
	}
	stale := importBook()
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale expected learning revision: %d %s", stale.Code, stale.Body.String())
	}
	var afterStale *int64
	if err := f.pool.QueryRow(context.Background(), `SELECT active_revision FROM learning_subject_state WHERE subject_id='subject:test'`).Scan(&afterStale); err != nil || afterStale == nil || *afterStale != *active {
		t.Fatalf("stale CAS changed active learning revision: before=%v after=%v err=%v", *active, afterStale, err)
	}
}

func TestPairedMapReplacementActivatesLearningButKeepsOldGeekOnFailure(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	admin := f.admin(t)
	importMap := func() *int64 {
		t.Helper()
		var expectedRevision int64
		if err := f.pool.QueryRow(context.Background(), `SELECT COALESCE((SELECT active_revision FROM learning_subject_state WHERE subject_id='subject:test'), 0)`).Scan(&expectedRevision); err != nil {
			t.Fatal(err)
		}
		response := uploadWithHeader(f, "/admin/subjects/subject:test/graph/competency-map", "file", "map.xlsx", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", pairedMapBook(t, false), admin, "X-Expected-Learning-Revision", fmt.Sprint(expectedRevision))
		if response.Code != http.StatusAccepted {
			t.Fatalf("paired map import: %d %s", response.Code, response.Body.String())
		}
		var revision *int64
		if err := f.pool.QueryRow(context.Background(), `SELECT active_revision FROM learning_subject_state WHERE subject_id='subject:test'`).Scan(&revision); err != nil || revision == nil {
			t.Fatalf("learning version should be active as soon as the map is saved: revision=%v err=%v", revision, err)
		}
		return revision
	}
	graphRepo := postgres.New(f.pool)
	firstRevision := importMap()
	claimAndPublish := func(title string, revision int64) {
		t.Helper()
		job, err := graphRepo.Claim(context.Background(), "paired-map-worker", time.Now())
		if err != nil || job.SubjectID != "subject:test" {
			t.Fatalf("claim map build: %+v %v", job, err)
		}
		graph, err := json.Marshal(map[string]interface{}{
			"learning_revision": revision,
			"topics":            []map[string]string{{"id": title, "title": title}},
			"prerequisites":     []interface{}{},
			"hierarchy":         []interface{}{},
		})
		if err != nil {
			t.Fatal(err)
		}
		if ok, err := graphRepo.Finish(context.Background(), job, graph, time.Now()); err != nil || !ok {
			t.Fatalf("publish map build: ok=%v err=%v", ok, err)
		}
	}
	claimAndPublish("old-version", *firstRevision)
	secondRevision := importMap()
	var preservedOldImport int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM course_map_imports WHERE revision=$1 AND subject_id='subject:test'`, *firstRevision).Scan(&preservedOldImport); err != nil || preservedOldImport != 1 {
		t.Fatalf("paired replacement deleted old learning revision %d: count=%d err=%v", *firstRevision, preservedOldImport, err)
	}
	if *secondRevision == *firstRevision {
		t.Fatalf("replacement reused learning version %d", *firstRevision)
	}
	duringReplacement, err := graphRepo.Read(context.Background(), "subject:test")
	if err != nil || duringReplacement.Status != "pending" || duringReplacement.GraphMapRevision == nil || *duringReplacement.GraphMapRevision != *firstRevision || duringReplacement.Map == nil || duringReplacement.Map.LearningRevision == nil || *duringReplacement.Map.LearningRevision != *secondRevision {
		t.Fatalf("replacement should expose new learning and old Geek versions separately: %+v err=%v", duringReplacement, err)
	}
	now := time.Now()
	for attempt := 0; attempt < 3; attempt++ {
		job, err := graphRepo.Claim(context.Background(), "paired-map-worker", now)
		if err != nil || job.SubjectID != "subject:test" {
			t.Fatalf("claim failed replacement attempt %d: %+v err=%v", attempt+1, job, err)
		}
		if err := graphRepo.Fail(context.Background(), job, "Geek preparation failed", now); err != nil {
			t.Fatalf("fail replacement attempt %d: %v", attempt+1, err)
		}
		now = now.Add(time.Duration(30*(attempt+1)+1) * time.Second)
	}
	afterFailure, err := graphRepo.Read(context.Background(), "subject:test")
	if err != nil || afterFailure.Status != "failed" || afterFailure.GraphMapRevision == nil || *afterFailure.GraphMapRevision != *firstRevision || afterFailure.Map == nil || afterFailure.Map.LearningRevision == nil || *afterFailure.Map.LearningRevision != *secondRevision || !strings.Contains(string(afterFailure.Graph), "old-version") {
		t.Fatalf("failed replacement should keep old Geek graph and new learning map: %+v err=%v", afterFailure, err)
	}
	subjectsResponse := f.request(http.MethodGet, "/subjects", "", admin)
	var subjects []struct {
		ID            string `json:"id"`
		LearningReady bool   `json:"learning_ready"`
		ExamReady     bool   `json:"exam_ready"`
	}
	if subjectsResponse.Code != http.StatusOK || json.Unmarshal(subjectsResponse.Body.Bytes(), &subjects) != nil || len(subjects) != 1 || !subjects[0].LearningReady || !subjects[0].ExamReady {
		t.Fatalf("failed replacement should keep both independent modes available: %d %s %+v", subjectsResponse.Code, subjectsResponse.Body.String(), subjects)
	}
}

func TestPairedMapAcceptsDifferentSheetContents(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	admin := f.admin(t)
	response := uploadWithHeader(f, "/admin/subjects/subject:test/graph/competency-map", "file", "map.xlsx", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", pairedMapBook(t, true), admin, "X-Expected-Learning-Revision", "0")
	if response.Code != http.StatusAccepted {
		t.Fatalf("mismatched pair: %d %s", response.Code, response.Body.String())
	}
	var maps, imports int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM course_graph_maps WHERE subject_id='subject:test'`).Scan(&maps); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM course_map_imports WHERE subject_id='subject:test'`).Scan(&imports); err != nil {
		t.Fatal(err)
	}
	if maps != 1 || imports != 1 {
		t.Fatalf("mismatched import partially persisted: graph maps=%d learning imports=%d", maps, imports)
	}
}

func TestPairedMapReportsLearningSheetParserDetails(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	admin := f.admin(t)
	book := excelize.NewFile()
	defer book.Close()
	first := book.GetSheetName(0)
	if err := book.SetSheetName(first, "Карта компетенций"); err != nil {
		t.Fatal(err)
	}
	if _, err := book.NewSheet("Учебная карта"); err != nil {
		t.Fatal(err)
	}
	for _, sheet := range []string{"Карта компетенций", "Учебная карта"} {
		rows := [][]interface{}{
			{"№", "Субкомпетенция (Proficiency)", "Составляющая (Observe)", "Образовательный результат (Observe Behaviour)", "Уровень по Блуму", "Важность (1–5)", "Что должно войти в тест", "Задание (устная инструкция)", "Критерии оценивания"},
			{"1", "К1", "Составляющая", "ОР 1", "Помнить", "4", "TRUE", "Задание 1", "Критерий"},
			{"2", "", "", "ОР 2", "Понимать", "3", "TRUE", "Задание 2", "Критерий"},
			{"3", "", "", "ОР 3", "Применять", "3", "TRUE", "Задание 3", "Критерий"},
		}
		if sheet == "Учебная карта" {
			rows[0][0] = "Неизвестная колонка"
		}
		for i, row := range rows {
			if err := book.SetSheetRow(sheet, fmt.Sprintf("A%d", i+1), &row); err != nil {
				t.Fatal(err)
			}
		}
	}
	buffer, err := book.WriteToBuffer()
	if err != nil {
		t.Fatal(err)
	}
	response := uploadWithHeader(f, "/admin/subjects/subject:test/graph/competency-map", "file", "map.xlsx", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", buffer.Bytes(), admin, "X-Expected-Learning-Revision", "0")
	if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), `"path":"sheet:2/row:1"`) || !strings.Contains(response.Body.String(), `"details"`) {
		t.Fatalf("learning parser detail should identify sheet and row: %d %s", response.Code, response.Body.String())
	}
}

func TestLearningSheetParseFailurePreservesSafeDetailsAndCause(t *testing.T) {
	underlying := errors.New("internal parser diagnostic")
	parserFailure := fault.New(fault.Invalid, "CSV_INVALID", "Некорректная карта.").WithCause(underlying)
	parserFailure.Details = []fault.Detail{{Path: "row:7", Code: "CSV_INVALID", Message: "Важность: ожидается число."}}

	failure := learningSheetParseFailure(parserFailure, coursegraphentity.MapSheet{Number: 2})
	if !errors.Is(failure, underlying) {
		t.Fatalf("parser cause was not preserved: %v", failure)
	}
	if len(failure.Details) != 1 || failure.Details[0].Path != "sheet:2/row:7" || failure.Details[0].Message != "Важность: ожидается число." {
		t.Fatalf("safe parser detail lost sheet/row context: %+v", failure.Details)
	}
	// A projected sheet answers with its sheet and column in the book.
	unified := learningSheetParseFailure(parserFailure, coursegraphentity.MapSheet{Number: 1, Titles: map[string]string{"Важность": "Важность (1–5)"}})
	if unified.Details[0].Path != "sheet:1/row:7" || unified.Details[0].Message != "Важность (1–5): ожидается число." {
		t.Fatalf("unified detail not addressed in the book: %+v", unified.Details)
	}
	encoded, err := json.Marshal(failure)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), underlying.Error()) {
		t.Fatalf("internal cause leaked to the client: %s", encoded)
	}
}

// A real book weighs hundreds of KiB, far above the 32 KiB the API allows a
// JSON body; the map route lifts that limit up to the book's own.
func TestImportMapAcceptsABookLargerThanAJSONBody(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	admin := f.admin(t)
	book := mapBook(t)
	archive, err := zip.NewReader(bytes.NewReader(book), int64(len(book)))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, part := range archive.File {
		if err := w.Copy(part); err != nil {
			t.Fatal(err)
		}
	}
	// A picture of the book: stored as is and never read by the importer.
	picture, err := w.CreateHeader(&zip.FileHeader{Name: "xl/media/image1.png", Method: zip.Store})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.CopyN(picture, rand.Reader, 1<<20); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	res := uploadWithHeader(f, "/admin/subjects/subject:test/graph/competency-map", "file", "map.xlsx", "application/octet-stream", buf.Bytes(), admin, "X-Expected-Learning-Revision", "0")
	if res.Code != http.StatusAccepted {
		t.Fatalf("1 MiB map: %d %s", res.Code, res.Body.String())
	}
}

func TestPairedMapPreservesUnselectedOutcomesAndIgnoresReferenceSheets(t *testing.T) {
	book, err := excelize.OpenReader(bytes.NewReader(pairedMapBook(t, true)))
	if err != nil {
		t.Fatal(err)
	}
	defer book.Close()
	row := []any{"4", "К1. Агент", "1.1 Описывает агента", "Дополнительный ОР", "Применять", "3", "FALSE", "Вопрос для обучения.", "Эталон: контекст."}
	if err := book.SetSheetRow("Карта компетенций", "A5", &row); err != nil {
		t.Fatal(err)
	}
	if _, err := book.NewSheet("Справочные данные"); err != nil {
		t.Fatal(err)
	}
	if err := book.SetCellValue("Справочные данные", "ZZ1", "Не относится к карте"); err != nil {
		t.Fatal(err)
	}
	buffer, err := book.WriteToBuffer()
	if err != nil {
		t.Fatal(err)
	}
	f := assertPairedWorkbookBanks(t, buffer.Bytes())
	var outcomes, tasks, audio int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(DISTINCT o.id), count(t.id),count(t.audio_asset_id) FROM course_map_competencies c JOIN course_map_constituents p ON p.competency_id=c.id JOIN course_map_outcomes o ON o.constituent_id=p.id LEFT JOIN course_map_tasks t ON t.outcome_id=o.id AND t.mode='exam' WHERE c.revision=1 AND c.source_mode='exam'`).Scan(&outcomes, &tasks, &audio); err != nil {
		t.Fatal(err)
	}
	if outcomes != 4 || tasks != 4 || audio != 4 {
		t.Fatalf("exam bank: outcomes=%d tasks=%d queued audio=%d", outcomes, tasks, audio)
	}
}
