package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/coursegraph"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/material"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/user"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/shared/fault"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/shared/logx"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

type requestRepo struct {
	snapshot  coursegraph.Snapshot
	queued    bool
	published []PublishedGraph
	mapGraph  json.RawMessage
}

func (r requestRepo) Published(context.Context) ([]PublishedGraph, error) {
	return r.published, nil
}

func (r requestRepo) Claim(context.Context, string, time.Time) (coursegraph.Job, error) {
	return coursegraph.Job{}, nil
}
func (r requestRepo) Finish(context.Context, coursegraph.Job, json.RawMessage, time.Time) (bool, error) {
	return false, nil
}
func (r requestRepo) Fail(context.Context, coursegraph.Job, string, time.Time) error { return nil }
func (r requestRepo) Read(context.Context, string) (coursegraph.Snapshot, error) {
	return r.snapshot, nil
}
func (r requestRepo) Request(context.Context, string, time.Time) (bool, error) { return r.queued, nil }
func (r requestRepo) RetryMap(context.Context, string, string, time.Time) (bool, error) {
	return r.queued, nil
}
func (r requestRepo) SaveMap(context.Context, string, string, string, json.RawMessage, time.Time) (bool, error) {
	return r.queued, nil
}
func (r requestRepo) SaveMapIfCurrent(context.Context, string, string, string, json.RawMessage, string, time.Time) (bool, error) {
	return r.queued, nil
}
func (r requestRepo) MapGraph(context.Context, string) (json.RawMessage, error) {
	return r.mapGraph, nil
}

type requestMaterials []material.Material

func (m requestMaterials) List(context.Context, string) ([]material.Material, error) { return m, nil }

type progressStorage struct{}

func (progressStorage) Get(context.Context, string) ([]byte, error) { return []byte(`{}`), nil }

// progressBuilder reports progress and checks the snapshot shows it while
// the build runs.
type progressBuilder struct {
	t      *testing.T
	worker *Worker
	id     string
}

func (b *progressBuilder) Build(ctx context.Context, buildID string, _ []coursegraph.Material, report func(coursegraph.BuildProgress)) (json.RawMessage, error) {
	b.id = buildID
	report(coursegraph.BuildProgress{Stage: "attach", StageIndex: 4, StageCount: 5, Done: 1, Total: 3})
	snapshot, err := b.worker.Read(ctx, user.User{Role: user.Admin}, "subject")
	if err != nil || snapshot.Progress == nil || snapshot.Progress.Stage != "attach" || snapshot.Progress.Done != 1 {
		b.t.Errorf("snapshot during the build = %+v, %v", snapshot.Progress, err)
	}
	return json.RawMessage(`{"version":"course-graph-v1","topics":[{}],"prerequisites":[],"quality":{"acyclic":true}}`), nil
}

func TestSnapshotShowsBuildProgressOnlyWhileTheBuildRuns(t *testing.T) {
	week := int32(1)
	kind := material.Kind("lecture")
	items := requestMaterials{{ID: "m", Week: &week, Kind: &kind, S3ParsedKey: "parsed", Cleaning: &material.Cleaning{Status: "ready", S3ReportKey: "report"}}}
	builder := &progressBuilder{t: t}
	w := New(requestRepo{snapshot: coursegraph.Snapshot{Status: "processing"}}, items, progressStorage{}, builder, nil, nil)
	builder.worker = w
	// Review of #135: a reclaimed lease keeps the attempt number while the
	// previous call may still run in course-graph, so two calls of the same
	// course and attempt must not share a build ID.
	ids := map[string]bool{}
	for _, token := range []string{"lease-1", "lease-2"} {
		if err := w.process(context.Background(), coursegraph.Job{SubjectID: "subject", Token: token, Attempts: 2}); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(builder.id, "subject#") || strings.Contains(builder.id, token) {
			t.Fatalf("build ID = %q: the lease token must not leave the API", builder.id)
		}
		ids[builder.id] = true
	}
	if len(ids) != 2 {
		t.Fatalf("build IDs = %v: each call needs its own", ids)
	}
	snapshot, err := w.Read(context.Background(), user.User{Role: user.Admin}, "subject")
	if err != nil || snapshot.Progress != nil {
		t.Fatalf("snapshot after the build = %+v, %v", snapshot.Progress, err)
	}
}

