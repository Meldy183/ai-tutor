package app

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AskarKasimov/ai-tutor/services/backend/internal/shared/llmgateway"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/shared/logx"
	pb "github.com/AskarKasimov/ai-tutor/services/shared/gen/go/llmgateway/v1"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/coursegraph"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/exam"
	geekapp "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/geek/application"
	geekgraph "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/geek/infrastructure/graph"
)

// buildExaminer fails the first fails questions, then writes questions by
// topic title and rephrases by the question.
type buildExaminer struct {
	fails atomic.Int32
	asked atomic.Int32
}

func (e *buildExaminer) Question(_ context.Context, r geekapp.QuestionRequest) (string, error) {
	e.asked.Add(1)
	if e.fails.Add(-1) >= 0 {
		return "", errors.New("gateway down")
	}
	if !r.Ahead {
		return "", errors.New("not prepared ahead")
	}
	if r.Kind == exam.KindRephrase {
		return "Проще: " + r.Dialogue[0].Text, nil
	}
	return "Вопрос про «" + r.Node.Title + "»?", nil
}

func (e *buildExaminer) Evaluate(context.Context, geekapp.EvaluationRequest) (geekapp.Assessment, error) {
	return geekapp.Assessment{}, errors.New("not used")
}

func (e *buildExaminer) Feedback(context.Context, geekapp.FeedbackRequest) (string, error) {
	return "", errors.New("not used")
}

const build = `{"version":"course-graph-v1","quality":{"acyclic":true},"hierarchy":[{"id":"s"}],
"topics":[
 {"id":"a","title":"Поиск","text":"Текст поиска","question":"Как работает поиск?","provides":["поиск"],"requires":[],"sources":[{"material_id":"m","block_id":1,"start":0,"end":5,"text":"Текст"}],"extra":{"kept":true}},
 {"id":"b","title":"Эвристика","text":"Текст эвристики","question":"","provides":["эвристика"],"requires":["поиск"]}
],
"prerequisites":[{"source":"a","target":"b"}]}`

func TestMapDefaultsAndWarningsSurviveQuestionPreparation(t *testing.T) {
	header := []string{"Компетенция", "Составляющая", "Образовательный результат", "Таксономия", "Важность", "Что должно войти в тест", "Задание 1", "Задание 2", "Критерии оценивания 1", "ID темы", "Пререквизиты"}
	rows := [][]string{header, {"ИИ", "Агенты", "Агент", "Понимать", "3", "TRUE", "Назови агента", "Назови агента и объясни выбор", "Балл (макс. 1): 0 — нет; 1 — верно", "agent", ""}}
	built, err := coursegraph.BuildMapGraph([]coursegraph.MapSheet{{Name: "Карта", Rows: rows}})
	if err != nil {
		t.Fatal(err)
	}
	examiner := &buildExaminer{}
	out, err := (builtQuestions{examiner: examiner, logger: zap.NewNop()}).Prepare(context.Background(), built.Graph)
	if err != nil {
		t.Fatal(err)
	}
	var before, after struct {
		Warnings []coursegraph.MapIssue `json:"assessment_warnings"`
	}
	if err := json.Unmarshal(built.Graph, &before); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(out, &after); err != nil {
		t.Fatal(err)
	}
	if len(before.Warnings) != 1 || !reflect.DeepEqual(before.Warnings, after.Warnings) {
		t.Fatalf("preparation lost warning metadata: before=%+v after=%+v", before, after)
	}
	g, ok, err := geekgraph.Decode(out)
	if err != nil || !ok {
		t.Fatalf("decode: %v, %v", ok, err)
	}
	n := g.Nodes[0]
	if len(n.Variants) != 2 || n.Variants[0].Question != rows[1][6] || n.Variants[1].Question != rows[1][7] || n.Variants[1].AssessmentGuidance != coursegraph.DefaultAssessmentGuidance || n.Variants[0].AssessmentGuidance == coursegraph.DefaultAssessmentGuidance || !n.Prepared() {
		t.Fatalf("preparation changed task criteria or questions: %+v", n)
	}
}

func TestBuildReportsProgressAndRetryWithoutCountingFailedTopics(t *testing.T) {
	examiner := &buildExaminer{}
	examiner.fails.Store(1)
	var updates []coursegraph.BuildProgress
	_, err := (builtQuestions{examiner: examiner, logger: zap.NewNop(), pause: time.Millisecond}).PrepareWithProgress(context.Background(), json.RawMessage(build), func(p coursegraph.BuildProgress) { updates = append(updates, p) })
	if err != nil {
		t.Fatal(err)
	}
	retry := false
	for _, p := range updates {
		if p.Done > p.Total || p.Total != 2 {
			t.Fatalf("invalid progress: %+v", p)
		}
		if p.Stage == "retry_questions" {
			retry = true
			if p.RemainingSeconds != nil {
				t.Fatal("retry has misleading estimate")
			}
		}
	}
	if !retry || updates[len(updates)-1].Done != 2 {
		t.Fatalf("missing retry or completion: %+v", updates)
	}
}

// tracedExaminer asks through the real gateway interceptor, as Geek's examiner does.
type tracedExaminer struct {
	*buildExaminer
	mu       sync.Mutex
	outgoing []string
}

