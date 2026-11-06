package app

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"

	"github.com/AskarKasimov/ai-tutor/services/backend/internal/features/competency/infrastructure/csvparser"
	competencypg "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/competency/infrastructure/postgres"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/features/coursegraph/infrastructure/postgres"
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
		{"Ком", "Сост", "ОР", "Что должно войти в тест", "Уровень ОР", "Таксономия", "Важность темы", "Уровень ALDs", "Задание 1", "Критерии 1"},
		{"К1. Агент", "1.1 Описывает агента", "Называет части агента", "TRUE", "Помнить", "Знание", "4", "базовый", "Учебное: назовите части агента.", "Учебный эталон частей агента."},
		{"", "", "Объясняет рациональность", "TRUE", "Понимать", "Понимание", "3", "базовый", "Учебное: объясните рациональность.", "Учебный эталон рациональности."},
		{"", "", "Выбирает тип агента", "TRUE", "Анализировать", "Анализ", "5", "средний", "Учебное: выберите тип агента.", "Учебный эталон типа агента."},
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
	if _, err := csvparser.ParseXLSXSheet(buffer.Bytes(), 1); err != nil {
		t.Fatalf("generated learning sheet: %+v", err)
	}
	return buffer.Bytes()
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

func TestPairedMapActivatesLearningAndGeekTogether(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	admin := f.admin(t)
	response := upload(f, "/admin/subjects/subject:test/graph/competency-map", "file", "map.xlsx", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", pairedMapBook(t, false), admin)
	if response.Code != http.StatusAccepted {
		t.Fatalf("paired map import: %d %s", response.Code, response.Body.String())
	}
	var active, pending *int64
	if err := f.pool.QueryRow(context.Background(), `SELECT active_revision,pending_revision FROM learning_subject_state WHERE subject_id='subject:test'`).Scan(&active, &pending); err != nil {
		t.Fatal(err)
	}
	if active != nil || pending == nil {
		t.Fatalf("learning map activated before Geek preparation: active=%v pending=%v", active, pending)
	}
	var mapRevision *int64
	if err := f.pool.QueryRow(context.Background(), `SELECT learning_revision FROM course_graph_maps WHERE subject_id='subject:test'`).Scan(&mapRevision); err != nil || mapRevision == nil || *mapRevision != *pending {
		t.Fatalf("paired revisions differ: map=%v pending=%v err=%v", mapRevision, pending, err)
	}
	graphRepo := postgres.New(f.pool)
	job, err := graphRepo.Claim(context.Background(), "paired-map-worker", time.Now())
	if err != nil || job.SubjectID != "subject:test" {
		t.Fatalf("claim paired map: %+v %v", job, err)
	}
	if ok, err := graphRepo.Finish(context.Background(), job, json.RawMessage(`{"topics":[],"prerequisites":[],"hierarchy":[]}`), time.Now()); err != nil || !ok {
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
	geekSnapshot, err := graphRepo.Read(context.Background(), "subject:test")
	if err != nil || geekSnapshot.GraphSource != "competency_map" || len(geekSnapshot.Graph) == 0 {
		t.Fatalf("Geek consumer did not read the paired graph: %+v err=%v", geekSnapshot, err)
	}
}

func TestPairedMapStructureMismatchStoresNeitherSide(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	admin := f.admin(t)
	response := upload(f, "/admin/subjects/subject:test/graph/competency-map", "file", "map.xlsx", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", pairedMapBook(t, true), admin)
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