type reportStorage map[string]string

func (s reportStorage) Get(_ context.Context, key string) ([]byte, error) { return []byte(s[key]), nil }

type capturingBuilder struct{ materials []coursegraph.Material }

func (b *capturingBuilder) Build(_ context.Context, _ string, materials []coursegraph.Material, _ func(coursegraph.BuildProgress)) (json.RawMessage, error) {
	b.materials = materials
	return json.RawMessage(`{"version":"course-graph-v1","topics":[{}],"prerequisites":[],"quality":{"acyclic":true}}`), nil
}

func TestBuildReusesOnlyCardsOfTheCurrentQuestionPolicy(t *testing.T) {
	// Old cards may ask about all outcomes; rebuild them under the
	// single-concept question policy.
	items := requestMaterials{
		{ID: "old", S3ParsedKey: "parsed", Cleaning: &material.Cleaning{Status: "ready", S3ReportKey: "old"}},
		{ID: "new", S3ParsedKey: "parsed", Cleaning: &material.Cleaning{Status: "ready", S3ReportKey: "new"}},
	}
	storage := reportStorage{
		"old": `{"topics_version":"topics-v3","topics":[{"question":"Нарисуй график функции и вычисли производную."}]}`,
		"new": `{"topics_version":"topics-v4","topics":[{"question":"Объясни, зачем нужна производная."}]}`,
	}
	builder := &capturingBuilder{}
	w := New(requestRepo{}, items, storage, builder, nil, nil)
	if err := w.process(context.Background(), coursegraph.Job{SubjectID: "subject"}); err != nil {
		t.Fatal(err)
	}
	if len(builder.materials) != 2 || builder.materials[0].Topics != nil || builder.materials[1].Topics == nil {
		t.Fatalf("materials = %+v: only topics-v4 cards may be reused", builder.materials)
	}
}

func TestRequestReportsClassifiedMaterialIDsWithoutPollingAsProcessing(t *testing.T) {
	week := int32(1)
	items := requestMaterials{{ID: "needs-classification", Status: material.StatusParsed,
		Week: &week, Cleaning: &material.Cleaning{Status: "ready"}}}
	w := New(requestRepo{snapshot: coursegraph.Snapshot{Status: "ready"}}, items, nil, nil, nil, nil)
	_, err := w.Request(context.Background(), user.User{Role: user.Admin}, "subject")
	if err == nil {
		t.Fatal("expected incomplete metadata error")
	}
	failure, ok := err.(interface{ Error() string })
	if !ok || failure.Error() != "MATERIAL_METADATA_INCOMPLETE" {
		t.Fatalf("error = %v", err)
	}
}

func TestRequestKeepsConflictPrecedenceDuringActiveBuild(t *testing.T) {
	w := New(requestRepo{snapshot: coursegraph.Snapshot{Status: "processing"}}, nil, nil, nil, nil, nil)
	_, err := w.Request(context.Background(), user.User{Role: user.Admin}, "subject")
	if err == nil || err.Error() != "GRAPH_BUILD_IN_PROGRESS" {
		t.Fatalf("error = %v", err)
	}
}