func (e *tracedExaminer) Question(ctx context.Context, r geekapp.QuestionRequest) (string, error) {
	err := llmgateway.ClientInterceptor(ctx, "/Complete", &pb.CompleteRequest{}, &pb.CompleteResponse{}, nil,
		func(ctx context.Context, _ string, _, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
			md, _ := metadata.FromOutgoingContext(ctx)
			e.mu.Lock()
			defer e.mu.Unlock()
			e.outgoing = append(e.outgoing, md.Get("traceparent")...)
			return nil
		})
	if err != nil {
		return "", err
	}
	return e.buildExaminer.Question(ctx, r)
}

func TestBuiltQuestionsKeepTheTraceOfTheJobInLogsAndCalls(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	examiner := &tracedExaminer{buildExaminer: &buildExaminer{}}
	ctx := logx.StartTrace(context.Background())
	data := json.RawMessage(`{"topics":[{"id":"a","title":"Тема","text":"Текст"}]}`)
	if _, err := (builtQuestions{examiner: examiner, logger: zap.New(core), pause: time.Millisecond}).Prepare(ctx, data); err != nil {
		t.Fatal(err)
	}
	traceID := logx.TraceID(ctx)
	if len(examiner.outgoing) == 0 || !strings.HasPrefix(examiner.outgoing[0], "00-"+traceID+"-") {
		t.Fatalf("outgoing traceparent = %v, trace = %s", examiner.outgoing, traceID)
	}
	calls := logs.FilterMessage("llm call").All()
	if len(calls) == 0 {
		t.Fatalf("no llm call in the logs: %v", logs.All())
	}
	for _, entry := range logs.All() {
		if entry.ContextMap()["trace_id"] != traceID {
			t.Errorf("%q has no trace of the job: %v", entry.Message, entry.ContextMap())
		}
	}
}

func TestBuiltQuestionsArePreparedIntoTheTopics(t *testing.T) {
	examiner := &buildExaminer{}
	// The first round loses two calls; the next round prepares the rest.
	examiner.fails.Store(2)
	out, err := builtQuestions{examiner: examiner, logger: zap.NewNop(), pause: time.Millisecond}.Prepare(context.Background(), json.RawMessage(build))
	if err != nil {
		t.Fatal(err)
	}
	g, ok, err := geekgraph.Decode(out)
	if err != nil || !ok {
		t.Fatalf("decode: ok=%v err=%v", ok, err)
	}
	if len(geekapp.Unprepared(g)) != 0 {
		t.Fatalf("unprepared: %+v", g.Nodes)
	}
	a, _ := g.Node("a")
	b, _ := g.Node("b")
	if a.MainQuestion() != "Как работает поиск?" || a.Rephrase != "Проще: Как работает поиск?" ||
		b.MainQuestion() != "Вопрос про «Эвристика»?" || b.Rephrase != "Проще: Вопрос про «Эвристика»?" {
		t.Fatalf("a=%+v b=%+v", a, b)
	}
	if len(b.Prerequisites) != 1 || b.Prerequisites[0] != "a" || len(a.Sources) != 1 {
		t.Fatalf("graph changed: a=%+v b=%+v", a, b)
	}
	// Everything course-graph wrote is kept.
	for _, field := range []string{`"hierarchy":[{"id":"s"}]`, `"extra":{"kept":true}`, `"quality":{"acyclic":true}`, `"version":"course-graph-v1"`} {
		if !strings.Contains(string(out), field) {
			t.Fatalf("lost %s in %s", field, out)
		}
	}
}

func TestBuiltQuestionsFailWhenATopicStaysUnprepared(t *testing.T) {
	examiner := &buildExaminer{}
	examiner.fails.Store(1000)
	_, err := builtQuestions{examiner: examiner, logger: zap.NewNop(), pause: time.Millisecond}.Prepare(context.Background(), json.RawMessage(build))
	if err == nil || !strings.Contains(err.Error(), "2 topics without prepared questions") {
		t.Fatalf("err = %v", err)
	}
	// Each of the three rounds asks for each topic once and stops at its failure.
	if examiner.asked.Load() != 2*builtQuestionRounds {
		t.Fatalf("asked %d times", examiner.asked.Load())
	}
}

func TestBuiltQuestionsRephraseEveryVariantOfAMapTopic(t *testing.T) {
	data := `{"version":"competency-map-v1","source":"competency_map","topics":[{"id":"m","title":"ОР","text":"Эталон 1","question":"Устно?",` +
		`"variants":[{"question":"Устно?","content":"Эталон 1"},{"question":"Академически?","content":"Эталон 2","assessment_guidance":"Рубрика"}],` +
		`"provides":["ОР"],"requires":[]}],"prerequisites":[]}`
	out, err := builtQuestions{examiner: &buildExaminer{}, logger: zap.NewNop(), pause: time.Millisecond}.Prepare(context.Background(), json.RawMessage(data))
	if err != nil {
		t.Fatal(err)
	}
	g, _, err := geekgraph.Decode(out)
	if err != nil {
		t.Fatal(err)
	}
	m := g.Nodes[0]
	if m.Rephrase != "Проще: Устно?" || len(m.Variants) != 2 || m.Variants[1].Rephrase != "Проще: Академически?" || m.Variants[1].AssessmentGuidance != "Рубрика" {
		t.Fatalf("node = %+v", m)
	}
}
