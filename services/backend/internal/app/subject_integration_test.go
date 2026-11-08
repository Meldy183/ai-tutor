package app

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"
)

func TestLearningReadinessRequiresThreeOutcomesInOneConstituent(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	student, _, _ := f.register(t, "constituent-readiness@example.edu")
	admin := f.admin(t)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `
INSERT INTO course_map_imports(revision,imported_at,imported_by,competency_count,constituent_count,outcome_count,task_count,source_format,source_headers,subject_id)
VALUES (1,1,(SELECT id FROM users WHERE role='admin' LIMIT 1),1,2,4,5,'paired','[]','subject:test');
`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `
INSERT INTO learning_subject_state(subject_id,active_revision) VALUES ('subject:test',1);
INSERT INTO course_map_competencies(id,name,revision) VALUES ('c1','Компетенция',1);
INSERT INTO course_map_constituents(id,competency_id,name) VALUES ('s1','c1','Первая'),('s2','c1','Вторая');
INSERT INTO course_map_outcomes(id,constituent_id,name,include_in_test,taxonomy_id,importance)
VALUES ('o1','s1','Первый',true,'taxonomy:knowledge',3),
       ('o2','s1','Второй',true,'taxonomy:understanding',3),
       ('o3','s2','Третий',true,'taxonomy:application',3),
       ('o4','s2','Четвёртый',true,'taxonomy:analysis',3);
INSERT INTO course_map_tasks(id,outcome_id,question,voice_instruction,reference_answer,created_at)
SELECT 'task-' || id,id,'Вопрос','Ответьте','Эталон',1 FROM course_map_outcomes;
INSERT INTO course_map_tasks(id,outcome_id,question,voice_instruction,reference_answer,created_at)
VALUES ('another-task','o1','Другой вопрос','Ответьте','Эталон',1);
`); err != nil {
		t.Fatal(err)
	}
	check := func(want bool) {
		t.Helper()
		response := f.request(http.MethodGet, "/learn/subjects", "", admin)
		var items []struct {
			Ready         bool `json:"ready"`
			LearningReady bool `json:"learning_ready"`
		}
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &items) != nil || len(items) != 1 || items[0].Ready != want || items[0].LearningReady != want {
			t.Fatalf("want learning_ready=%t: %d %s", want, response.Code, response.Body.String())
		}
		response = f.request(http.MethodGet, "/learn/subjects", "", student)
		var visible []json.RawMessage
		wantVisible := 0
		if want {
			wantVisible = 1
		}
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &visible) != nil || len(visible) != wantVisible {
			t.Fatalf("student catalog want ready=%t: %d %s", want, response.Code, response.Body.String())
		}
	}
	// Four outcomes and five tasks across two constituents cannot form a block.
	check(false)
	requireCode(t, f.keyed(http.MethodPost, "/learn/variants", "short-constituents", `{"subject_id":"subject:test"}`, student), http.StatusConflict, "NO_ELIGIBLE_COMPETENCIES")
	// Moving one outcome makes a complete block; the short constituent is skipped.
	if _, err := f.pool.Exec(ctx, `UPDATE course_map_outcomes SET constituent_id='s1' WHERE id='o3'`); err != nil {
		t.Fatal(err)
	}
	check(true)
	response := f.keyed(http.MethodPost, "/learn/variants", "complete-constituent", `{"subject_id":"subject:test"}`, student)
	if response.Code != http.StatusCreated {
		t.Fatalf("create variant for ready course: %d %s", response.Code, response.Body.String())
	}
	// A third outcome without a usable task must not make the course ready.
	if _, err := f.pool.Exec(ctx, `UPDATE course_map_tasks SET reference_answer='' WHERE outcome_id='o3'`); err != nil {
		t.Fatal(err)
	}
	check(false)
}