func TestTopicQuestionsAreThePreparedQuestionsOfTheGraph(t *testing.T) {
	graph := json.RawMessage(`{"topics":[{"question":" Почему ACO находит короткий путь? ","rephrase":" Как муравьи находят путь? "},{"question":""},{"title":"без вопроса","rephrase":"Сирота?"}]}`)
	if got := TopicQuestions(graph); len(got) != 2 || got[0] != "Почему ACO находит короткий путь?" || got[1] != "Как муравьи находят путь?" {
		t.Fatalf("questions = %q", got)
	}
	variants := json.RawMessage(`{"topics":[{"question":"Устно?","rephrase":"Проще?","variants":[{"question":"Устно?","rephrase":"Проще?"},{"question":"Академически?","rephrase":"Проще академически?"}]}]}`)
	if got := TopicQuestions(variants); strings.Join(got, "|") != "Устно?|Проще?|Академически?|Проще академически?" {
		t.Fatalf("questions of the variants = %q", got)
	}
	if got := TopicQuestions(json.RawMessage(`not json`)); len(got) != 0 {
		t.Fatalf("questions of an invalid graph = %q", got)
	}
}

type snapshotRepo struct {
	Repository
	snapshot coursegraph.Snapshot
}

func (r snapshotRepo) Read(context.Context, string) (coursegraph.Snapshot, error) {
	return r.snapshot, nil
}

var admin = user.User{ID: "admin", Role: user.Admin}

func TestReadShowsTheBuildOnceItIsPublished(t *testing.T) {
	built := json.RawMessage(`{"topics":[{"id":"x","title":"Собранная тема"}]}`)
	w := New(snapshotRepo{snapshot: coursegraph.Snapshot{Status: "ready", Graph: built, GraphSource: coursegraph.SourceMaterials}}, requestMaterials{{ID: "m"}}, nil, nil, nil, nil)
	got, err := w.Read(context.Background(), admin, "subject")
	if err != nil || got.GraphSource != coursegraph.SourceBuilt || string(got.Graph) != string(built) {
		t.Fatalf("snapshot = %+v, %v", got, err)
	}
}

func TestReadWithoutAGraphHasNone(t *testing.T) {
	w := New(snapshotRepo{snapshot: coursegraph.Snapshot{Status: "not_requested", Graph: json.RawMessage("null")}}, nil, nil, nil, nil, nil)
	got, err := w.Read(context.Background(), admin, "subject")
	if err != nil || got.GraphSource != coursegraph.SourceNone || got.Graph != nil {
		t.Fatalf("snapshot = %+v, %v", got, err)
	}
}

func TestReadDropsABuildWhoseCourseLostItsMaterials(t *testing.T) {
	// Geek ignores a published graph of materials once the course has none.
	built := json.RawMessage(`{"topics":[{"id":"x","title":"Собранная тема"}]}`)
	w := New(snapshotRepo{snapshot: coursegraph.Snapshot{Status: "ready", Graph: built, GraphSource: coursegraph.SourceMaterials}}, requestMaterials{}, nil, nil, nil, nil)
	got, err := w.Read(context.Background(), admin, "subject")
	if err != nil || got.GraphSource != coursegraph.SourceNone || got.Graph != nil {
		t.Fatalf("snapshot = %+v, %v", got, err)
	}
}

func TestReadShowsAGraphOfACompetencyMapWithoutMaterials(t *testing.T) {
	built := json.RawMessage(`{"version":"competency-map-v1","topics":[{"id":"x","title":"ОР"}]}`)
	w := New(snapshotRepo{snapshot: coursegraph.Snapshot{Status: "ready", Graph: built, GraphSource: coursegraph.SourceMap}}, requestMaterials{}, nil, nil, nil, nil)
	got, err := w.Read(context.Background(), admin, "subject")
	if err != nil || got.GraphSource != coursegraph.SourceMap || string(got.Graph) != string(built) {
		t.Fatalf("snapshot = %+v, %v", got, err)
	}
}

// mapRepo records an imported map.
type mapRepo struct {
	requestRepo
	saved  *json.RawMessage
	sheet  *string
	queued bool
}

