package app

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"

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
func mapBook(t *testing.T) []byte {
	t.Helper()
	rows := [][]string{
		{"№", "Субкомпетенция (Proficiency)", "Составляющая (Observe)", "Образовательный результат (Observe Behaviour)",
			"Уровень по Блуму", "Важность (1–5)", "Что должно войти в тест", "Задание (устная инструкция)", "Критерии оценивания"},
		{"1", "К1. Агент", "1.1 Описывает агента", "Называет части агента", "Помнить", "4", "TRUE", "Назовите части агента.", "Эталон: сенсоры и исполнительные механизмы."},
		{"2", "", "", "Объясняет рациональность", "Понимать", "3", "TRUE", "Объясните, что такое рациональный агент.", "Эталон: максимизирует меру эффективности."},
		{"3", "", "", "Выбирает тип агента", "Анализировать", "5", "TRUE", "Какой агент нужен роботу-уборщику и почему?", "Эталон: агент с моделью мира.\nБалл (макс. 1): 1 — тип и причина."},
	}
	var sheet strings.Builder
	sheet.WriteString(`<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>`)
	for i, row := range rows {
		fmt.Fprintf(&sheet, `<row r="%d">`, i+1)
		for j, value := range row {
			var text bytes.Buffer
			if err := xml.EscapeText(&text, []byte(value)); err != nil {
				t.Fatal(err)
			}
			fmt.Fprintf(&sheet, `<c r="%c%d" t="inlineStr"><is><t>%s</t></is></c>`, 'A'+j, i+1, text.String())
		}
		sheet.WriteString(`</row>`)
	}
	sheet.WriteString(`</sheetData></worksheet>`)
	parts := map[string]string{
		"xl/workbook.xml": `<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">` +
			`<sheets><sheet name="Карта компетенций" sheetId="1" r:id="rId1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
			`<Relationship Id="rId1" Target="worksheets/sheet1.xml"/></Relationships>`,
		"xl/worksheets/sheet1.xml": sheet.String(),
	}
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, body := range parts {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func newPairedGraphRepository(pool *pgxpool.Pool) *pairedCourseGraphRepository {
	return &pairedCourseGraphRepository{Repository: postgres.New(pool), pool: pool, learning: competencypg.New(pool)}
}

// Set REAL_COMPETENCY_MAP_PATH to run the supplied workbook through the real
// PostgreSQL import and both mode readers. The fixture is isolated per test and
// the workbook itself stays outside the repository.
func TestRealPairedWorkbookUsesCanonicalGraphInBothModes(t *testing.T) {
	path := os.Getenv("REAL_COMPETENCY_MAP_PATH")
	if path == "" {
		t.Skip("REAL_COMPETENCY_MAP_PATH is not set")
	}
	book, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	f := newFixture(t)
	admin := f.admin(t)
	response := uploadWithHeader(f, "/admin/subjects/subject:test/graph/competency-map", "file", "map.xlsx",
		"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", book, admin, "X-Expected-Learning-Revision", "0")
	if response.Code != http.StatusAccepted {
		t.Fatalf("import real workbook: %d %s", response.Code, response.Body.String())
	}
	graphRepo := newPairedGraphRepository(f.pool)
	mapData, err := graphRepo.MapGraph(context.Background(), "subject:test")
	if err != nil {
		t.Fatal(err)
	}
	var mapProjection struct {
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
	if err := json.Unmarshal(mapData, &mapProjection); err != nil {
		t.Fatal(err)
	}
	if len(mapProjection.Topics) != 15 {
		t.Fatalf("real Geek projection topics=%d, want 15", len(mapProjection.Topics))
	}
	var storedMetadata json.RawMessage
	var canonicalTopicCount, edgeCount, examTaskCount int
	if err := f.pool.QueryRow(context.Background(), `SELECT graph FROM course_graph_maps WHERE subject_id='subject:test'`).Scan(&storedMetadata); err != nil {
		t.Fatal(err)
	}
	var storedMap map[string]json.RawMessage
	if err := json.Unmarshal(storedMetadata, &storedMap); err != nil {
		t.Fatal(err)
	}
	if _, hasTopics := storedMap["topics"]; hasTopics {
		t.Fatal("course_graph_maps must store metadata only; canonical vertices are read from learning_*")
	}
	if err := f.pool.QueryRow(context.Background(), `SELECT
		(SELECT count(*) FROM course_graph_exam_topics WHERE map_revision=(SELECT learning_revision FROM course_graph_maps WHERE subject_id='subject:test')),
		(SELECT count(*) FROM learning_graph_prerequisites WHERE map_revision=(SELECT learning_revision FROM course_graph_maps WHERE subject_id='subject:test')),
		(SELECT count(*) FROM course_graph_exam_tasks WHERE map_revision=(SELECT learning_revision FROM course_graph_maps WHERE subject_id='subject:test'))`).Scan(&canonicalTopicCount, &edgeCount, &examTaskCount); err != nil {
		t.Fatal(err)
	}
	if canonicalTopicCount != 15 || edgeCount != len(mapProjection.Prerequisites) || examTaskCount < canonicalTopicCount {
		t.Fatalf("versioned canonical relations and mode tasks: Geek topics=%d edges=%d tasks=%d", canonicalTopicCount, edgeCount, examTaskCount)
	}
	learningSnapshot, err := competencypg.New(f.pool).Read(context.Background(), "subject:test")
	if err != nil {
		t.Fatal(err)
	}
	canonicalIDs := map[string]bool{}
	canonicalParents := map[string][2]string{}
	var outcomeCount, taskCount int
	for _, competency := range learningSnapshot.Competencies {
		for _, constituent := range competency.Constituents {
			for _, outcome := range constituent.Outcomes {
				outcomeCount++
				canonicalIDs[outcome.ID] = true
				canonicalParents[outcome.ID] = [2]string{competency.ID, constituent.ID}
				taskCount += len(outcome.Tasks)
			}
		}
	}
	if outcomeCount != 24 || taskCount != 72 {
		t.Fatalf("learning sheet import counts: outcomes=%d tasks=%d, want 24/72", outcomeCount, taskCount)
	}
	for _, topic := range mapProjection.Topics {
		if !canonicalIDs[topic.ID] {
			t.Fatalf("Geek map projection vertex %q is not a learning graph vertex", topic.ID)
		}
		if canonicalParents[topic.ID] != [2]string{topic.CompetencyID, topic.ConstituentID} {
			t.Fatalf("Geek and learning readers use different grouping relations for %q: Geek=%q/%q learning=%v", topic.ID, topic.CompetencyID, topic.ConstituentID, canonicalParents[topic.ID])
		}
		for _, required := range topic.Requires {
			if !canonicalIDs[required] {
				t.Fatalf("Geek map projection prerequisite %q is not a learning graph vertex", required)
			}
		}
	}
	for _, edge := range mapProjection.Prerequisites {
		if !canonicalIDs[edge.Source] || !canonicalIDs[edge.Target] {
			t.Fatalf("Geek map projection edge does not use canonical vertices: %+v", edge)
		}
	}
	selectedIDs := make(map[string]bool, len(mapProjection.Topics))
	for _, topic := range mapProjection.Topics {
		selectedIDs[topic.ID] = true
	}
	unselectedCanonicalOutcomes := 0
	for id := range canonicalIDs {
		if !selectedIDs[id] {
			unselectedCanonicalOutcomes++
		}
	}
	if unselectedCanonicalOutcomes != 9 {
		t.Fatalf("all 24 learning outcomes must remain in the common canonical graph; %d were outside the Geek consumer projection, want 9", unselectedCanonicalOutcomes)
	}
	job, err := graphRepo.Claim(context.Background(), "real-map-worker", time.Now())
	if err != nil || job.SubjectID != "subject:test" {
		t.Fatalf("claim Geek build from real map: %+v %v", job, err)
	}
	var versioned map[string]json.RawMessage
	if err := json.Unmarshal(mapData, &versioned); err != nil {
		t.Fatal(err)
	}
	var mapRevision int64
	if err := f.pool.QueryRow(context.Background(), `SELECT learning_revision FROM course_graph_maps WHERE subject_id='subject:test'`).Scan(&mapRevision); err != nil {
		t.Fatal(err)
	}
	versioned["learning_revision"], _ = json.Marshal(mapRevision)
	mapData, err = json.Marshal(versioned)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := graphRepo.Finish(context.Background(), job, mapData, time.Now()); err != nil || !ok {
		t.Fatalf("publish Geek projection from real map: ok=%v err=%v", ok, err)
	}
	var publishedMetadata json.RawMessage
	if err := f.pool.QueryRow(context.Background(), `SELECT graph FROM course_graphs WHERE subject_id='subject:test'`).Scan(&publishedMetadata); err != nil {
		t.Fatal(err)
	}
	var publishedShape map[string]json.RawMessage
	if err := json.Unmarshal(publishedMetadata, &publishedShape); err != nil {
		t.Fatal(err)
	}
	if _, hasTopics := publishedShape["topics"]; hasTopics {
		t.Fatal("course_graphs must store only the published canonical revision pointer, not a second vertex graph")
	}
	examGraph, ok, err := geekgraph.NewStored(f.pool).Current(context.Background(), "subject:test")
	if err != nil || !ok {
		t.Fatalf("Geek reader did not read real map: ok=%v err=%v", ok, err)
	}
	if len(examGraph.Nodes) != len(mapProjection.Topics) {
		t.Fatalf("Geek nodes=%d, map projection topics=%d", len(examGraph.Nodes), len(mapProjection.Topics))
	}
	for _, node := range examGraph.Nodes {
		if !canonicalIDs[node.ID] {
			t.Fatalf("Geek runtime vertex %q is not a learning graph vertex", node.ID)
		}
		if canonicalParents[node.ID] != [2]string{node.CompetencyID, node.ConstituentID} {
			t.Fatalf("Geek runtime grouping relations differ for %q: Geek=%q/%q learning=%v", node.ID, node.CompetencyID, node.ConstituentID, canonicalParents[node.ID])
		}
		for _, required := range node.Prerequisites {
			if !canonicalIDs[required] {
				t.Fatalf("Geek runtime prerequisite %q is not a learning graph vertex", required)
			}
		}
	}
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
	if err := book.SetCellValue("Карта компетенций", "I2", ""); err != nil {
		t.Fatal(err)
	}
	buffer, err := book.WriteToBuffer()
	if err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestSkippingGeekTaskCreatesNewProjectionWithoutChangingPublishedVersionOrCanonicalGraph(t *testing.T) {
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
	var oldPointer struct {
		ExamRevision int64 `json:"exam_revision"`
	}
	if err := f.pool.QueryRow(context.Background(), `SELECT graph FROM course_graphs WHERE subject_id='subject:test'`).Scan(&before.Graph); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(before.Graph, &oldPointer); err != nil || oldPointer.ExamRevision <= 0 {
		t.Fatalf("published Geek pointer has no version: %+v err=%v", oldPointer, err)
	}
	var originalOutcomes int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM learning_outcomes o JOIN learning_constituents s ON s.id=o.constituent_id JOIN learning_competencies c ON c.id=s.competency_id WHERE c.revision=1`).Scan(&originalOutcomes); err != nil || originalOutcomes != 3 {
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
		t.Fatalf("filtered map should be preparing as a new Geek projection while learning stays active: %+v", current)
	}
	var currentExamRevision int64
	if err := f.pool.QueryRow(context.Background(), `SELECT (graph->>'exam_revision')::bigint FROM course_graph_maps WHERE subject_id='subject:test'`).Scan(&currentExamRevision); err != nil {
		t.Fatal(err)
	}
	if currentExamRevision == oldPointer.ExamRevision {
		t.Fatalf("filtering mutated published Geek version %d", oldPointer.ExamRevision)
	}
	oldGeek, ok, err := geekgraph.NewStored(f.pool).Current(context.Background(), "subject:test")
	if err != nil || !ok || len(oldGeek.Nodes) != 3 {
		t.Fatalf("published Geek must stay on its old three-topic version while preparing: nodes=%d ok=%v err=%v", len(oldGeek.Nodes), ok, err)
	}
	newProjection, err := repo.MapGraph(context.Background(), "subject:test")
	if err != nil {
		t.Fatal(err)
	}
	var shape struct {
		Topics []json.RawMessage `json:"topics"`
	}
	if err := json.Unmarshal(newProjection, &shape); err != nil || len(shape.Topics) != 2 {
		t.Fatalf("new Geek projection should omit one fallback-only topic: topics=%d err=%v", len(shape.Topics), err)
	}
	var afterOutcomes int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM learning_outcomes o JOIN learning_constituents s ON s.id=o.constituent_id JOIN learning_competencies c ON c.id=s.competency_id WHERE c.revision=1`).Scan(&afterOutcomes); err != nil || afterOutcomes != originalOutcomes {
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
	response := upload(f, "/admin/subjects/"+course.ID+"/graph/competency-map", "file", "map.xlsx", "application/octet-stream", mapBook(t), admin)
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
	var active, pending *int64
	if err := f.pool.QueryRow(context.Background(), `SELECT active_revision,pending_revision FROM learning_subject_state WHERE subject_id='subject:test'`).Scan(&active, &pending); err != nil {
		t.Fatal(err)
	}
	if active == nil || pending != nil {
		t.Fatalf("learning map should activate after the paired version is saved: active=%v pending=%v", active, pending)
	}
	var mapRevision *int64
	if err := f.pool.QueryRow(context.Background(), `SELECT learning_revision FROM course_graph_maps WHERE subject_id='subject:test'`).Scan(&mapRevision); err != nil || mapRevision == nil || *mapRevision != *active {
		t.Fatalf("paired revisions differ: map=%v active=%v pending=%v err=%v", mapRevision, active, pending, err)
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
	var projection map[string]json.RawMessage
	if err := json.Unmarshal(graphWithRevision, &projection); err != nil {
		t.Fatal(err)
	}
	projection["learning_revision"], _ = json.Marshal(*mapRevision)
	graphWithRevision, err = json.Marshal(projection)
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
	canonicalParents := map[string][2]string{}
	for _, topic := range graphShape.Topics {
		var competencyID, constituentID string
		if err := f.pool.QueryRow(context.Background(), `SELECT c.id, s.id FROM learning_outcomes o JOIN learning_constituents s ON s.id=o.constituent_id JOIN learning_competencies c ON c.id=s.competency_id WHERE o.id=$1 AND c.revision=$2`, topic.ID, *mapRevision).Scan(&competencyID, &constituentID); err != nil {
			t.Fatalf("Geek projection vertex %q must be a canonical learning outcome: %v", topic.ID, err)
		}
		if topic.CompetencyID != competencyID || topic.ConstituentID != constituentID {
			t.Fatalf("Geek grouping IDs do not point to canonical rows for %q: Geek=%q/%q canonical=%q/%q", topic.ID, topic.CompetencyID, topic.ConstituentID, competencyID, constituentID)
		}
		canonicalIDs[topic.ID] = true
		canonicalParents[topic.ID] = [2]string{competencyID, constituentID}
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
	if err := f.pool.QueryRow(context.Background(), `SELECT active_revision,pending_revision FROM learning_subject_state WHERE subject_id='subject:test'`).Scan(&active, &pending); err != nil {
		t.Fatal(err)
	}
	if active == nil || *active != *mapRevision || pending != nil {
		t.Fatalf("learning version did not activate with Geek graph: active=%v pending=%v map=%v", active, pending, mapRevision)
	}
	learningSnapshot, err := competencypg.New(f.pool).Read(context.Background(), "subject:test")
	if err != nil || len(learningSnapshot.Competencies) != 1 || len(learningSnapshot.Competencies[0].Constituents) != 1 || len(learningSnapshot.Competencies[0].Constituents[0].Outcomes) != 3 {
		t.Fatalf("learning consumer did not read the paired map: %#v err=%v", learningSnapshot, err)
	}
	learningIDs := map[string]bool{}
	learningParents := map[string][2]string{}
	for _, outcome := range learningSnapshot.Competencies[0].Constituents[0].Outcomes {
		learningIDs[outcome.ID] = true
		learningParents[outcome.ID] = [2]string{learningSnapshot.Competencies[0].ID, learningSnapshot.Competencies[0].Constituents[0].ID}
	}
	geekSnapshot, err := graphRepo.Read(context.Background(), "subject:test")
	if err != nil || geekSnapshot.GraphSource != "competency_map" || len(geekSnapshot.Graph) == 0 || geekSnapshot.GraphMapRevision == nil || *geekSnapshot.GraphMapRevision != *mapRevision {
		t.Fatalf("Geek consumer did not read the paired graph: %+v err=%v", geekSnapshot, err)
	}
	if len(graphShape.Topics) != len(learningIDs) {
		t.Fatalf("Geek projection topics=%d, learning canonical vertices=%d", len(graphShape.Topics), len(learningIDs))
	}
	for id := range canonicalIDs {
		if !learningIDs[id] {
			t.Fatalf("Geek and learning consumers read different canonical vertex IDs: %q", id)
		}
		if canonicalParents[id] != learningParents[id] {
			t.Fatalf("Geek and learning consumers read different canonical grouping relations for %q: Geek=%v learning=%v", id, canonicalParents[id], learningParents[id])
		}
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
	if err := f.pool.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM learning_subject_state WHERE subject_id='subject:test'), (SELECT count(*) FROM learning_map_imports WHERE subject_id='subject:test'), (SELECT count(*) FROM course_graph_maps WHERE subject_id='subject:test'), (SELECT count(*) FROM course_graph_jobs WHERE subject_id='subject:test')`).Scan(&stateRows, &imports, &maps, &jobs); err != nil {
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

func TestGeekBuildFinishesAfterIndependentLearningMapImport(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	admin := f.admin(t)
	response := uploadWithHeader(f, "/admin/subjects/subject:test/graph/competency-map", "file", "map.xlsx", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", pairedMapBook(t, false), admin, "X-Expected-Learning-Revision", "0")
	if response.Code != http.StatusAccepted {
		t.Fatalf("paired map import: %d %s", response.Code, response.Body.String())
	}
	var revision int64
	if err := f.pool.QueryRow(context.Background(), `SELECT active_revision FROM learning_subject_state WHERE subject_id='subject:test'`).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	var actorID string
	if err := f.pool.QueryRow(context.Background(), `SELECT id FROM users WHERE email='admin@example.edu'`).Scan(&actorID); err != nil {
		t.Fatal(err)
	}
	parsed, err := csvparser.ParseXLSXSheet(pairedMapBook(t, false), 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := competencypg.New(f.pool).Replace(context.Background(), "subject:test", actorID, parsed, time.Now().Unix(), &revision); err != nil {
		t.Fatalf("independent learning import: %v", err)
	}
	graphRepo := newPairedGraphRepository(f.pool)
	job, err := graphRepo.Claim(context.Background(), "paired-after-learning-import", time.Now())
	if err != nil || job.SubjectID != "subject:test" {
		t.Fatalf("claim paired Geek build: %+v %v", job, err)
	}
	graph, err := graphRepo.MapGraph(context.Background(), "subject:test")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := graphRepo.Finish(context.Background(), job, graph, time.Now()); err != nil || !ok {
		t.Fatalf("Geek build after separate learning import was rejected: ok=%v err=%v", ok, err)
	}
	var mapRevision *int64
	if err := f.pool.QueryRow(context.Background(), `SELECT graph_map_revision FROM (SELECT map_revision AS graph_map_revision FROM course_graphs WHERE subject_id='subject:test') published`).Scan(&mapRevision); err != nil || mapRevision == nil || *mapRevision != revision {
		t.Fatalf("Geek must retain its source canonical revision after an independent learning import: map_revision=%v expected=%d err=%v", mapRevision, revision, err)
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
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM learning_map_imports WHERE revision=$1 AND subject_id='subject:test'`, *firstRevision).Scan(&preservedOldImport); err != nil || preservedOldImport != 1 {
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

func TestPairedMapStructureMismatchStoresNeitherSide(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	admin := f.admin(t)
	response := uploadWithHeader(f, "/admin/subjects/subject:test/graph/competency-map", "file", "map.xlsx", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", pairedMapBook(t, true), admin, "X-Expected-Learning-Revision", "0")
	if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), "COMPETENCY_MAP_STRUCTURE_MISMATCH") {
		t.Fatalf("mismatched pair: %d %s", response.Code, response.Body.String())
	}
	var maps, imports int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM course_graph_maps WHERE subject_id='subject:test'`).Scan(&maps); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM learning_map_imports WHERE subject_id='subject:test'`).Scan(&imports); err != nil {
		t.Fatal(err)
	}
	if maps != 0 || imports != 0 {
		t.Fatalf("mismatched import partially persisted: graph maps=%d learning imports=%d", maps, imports)
	}
}

func TestLearningImportRejectsPairedWorkbookToPreventIndependentPublication(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	admin := f.admin(t)
	response := upload(f, "/learn/admin/subjects/subject:test/competency-map/import", "file", "map.xlsx", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", pairedMapBook(t, false), admin)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "UNIFIED_COMPETENCY_MAP_REQUIRED") {
		t.Fatalf("paired learning import should require unified upload: %d %s", response.Code, response.Body.String())
	}
	var imports, maps int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM learning_map_imports WHERE subject_id='subject:test'`).Scan(&imports); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM course_graph_maps WHERE subject_id='subject:test'`).Scan(&maps); err != nil {
		t.Fatal(err)
	}
	if imports != 0 || maps != 0 {
		t.Fatalf("rejected independent import stored data: learning=%d graph=%d", imports, maps)
	}
}

func TestLearningImportRejectsPairedWorkbookRegardlessOfMime(t *testing.T) {
	t.Parallel()
	for _, media := range []string{"application/vnd.ms-excel", "application/csv", "text/csv"} {
		t.Run(media, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			admin := f.admin(t)
			response := upload(f, "/learn/admin/subjects/subject:test/competency-map/import", "file", "map.xlsx", media, pairedMapBook(t, false), admin)
			if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "UNIFIED_COMPETENCY_MAP_REQUIRED") {
				t.Fatalf("mislabeled paired workbook should require unified upload: %d %s", response.Code, response.Body.String())
			}
			var imports int
			if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM learning_map_imports WHERE subject_id='subject:test'`).Scan(&imports); err != nil {
				t.Fatal(err)
			}
			if imports != 0 {
				t.Fatalf("rejected paired workbook persisted %d learning maps", imports)
			}
		})
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

	failure := learningSheetParseFailure(parserFailure)
	if !errors.Is(failure, underlying) {
		t.Fatalf("parser cause was not preserved: %v", failure)
	}
	if len(failure.Details) != 1 || failure.Details[0].Path != "sheet:2/row:7" || failure.Details[0].Message != "Важность: ожидается число." {
		t.Fatalf("safe parser detail lost sheet/row context: %+v", failure.Details)
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
	res := upload(f, "/admin/subjects/subject:test/graph/competency-map", "file", "map.xlsx", "application/octet-stream", buf.Bytes(), admin)
	if res.Code != http.StatusAccepted {
		t.Fatalf("1 MiB map: %d %s", res.Code, res.Body.String())
	}
}