func TestSubjectCatalogPermissionsAndEmptySubjects(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	if _, err := f.pool.Exec(context.Background(), `DELETE FROM subjects WHERE id='subject:test'`); err != nil {
		t.Fatal(err)
	}
	student, _, _ := f.register(t, "subject-student@example.edu")
	admin := f.admin(t)

	requireCode(t, f.request(http.MethodGet, "/subjects", ""), http.StatusUnauthorized, "UNAUTHORIZED")
	requireCode(t, f.request(http.MethodPost, "/admin/subjects", `{"name":"Пустой предмет"}`), http.StatusUnauthorized, "UNAUTHORIZED")
	requireCode(t, f.request(http.MethodPost, "/admin/subjects", `{"name":"   "}`, admin), http.StatusUnprocessableEntity, "VALIDATION_ERROR")
	studentCatalog := f.request(http.MethodGet, "/subjects", "", student)
	if studentCatalog.Code != http.StatusOK || studentCatalog.Body.String() != "[]\n" {
		t.Fatalf("empty student catalog: %d %s", studentCatalog.Code, studentCatalog.Body.String())
	}
	requireCode(t, f.request(http.MethodPost, "/admin/subjects", `{"name":"Пустой предмет"}`, student), http.StatusForbidden, "FORBIDDEN")

	created := f.request(http.MethodPost, "/admin/subjects", `{"name":"  Пустой предмет  "}`, admin)
	if created.Code != http.StatusCreated {
		t.Fatalf("create subject: %d %s", created.Code, created.Body.String())
	}
	var value struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Ready       bool   `json:"ready"`
		Preparation struct {
			Exam struct {
				State  string  `json:"state"`
				Source *string `json:"source"`
				Stale  bool    `json:"stale"`
			} `json:"exam"`
			Learn struct {
				State string `json:"state"`
			} `json:"learn"`
		} `json:"preparation"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &value); err != nil {
		t.Fatal(err)
	}
	if value.ID == "" || value.Name != "Пустой предмет" || value.Ready {
		t.Fatalf("created subject: %+v", value)
	}
	if value.Preparation.Exam.State != "empty" || value.Preparation.Exam.Source != nil || value.Preparation.Exam.Stale || value.Preparation.Learn.State != "empty" {
		t.Fatalf("created subject preparation: %+v", value.Preparation)
	}
	var persistedName string
	if err := f.pool.QueryRow(context.Background(), `SELECT name FROM subjects WHERE id=$1`, value.ID).Scan(&persistedName); err != nil || persistedName != value.Name {
		t.Fatalf("persisted subject name=%q err=%v", persistedName, err)
	}

	adminCatalog := f.request(http.MethodGet, "/subjects", "", admin)
	var items []struct {
		ID          string          `json:"id"`
		Name        string          `json:"name"`
		Ready       bool            `json:"ready"`
		Preparation json.RawMessage `json:"preparation"`
	}
	if adminCatalog.Code != http.StatusOK || json.Unmarshal(adminCatalog.Body.Bytes(), &items) != nil {
		t.Fatalf("admin catalog: %d %s", adminCatalog.Code, adminCatalog.Body.String())
	}
	if len(items) != 1 || items[0].Ready || len(items[0].Preparation) == 0 {
		t.Fatalf("admin should see the created empty subject: %+v", items)
	}
	compatible := f.request(http.MethodPost, "/learn/admin/subjects", `{"name":"Совместимый курс"}`, admin)
	var compatibilityResponse struct {
		Preparation struct {
			Exam struct {
				State string `json:"state"`
			} `json:"exam"`
			Learn struct {
				State string `json:"state"`
			} `json:"learn"`
		} `json:"preparation"`
	}
	if compatible.Code != http.StatusCreated || json.Unmarshal(compatible.Body.Bytes(), &compatibilityResponse) != nil || compatibilityResponse.Preparation.Exam.State != "empty" || compatibilityResponse.Preparation.Learn.State != "empty" {
		t.Fatalf("compatibility create should return empty preparation: %d %s", compatible.Code, compatible.Body.String())
	}
}

func TestCourseCreatedOnceAppearsInBothDirectionCatalogs(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	if _, err := f.pool.Exec(context.Background(), `DELETE FROM subjects WHERE id='subject:test'`); err != nil {
		t.Fatal(err)
	}
	admin := f.admin(t)
	student, _, _ := f.register(t, "shared-course-student@example.edu")
	created := f.request(http.MethodPost, "/admin/subjects", `{"name":"Общий курс"}`, admin)
	if created.Code != http.StatusCreated {
		t.Fatalf("create common course: %d %s", created.Code, created.Body.String())
	}
	var course struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &course); err != nil || course.ID == "" {
		t.Fatalf("created course response: %s (%v)", created.Body.String(), err)
	}
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO course_graph_maps(subject_id,filename,sheet,fingerprint,graph,uploaded_at)
VALUES ($1,'exam.xlsx','Экзамен','exam-v1','{"marker":"exam-before-learning-import"}',1)`, course.ID); err != nil {
		t.Fatal(err)
	}
	type catalogSubject struct {
		ID            string          `json:"id"`
		Ready         bool            `json:"ready"`
		LearningReady bool            `json:"learning_ready"`
		ExamReady     bool            `json:"exam_ready"`
		Preparation   json.RawMessage `json:"preparation"`
	}
	readCatalog := func(path string, actor *http.Cookie) []catalogSubject {
		t.Helper()
		response := f.request(http.MethodGet, path, "", actor)
		var items []catalogSubject
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &items) != nil {
			t.Fatalf("catalog %s: %d %s", path, response.Code, response.Body.String())
		}
		return items
	}
	var firstPreparation json.RawMessage
	for _, path := range []string{"/subjects", "/learn/subjects"} {
		items := readCatalog(path, admin)
		if len(items) != 1 || items[0].ID != course.ID {
			t.Fatalf("admin catalog %s should contain the single shared course %q: %+v", path, course.ID, items)
		}
		if items[0].LearningReady || items[0].ExamReady || items[0].Ready {
			t.Fatalf("direction readiness is not independent in %s: %+v", path, items[0])
		}
		if len(items[0].Preparation) == 0 {
			t.Fatalf("admin preparation missing in %s", path)
		}
		var preparation struct {
			Exam struct {
				State  string  `json:"state"`
				Source *string `json:"source"`
			} `json:"exam"`
			Learn struct {
				State string `json:"state"`
			} `json:"learn"`
		}
		if err := json.Unmarshal(items[0].Preparation, &preparation); err != nil {
			t.Fatalf("decode preparation: %v", err)
		}
		if preparation.Exam.State != "needs_build" || preparation.Exam.Source == nil || *preparation.Exam.Source != "competency_map" || preparation.Learn.State != "empty" {
			t.Fatalf("newly imported exam map preparation: %+v", preparation)
		}
		if firstPreparation == nil {
			firstPreparation = items[0].Preparation
		} else if string(firstPreparation) != string(items[0].Preparation) {
			t.Fatalf("preparation differs between catalogs: %s vs %s", firstPreparation, items[0].Preparation)
		}
	}
	if items := readCatalog("/subjects", student); len(items) != 0 {
		t.Fatalf("empty course must not appear in exam catalog: %+v", items)
	}
	if items := readCatalog("/learn/subjects", student); len(items) != 0 {
		t.Fatalf("empty course must not appear in learning catalog: %+v", items)
	}
	mapCSV, err := os.ReadFile("testdata/learning-ready-map.csv")
	if err != nil {
		t.Fatal(err)
	}
	missingImport := importCourseMap(t, f, "subject-does-not-exist", mapCSV, 0, admin)
	requireCode(t, missingImport, http.StatusNotFound, "SUBJECT_NOT_FOUND")
	var missingMapCount int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM course_map_imports WHERE subject_id='subject-does-not-exist'`).Scan(&missingMapCount); err != nil || missingMapCount != 0 {
		t.Fatalf("import for missing course created global learning data: count=%d err=%v", missingMapCount, err)
	}
	imported := importCourseMap(t, f, course.ID, mapCSV, 0, admin)
	if imported.Code != http.StatusAccepted {
		t.Fatalf("learning map upload for common course: %d %s", imported.Code, imported.Body.String())
	}
	var importedCounts string
	if err := f.pool.QueryRow(context.Background(), `SELECT format('c=%s k=%s o=%s t=%s', competency_count, constituent_count, outcome_count, task_count)