func (r mapRepo) SaveMap(_ context.Context, _, _, sheet string, graph json.RawMessage, _ time.Time) (bool, error) {
	if !r.queued {
		return false, nil
	}
	*r.saved, *r.sheet = graph, sheet
	return true, nil
}

func mapRows(competencies ...string) [][]string {
	rows := [][]string{{"№", "Субкомпетенция (Proficiency)", "Составляющая (Observe)", "Образовательный результат (Observe Behaviour)",
		"Уровень по Блуму", "Важность (1–5)", "Что должно войти в тест", "Задание (устная инструкция)", "Критерии оценивания"}}
	for _, c := range competencies {
		rows = append(rows,
			[]string{"1", c, "1.1 Составляющая", "Называет", "Помнить", "3", "TRUE", "Назовите.", "Эталон: имя.\nБалл (макс. 1): 1 — назвал."},
			[]string{"2", "", "", "Объясняет", "Понимать", "4", "TRUE", "Объясните.", "Эталон: причина."},
			[]string{"3", "", "", "Анализирует", "Анализировать", "5", "TRUE", "Сравните.", "Эталон: сравнение.\nАналитическая рубрика (макс. 2):\nК1 «Сравнение»: 1 — есть."},
		)
	}
	return rows
}

func TestImportMapQueuesTheGraphOfTheMap(t *testing.T) {
	var saved json.RawMessage
	var sheet string
	read := func([]byte) ([]coursegraph.MapSheet, error) {
		return []coursegraph.MapSheet{{Name: "Карта", Rows: mapRows("К1. Агенты")}, {Name: "ДИАГ", Rows: [][]string{{"Поле"}}}}, nil
	}
	repo := mapRepo{requestRepo: requestRepo{snapshot: coursegraph.Snapshot{Status: "ready"}}, saved: &saved, sheet: &sheet, queued: true}
	w := New(repo, nil, nil, nil, nil, read)
	w.SetLearningMapStager(func(context.Context, user.User, string, []byte, []coursegraph.MapSheet) (int64, error) {
		return 42, nil
	})
	if _, err := w.ImportMap(context.Background(), user.User{ID: "s", Role: user.Student}, "subject", "map.xlsx", nil); !isFault(err, "FORBIDDEN") {
		t.Fatalf("student import: %v", err)
	}
	if _, err := w.ImportMap(context.Background(), admin, "subject", "map.xlsx", nil); err != nil {
		t.Fatal(err)
	}
	if sheet != "Карта" || !strings.Contains(string(saved), `"version":"competency-map-v1"`) || !strings.Contains(string(saved), `"learning_revision":42`) {
		t.Fatalf("saved sheet %q graph %s", sheet, saved)
	}
	busy := New(mapRepo{requestRepo: requestRepo{snapshot: coursegraph.Snapshot{Status: "processing"}}, saved: &saved, sheet: &sheet}, nil, nil, nil, nil, read)
	if _, err := busy.ImportMap(context.Background(), admin, "subject", "map.xlsx", nil); !isFault(err, "GRAPH_BUILD_IN_PROGRESS") {
		t.Fatalf("import during a build: %v", err)
	}
}

func TestImportMapReadsTheFirstSheetAndNamesBadCells(t *testing.T) {
	bad := mapRows("К1. Агенты")
	bad[2][4] = "Вспоминать"
	read := func([]byte) ([]coursegraph.MapSheet, error) {
		return []coursegraph.MapSheet{{Name: "Карта", Rows: bad}, {Name: "Карта_ВСЯ", Rows: mapRows("К1. Агенты")}}, nil
	}
	w := New(mapRepo{requestRepo: requestRepo{snapshot: coursegraph.Snapshot{Status: "ready"}}}, nil, nil, nil, nil, read)
	_, err := w.ImportMap(context.Background(), admin, "subject", "map.xlsx", nil)
	var f *fault.Error
	if !errors.As(err, &f) || f.Code != "COMPETENCY_MAP_INVALID" || len(f.Details) != 1 || !strings.Contains(f.Details[0].Message, "Строка 3, «Уровень по Блуму»") {
		t.Fatalf("bad cell: %+v", err)
	}
	unreadable := New(mapRepo{requestRepo: requestRepo{snapshot: coursegraph.Snapshot{Status: "ready"}}}, nil, nil, nil, nil,
		func([]byte) ([]coursegraph.MapSheet, error) { return nil, errors.New("zip") })
	if _, err := unreadable.ImportMap(context.Background(), admin, "subject", "map.pdf", nil); !isFault(err, "COMPETENCY_MAP_INVALID") {
		t.Fatalf("unreadable book: %v", err)
	}
}

func TestImportMapKeepsTheExactNameOfTheFirstSheet(t *testing.T) {
	var saved json.RawMessage
	var sheet string
	read := func([]byte) ([]coursegraph.MapSheet, error) {
		return []coursegraph.MapSheet{{Name: "Карта ", Rows: mapRows("К1. Агенты")}, {Name: "Карта_ВСЯ", Rows: mapRows("К2. Логика")}}, nil
	}
	repo := mapRepo{requestRepo: requestRepo{snapshot: coursegraph.Snapshot{Status: "ready"}}, saved: &saved, sheet: &sheet, queued: true}
	if _, err := New(repo, nil, nil, nil, nil, read).ImportMap(context.Background(), admin, "subject", "map.xlsx", nil); err != nil {
		t.Fatal(err)
	}
	if sheet != "Карта " || !strings.Contains(string(saved), "К1. Агенты") {
		t.Fatalf("sheet %q graph %s", sheet, saved)
	}
}

func TestBuildOfAMapPublishesItsGraphWithPreparedQuestions(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		name := "legacy"
		rows := mapRows("К1. Агенты")
		if explicit {
			name = "explicit"
			rows[0] = append([]string{"ID темы", "Пререквизиты"}, rows[0]...)
			for i, id := range []string{"a", "b", "c"} {
				requires := ""
				if i > 0 {
					requires = "a"
				}
				rows[i+1] = append([]string{id, requires}, rows[i+1]...)
			}
		}
		t.Run(name, func(t *testing.T) {
			graph, err := coursegraph.BuildMapGraph([]coursegraph.MapSheet{{Name: "Карта", Rows: rows}})
			if err != nil {
				t.Fatal(err)
			}
			repo := &publishingRepo{requestRepo: requestRepo{mapGraph: graph.Graph}}
			questions := &stubQuestions{}
			w := New(repo, nil, nil, nil, nil, nil)
			w.SetQuestions(questions)
			if err := w.process(context.Background(), coursegraph.Job{SubjectID: "subject", Source: coursegraph.SourceMap, Attempts: 1}); err != nil {
				t.Fatal(err)
			}
			if string(questions.seen) != string(graph.Graph) || len(repo.finished) != 1 {
				t.Fatalf("prepared %s, published %d graphs", questions.seen, len(repo.finished))
			}
		})
	}
}

func TestMapBuildRejectsUnknownGraphVersion(t *testing.T) {
	repo := &publishingRepo{requestRepo: requestRepo{mapGraph: json.RawMessage(`{"version":"unknown","topics":[{}]}`)}}
	w := New(repo, nil, nil, nil, nil, nil)
	if err := w.process(context.Background(), coursegraph.Job{Source: coursegraph.SourceMap, Attempts: 1}); err == nil || len(repo.finished) != 0 {
		t.Fatalf("unknown map version: error %v, published %d", err, len(repo.finished))
	}
}

func isFault(err error, code string) bool {
	var f *fault.Error
	return errors.As(err, &f) && f.Code == code
}

type queuedVoices struct {
	texts []string
	seen  map[string]bool
}