FROM course_map_imports WHERE subject_id=$1`, course.ID).Scan(&importedCounts); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"/subjects", "/learn/subjects"} {
		items := readCatalog(path, admin)
		if len(items) != 1 || items[0].ID != course.ID || !items[0].LearningReady || items[0].ExamReady || items[0].Ready != (path == "/learn/subjects") {
			t.Fatalf("both readiness states should be present after the learning import, %s: %+v", path, items)
		}
	}
	if items := readCatalog("/learn/subjects", student); len(items) != 1 || items[0].ID != course.ID {
		t.Fatalf("learning-ready shared course should appear in learning catalog: %+v", items)
	} else if len(items[0].Preparation) != 0 {
		t.Fatalf("student catalog must omit preparation: %+v", items[0])
	}
	if items := readCatalog("/subjects", student); len(items) != 0 {
		t.Fatalf("learning readiness must not make exam course ready: %+v", items)
	}
	var examFilename string
	if err := f.pool.QueryRow(context.Background(), `SELECT filename FROM course_graph_maps WHERE subject_id=$1`, course.ID).Scan(&examFilename); err != nil || examFilename != "map.xlsx" {
		t.Fatalf("one import saves the Geek sheet too: filename=%q err=%v", examFilename, err)
	}
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO course_graphs(subject_id,source,fingerprint,graph,updated_at)
VALUES ($1,'competency_map','exam-v1','{"marker":"exam-after-learning-import"}',1)`, course.ID); err != nil {
		t.Fatal(err)
	}
	if items := readCatalog("/subjects", student); len(items) != 1 || items[0].ID != course.ID {
		t.Fatalf("published exam graph should independently enable exam catalog: %+v", items)
	} else if len(items[0].Preparation) != 0 {
		t.Fatalf("student exam catalog must omit preparation: %+v", items[0])
	}
	if items := readCatalog("/learn/subjects", student); len(items) != 1 || items[0].ID != course.ID {
		t.Fatalf("exam graph change must not affect learning readiness: %+v", items)
	}
	var count int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM course_map_imports WHERE subject_id=$1`, course.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("exam graph change removed learning import count=%d err=%v", count, err)
	}
	if err := f.pool.QueryRow(context.Background(), "SELECT count(*) FROM subjects WHERE id=$1", course.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("shared course row count=%d err=%v", count, err)
	}
}

// Without the graph fixture a course is ready only with its own published graph.
func TestSubjectCatalogShowsOnlyReadySubjectsToStudents(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	student, _, _ := f.register(t, "subject-ready-student@example.edu")
	admin := f.admin(t)
	created := f.request(http.MethodPost, "/admin/subjects", `{"name":"Пока без материалов"}`, admin)
	if created.Code != http.StatusCreated {
		t.Fatalf("create empty subject: %d %s", created.Code, created.Body.String())
	}
	setStatus := func(status string) {
		t.Helper()
		if _, err := f.pool.Exec(context.Background(), `INSERT INTO subject_materials(id, subject_id, filename, content_type, s3_raw_key, s3_parsed_key, status, created_at, updated_at)