func (v *queuedVoices) Prepare(texts []string) int {
	queued := 0
	if v.seen == nil {
		v.seen = map[string]bool{}
	}
	for _, text := range texts {
		if !v.seen[text] {
			v.seen[text] = true
			v.texts = append(v.texts, text)
			queued++
		}
	}
	return queued
}

func (*queuedVoices) TrackPublished(_, _ string, _ []string) {}
func (*queuedVoices) TrackRestored(_, _ string, _ []string)  {}

func TestPrepareVoicesQueuesTheQuestionsOfEveryPublishedGraph(t *testing.T) {
	voices := &queuedVoices{}
	repo := requestRepo{published: []PublishedGraph{
		{SubjectID: "s1", Graph: json.RawMessage(`{"topics":[{"question":"Почему A?"},{"question":"Почему B?"}]}`)},
		{SubjectID: "s2", Graph: json.RawMessage(`{"topics":[{"question":"Почему C?"}]}`)},
	}}
	if err := New(repo, nil, nil, nil, voices, nil).PrepareVoices(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(voices.texts) != 3 || voices.texts[0] != "Почему A?" || voices.texts[2] != "Почему C?" {
		t.Fatalf("queued %q", voices.texts)
	}
	// Without a queue there is nothing to prepare.
	if err := New(repo, nil, nil, nil, nil, nil).PrepareVoices(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// publishingRepo records the graph a build publishes.
type publishingRepo struct {
	requestRepo
	finished []json.RawMessage
}

func (r *publishingRepo) Finish(_ context.Context, _ coursegraph.Job, graph json.RawMessage, _ time.Time) (bool, error) {
	r.finished = append(r.finished, graph)
	return true, nil
}

type stubQuestions struct {
	err  error
	seen json.RawMessage
}

func (q *stubQuestions) Prepare(_ context.Context, graph json.RawMessage) (json.RawMessage, error) {
	q.seen = graph
	if q.err != nil {
		return nil, q.err
	}
	return json.RawMessage(`{"version":"course-graph-v1","topics":[{"question":"Почему?","rephrase":"Отчего?"}],"prerequisites":[],"quality":{"acyclic":true}}`), nil
}

func TestBuildPublishesOnlyAGraphWithPreparedQuestions(t *testing.T) {
	items := requestMaterials{{ID: "m", S3ParsedKey: "parsed", Cleaning: &material.Cleaning{Status: "ready", S3ReportKey: "report"}}}
	for name, prepareErr := range map[string]error{"prepared": nil, "not prepared": errors.New("gateway down")} {
		t.Run(name, func(t *testing.T) {
			repo := &publishingRepo{}
			voices := &queuedVoices{}
			questions := &stubQuestions{err: prepareErr}
			w := New(repo, items, progressStorage{}, &capturingBuilder{}, voices, nil)
			w.SetQuestions(questions)
			err := w.process(context.Background(), coursegraph.Job{SubjectID: "subject"})
			if questions.seen == nil {
				t.Fatal("the build was not prepared")
			}
			if prepareErr != nil {
				// The previous graph stays published.
				if err == nil || len(repo.finished) != 0 || len(voices.texts) != 0 {
					t.Fatalf("err=%v published=%d voiced=%q", err, len(repo.finished), voices.texts)
				}
				return
			}
			if err != nil || len(repo.finished) != 1 || !strings.Contains(string(repo.finished[0]), `"rephrase":"Отчего?"`) {
				t.Fatalf("err=%v published=%s", err, repo.finished)
			}
			if len(voices.texts) != 2 || voices.texts[0] != "Почему?" || voices.texts[1] != "Отчего?" {
				t.Fatalf("voiced %q", voices.texts)
			}
		})
	}
}

// failingRepo hands out one job and records why it failed.
type failingRepo struct {
	publishingRepo
	claimed bool
	failed  []string
}

func (r *failingRepo) Claim(context.Context, string, time.Time) (coursegraph.Job, error) {
	if r.claimed {
		return coursegraph.Job{}, nil
	}
	r.claimed = true
	return coursegraph.Job{SubjectID: "subject", Token: "lease"}, nil
}

func (r *failingRepo) Fail(_ context.Context, _ coursegraph.Job, message string, _ time.Time) error {
	r.failed = append(r.failed, message)
	return nil
}

func TestAFailureToPrepareQuestionsIsReportedAsSuch(t *testing.T) {
	items := requestMaterials{{ID: "m", S3ParsedKey: "parsed", Cleaning: &material.Cleaning{Status: "ready", S3ReportKey: "report"}}}
	repo := &failingRepo{}
	w := New(repo, items, progressStorage{}, &capturingBuilder{}, nil, nil)
	w.SetQuestions(&stubQuestions{err: errors.New("gateway down")})
	if worked, err := w.ProcessOne(context.Background()); !worked || err == nil {
		t.Fatalf("worked=%v err=%v", worked, err)
	}
	if len(repo.failed) != 1 || !strings.Contains(repo.failed[0], "вопросы к темам не подготовлены") || len(repo.finished) != 0 {
		t.Fatalf("failed=%q published=%d", repo.failed, len(repo.finished))
	}
}

func TestImportMapLogsFallbackCriteriaOnlyAfterSaving(t *testing.T) {
	for _, tc := range []struct {
		name            string
		missing, queued bool
	}{
		{"saved map with missing criteria", true, true},
		{"saved map with authored criteria", false, true},
		{"rejected concurrent import", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			core, logs := observer.New(zap.WarnLevel)
			ctx := logx.With(context.Background(), zap.New(core))
			rows := mapRows("К1. Агенты")
			if tc.missing {
				rows[1][8] = ""
				rows[3][8] = ""
			}
			read := func([]byte) ([]coursegraph.MapSheet, error) {
				return []coursegraph.MapSheet{{Name: "Карта", Rows: rows}}, nil
			}
			var saved json.RawMessage
			var sheet string
			repo := mapRepo{requestRepo: requestRepo{snapshot: coursegraph.Snapshot{Status: "ready"}}, saved: &saved, sheet: &sheet, queued: tc.queued}
			_, err := New(repo, nil, nil, nil, nil, read).ImportMap(ctx, admin, "subject", "map.xlsx", nil)
			if tc.queued && err != nil {
				t.Fatal(err)
			}
			if !tc.queued && !isFault(err, "GRAPH_BUILD_IN_PROGRESS") {
				t.Fatalf("expected rejected import, got %v", err)
			}
			if !tc.missing || !tc.queued {
				if logs.Len() != 0 {
					t.Fatalf("unexpected warning: %v", logs.All())
				}
				return
			}
			if logs.Len() != 2 {
				t.Fatalf("expected one warning per affected task: %v", logs.All())
			}
			for i, row := range []int64{2, 4} {
				fields := logs.All()[i].ContextMap()
				for key, want := range map[string]any{
					"subject_id": "subject", "filename": "map.xlsx", "sheet": "Карта", "row": row, "column": "Задание 1",
					"warning": "Критерии не заданы: используются общие критерии корректности и покрытия вопроса.",
				} {
					if fields[key] != want {
						t.Errorf("%s = %v, want %v", key, fields[key], want)
					}
				}
			}
		})
	}
}

type recoveryRaceRepo struct {
	requestRepo
	finished chan struct{}
}

func (r *recoveryRaceRepo) Finish(_ context.Context, _ coursegraph.Job, _ json.RawMessage, _ time.Time) (bool, error) {
	close(r.finished)
	return true, nil
}

type recoveryRaceVoices struct {
	mu         sync.Mutex
	oldEntered chan struct{}
	releaseOld chan struct{}
	version    string
	oldVersion string
}

func (v *recoveryRaceVoices) Prepare([]string) int { return 0 }
func (v *recoveryRaceVoices) TrackRestored(subject, version string, texts []string) {
	v.TrackPublished(subject, version, texts)
}
func (v *recoveryRaceVoices) TrackPublished(_, version string, _ []string) {
	if version == v.oldVersion {
		close(v.oldEntered)
		<-v.releaseOld
	}
	v.mu.Lock()
	v.version = version
	v.mu.Unlock()
}

func TestPrepareVoicesDoesNotOverwriteConcurrentPublication(t *testing.T) {
	oldGraph := json.RawMessage(`{"topics":[{"question":"Старый?"}]}`)
	newGraph := json.RawMessage(`{"topics":[{"question":"Новый?"}]}`)
	repo := &recoveryRaceRepo{requestRepo: requestRepo{published: []PublishedGraph{{SubjectID: "s", Graph: oldGraph}}}, finished: make(chan struct{})}
	voices := &recoveryRaceVoices{oldEntered: make(chan struct{}), releaseOld: make(chan struct{}), oldVersion: GraphVersion(oldGraph)}
	worker := New(repo, nil, nil, nil, voices, nil)
	recoveryDone := make(chan error, 1)
	go func() { recoveryDone <- worker.PrepareVoices(context.Background()) }()
	<-voices.oldEntered
	publishDone := make(chan error, 1)
	go func() { publishDone <- worker.publish(context.Background(), coursegraph.Job{SubjectID: "s"}, newGraph) }()
	// Without synchronization publication finishes before the blocked old snapshot is installed.
	var publishErr error
	finishedEarly := false
	select {
	case <-repo.finished:
		publishErr = <-publishDone
		finishedEarly = true
	case <-time.After(100 * time.Millisecond):
	}
	close(voices.releaseOld)
	if err := <-recoveryDone; err != nil {
		t.Fatal(err)
	}
	if !finishedEarly {
		publishErr = <-publishDone
	}
	if publishErr != nil {
		t.Fatal(publishErr)
	}
	voices.mu.Lock()
	defer voices.mu.Unlock()
	if voices.version != GraphVersion(newGraph) {
		t.Fatalf("registered version = %s; want new published graph", voices.version)
	}
}

func TestRefreshPublishedAudioDoesNotOverwriteConcurrentPublication(t *testing.T) {
	oldGraph := json.RawMessage(`{"topics":[{"question":"Старый?"}]}`)
	newGraph := json.RawMessage(`{"topics":[{"question":"Новый?"}]}`)
	repo := &recoveryRaceRepo{requestRepo: requestRepo{published: []PublishedGraph{{SubjectID: "s", Graph: oldGraph}}}, finished: make(chan struct{})}
	voices := &recoveryRaceVoices{oldEntered: make(chan struct{}), releaseOld: make(chan struct{}), oldVersion: GraphVersion(oldGraph)}
	worker := New(repo, nil, nil, nil, voices, nil)
	refreshDone := make(chan error, 1)
	go func() { refreshDone <- worker.RefreshPublishedAudio(context.Background(), "s") }()
	<-voices.oldEntered
	publishDone := make(chan error, 1)
	go func() { publishDone <- worker.publish(context.Background(), coursegraph.Job{SubjectID: "s"}, newGraph) }()

	var publishErr error
	finishedEarly := false
	select {
	case <-repo.finished:
		publishErr = <-publishDone
		finishedEarly = true
	case <-time.After(100 * time.Millisecond):
	}
	close(voices.releaseOld)
	if err := <-refreshDone; err != nil {
		t.Fatal(err)
	}
	if !finishedEarly {
		publishErr = <-publishDone
	}
	if publishErr != nil {
		t.Fatal(publishErr)
	}
	voices.mu.Lock()
	defer voices.mu.Unlock()
	if voices.version != GraphVersion(newGraph) {
		t.Fatalf("registered version = %s; want new published graph", voices.version)
	}
}