VALUES ('material:ready', 'subject:test', 'lecture.pdf', 'application/pdf', 'raw', 'parsed', $1, 1, 1)
ON CONFLICT (id) DO UPDATE SET status = EXCLUDED.status`, status); err != nil {
			t.Fatal(err)
		}
	}
	studentCatalog := func() []struct {
		ID    string `json:"id"`
		Ready bool   `json:"ready"`
	} {
		t.Helper()
		response := f.request(http.MethodGet, "/subjects", "", student)
		var items []struct {
			ID    string `json:"id"`
			Ready bool   `json:"ready"`
		}
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &items) != nil {
			t.Fatalf("student catalog: %d %s", response.Code, response.Body.String())
		}
		return items
	}
	if items := studentCatalog(); len(items) != 0 {
		t.Fatalf("subjects without materials are not ready: %+v", items)
	}
	setStatus("failed")
	if items := studentCatalog(); len(items) != 0 {
		t.Fatalf("a failed material does not make a subject ready: %+v", items)
	}
	setStatus("parsed")
	if items := studentCatalog(); len(items) != 0 {
		t.Fatalf("a parsed material without a graph does not make a subject ready: %+v", items)
	}
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO course_graphs(subject_id, fingerprint, graph, updated_at) VALUES ('subject:test', 'f', '{}', 1)`); err != nil {
		t.Fatal(err)
	}
	if items := studentCatalog(); len(items) != 1 || items[0].ID != "subject:test" || !items[0].Ready {
		t.Fatalf("student catalog should contain only the subject with a published graph: %+v", items)
	}
	adminCatalog := f.request(http.MethodGet, "/subjects", "", admin)
	var adminItems []struct {
		ID    string `json:"id"`
		Ready bool   `json:"ready"`
	}
	if adminCatalog.Code != http.StatusOK || json.Unmarshal(adminCatalog.Body.Bytes(), &adminItems) != nil || len(adminItems) != 2 {
		t.Fatalf("admin catalog: %d %s", adminCatalog.Code, adminCatalog.Body.String())
	}
}

func TestSubjectWithAGraphOfACompetencyMapIsReadyWithoutMaterials(t *testing.T) {
	t.Parallel()
	f := newFixtureWithConfig(t, testConfig())
	if _, err := f.pool.Exec(context.Background(), `DELETE FROM subjects WHERE id='subject:test'`); err != nil {
		t.Fatal(err)
	}
	student, _, _ := f.register(t, "subject-map-student@example.edu")
	admin := f.admin(t)
	created := f.request(http.MethodPost, "/admin/subjects", `{"name":"Только карта"}`, admin)
	var value struct {
		ID    string `json:"id"`
		Ready bool   `json:"ready"`
	}
	// Without a default graph a new course has nothing to examine by.
	if created.Code != http.StatusCreated || json.Unmarshal(created.Body.Bytes(), &value) != nil || value.Ready {
		t.Fatalf("a new subject is not ready: %d %s", created.Code, created.Body.String())
	}
	catalog := func() []struct {
		ID    string `json:"id"`
		Ready bool   `json:"ready"`
	} {
		response := f.request(http.MethodGet, "/subjects", "", student)
		var items []struct {
			ID    string `json:"id"`
			Ready bool   `json:"ready"`
		}
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &items) != nil {
			t.Fatalf("student catalog: %d %s", response.Code, response.Body.String())
		}
		return items
	}
	if items := catalog(); len(items) != 0 {
		t.Fatalf("student catalog before the graph: %+v", items)
	}
	publishGraph(t, f, value.ID, []byte(`{"nodes":[{"id":"a","title":"Агент"}]}`))
	if items := catalog(); len(items) != 1 || items[0].ID != value.ID || !items[0].Ready {
		t.Fatalf("student catalog should contain the subject of the map without materials: %+v", items)
	}
}

func TestAdminCatalogProjectsMaterialPreparationAndKeepsOldGraphAvailable(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	admin := f.admin(t)
	created := f.request(http.MethodPost, "/admin/subjects", `{"name":"Статус материалов"}`, admin)
	var course struct {
		ID string `json:"id"`
	}
	if created.Code != http.StatusCreated || json.Unmarshal(created.Body.Bytes(), &course) != nil || course.ID == "" {
		t.Fatalf("create subject: %d %s", created.Code, created.Body.String())
	}
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `INSERT INTO subject_materials(id,subject_id,filename,content_type,s3_raw_key,s3_parsed_key,status,created_at,updated_at)
VALUES ('material:status',$1,'lecture.pdf','application/pdf','raw','parsed','pending',1,1)`, course.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO material_ocr_jobs(material_id,updated_at) VALUES ('material:status',1)`); err != nil {
		t.Fatal(err)
	}
	type status struct {
		ID          string `json:"id"`
		ExamReady   bool   `json:"exam_ready"`
		Preparation struct {
			Exam struct {
				State  string  `json:"state"`
				Source *string `json:"source"`
				Stale  bool    `json:"stale"`
			} `json:"exam"`
		} `json:"preparation"`
	}
	read := func() status {
		t.Helper()
		response := f.request(http.MethodGet, "/subjects", "", admin)
		var items []status
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &items) != nil {
			t.Fatalf("admin catalog: %d %s", response.Code, response.Body.String())
		}
		for _, item := range items {
			if item.ID == course.ID {
				return item
			}
		}
		t.Fatalf("materials course missing from catalog: %s", response.Body.String())
		return status{}
	}
	check := func(want string, ready, stale bool) {
		t.Helper()
		got := read()
		if got.Preparation.Exam.State != want || got.ExamReady != ready || got.Preparation.Exam.Stale != stale {
			t.Fatalf("preparation got state=%s exam_ready=%t stale=%t, want %s/%t/%t", got.Preparation.Exam.State, got.ExamReady, got.Preparation.Exam.Stale, want, ready, stale)
		}
	}
	check("needs_metadata", false, false)
	if _, err := f.pool.Exec(ctx, `UPDATE subject_materials SET week=1,kind='lecture' WHERE id='material:status'`); err != nil {
		t.Fatal(err)
	}
	check("queued", false, false)
	if _, err := f.pool.Exec(ctx, `UPDATE material_ocr_jobs SET status='processing' WHERE material_id='material:status'`); err != nil {
		t.Fatal(err)
	}
	check("processing", false, false)
	if _, err := f.pool.Exec(ctx, `UPDATE material_ocr_jobs SET status='ready' WHERE material_id='material:status'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO material_cleaning_jobs(material_id,status,updated_at) VALUES ('material:status','processing',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE subject_materials SET status='parsed' WHERE id='material:status'`); err != nil {
		t.Fatal(err)
	}
	check("processing", false, false)
	if _, err := f.pool.Exec(ctx, `UPDATE material_cleaning_jobs SET status='failed' WHERE material_id='material:status'`); err != nil {
		t.Fatal(err)
	}
	check("failed", false, false)
	if _, err := f.pool.Exec(ctx, `UPDATE material_cleaning_jobs SET status='ready',s3_report_key='report' WHERE material_id='material:status'`); err != nil {
		t.Fatal(err)
	}
	check("needs_build", false, false)
	if _, err := f.pool.Exec(ctx, `INSERT INTO course_graph_jobs(subject_id,source,fingerprint,status,error_message,updated_at)
SELECT $1,'materials',fingerprint,'pending','temporary failure before retry',2 FROM course_graph_inputs WHERE subject_id=$1`, course.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO course_graphs(subject_id,source,fingerprint,graph,updated_at) VALUES ($1,'materials','old','{}',1)`, course.ID); err != nil {
		t.Fatal(err)
	}
	check("queued", true, true)
}
