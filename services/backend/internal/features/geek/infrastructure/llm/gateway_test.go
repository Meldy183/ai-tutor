package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	llmgatewayv1 "github.com/AskarKasimov/ai-tutor/services/shared/gen/go/llmgateway/v1"

	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/exam"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/features/geek/application"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/shared/logx"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// fakeGateway answers Complete with scripted message contents in order; the
// last one repeats.
type fakeGateway struct {
	llmgatewayv1.UnimplementedGatewayServer
	mu        sync.Mutex
	requests  []*llmgatewayv1.CompleteRequest
	answers   []string
	err       error
	block     bool
	cancelled chan struct{}
	header    metadata.MD
}

func (f *fakeGateway) Complete(ctx context.Context, r *llmgatewayv1.CompleteRequest) (*llmgatewayv1.CompleteResponse, error) {
	f.mu.Lock()
	f.requests = append(f.requests, r)
	n := len(f.requests)
	f.mu.Unlock()
	if f.block {
		<-ctx.Done()
		close(f.cancelled)
		return nil, ctx.Err()
	}
	if f.header != nil {
		_ = grpc.SetHeader(ctx, f.header)
	}
	if f.err != nil {
		return nil, f.err
	}
	completion, _ := json.Marshal(map[string]any{
		"id": "c", "object": "chat.completion", "created": 1, "model": "gpt-oss-120b",
		"choices": []any{map[string]any{"index": 0, "message": map[string]string{"role": "assistant", "content": f.answers[min(n, len(f.answers))-1]}, "finish_reason": "stop"}},
	})
	return &llmgatewayv1.CompleteResponse{RequestId: "req-1", CompletionJson: completion}, nil
}

func (f *fakeGateway) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func serve(t *testing.T, f *fakeGateway, timeout time.Duration) *Gateway {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	llmgatewayv1.RegisterGatewayServer(server, f)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	g, conn, err := Dial(listener.Addr().String(), timeout)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return g
}

var node = exam.Node{ID: "a-star", Title: "Алгоритм A*", Content: "f(n) = g(n) + h(n)."}

var turns = []exam.Turn{
	{Line: 1, Speaker: exam.SpeakerGeek, Text: "Вопрос?"},
	{Line: 2, Speaker: exam.SpeakerStudent, Text: "Ответ."},
}

func TestGatewayAsksWithComplexityAndJSONFormat(t *testing.T) {
	f := &fakeGateway{answers: []string{`{"text": "Почему A* оптимален?"}`}}
	g := serve(t, f, time.Second)
	text, err := g.Question(context.Background(), application.QuestionRequest{Kind: exam.KindRephrase, Node: node})
	if err != nil || text != "Почему A* оптимален?" {
		t.Fatalf("question = %q, %v", text, err)
	}
	r := f.requests[0]
	if r.GetComplexity() != llmgatewayv1.Complexity_COMPLEXITY_LOW || !r.GetUrgent() {
		t.Fatalf("complexity = %s urgent = %v", r.GetComplexity(), r.GetUrgent())
	}
	if r.GetParameters().GetResponseFormat() != llmgatewayv1.ResponseFormat_RESPONSE_FORMAT_JSON_OBJECT || r.GetParameters().GetTemperature() != 0.2 ||
		r.GetParameters().GetReasoningEffort() != llmgatewayv1.ReasoningEffort_REASONING_EFFORT_LOW {
		t.Fatalf("parameters = %v", r.GetParameters())
	}
	messages := r.GetMessages()
	if len(messages) != 2 || messages[0].GetRole() != llmgatewayv1.Role_ROLE_SYSTEM || messages[1].GetRole() != llmgatewayv1.Role_ROLE_USER ||
		!strings.Contains(messages[1].GetContent(), "f(n) = g(n) + h(n).") {
		t.Fatalf("messages = %v", messages)
	}
	if _, err := g.Question(context.Background(), application.QuestionRequest{Kind: exam.KindFollowup, Node: node}); err != nil ||
		f.requests[1].GetComplexity() != llmgatewayv1.Complexity_COMPLEXITY_LOW {
		t.Fatalf("followup complexity = %s, %v", f.requests[1].GetComplexity(), err)
	}
	if _, err := g.Question(context.Background(), application.QuestionRequest{Kind: exam.KindQuestion, Node: node}); err != nil ||
		f.requests[2].GetComplexity() != llmgatewayv1.Complexity_COMPLEXITY_LOW {
		t.Fatalf("main question asked in a session: complexity = %s, %v", f.requests[2].GetComplexity(), err)
	}
	if _, err := g.Question(context.Background(), application.QuestionRequest{Kind: exam.KindQuestion, Node: node, Ahead: true}); err != nil ||
		f.requests[3].GetComplexity() != llmgatewayv1.Complexity_COMPLEXITY_MEDIUM || f.requests[3].GetUrgent() {
		t.Fatalf("main question prepared ahead: complexity = %s, %v", f.requests[3].GetComplexity(), err)
	}
}

func TestGatewayEvaluatesAndRetriesAnInvalidAnswerOnce(t *testing.T) {
	f := &fakeGateway{answers: []string{"не JSON", "```json\n{\"verdict\":\"answered\",\"score\":1,\"reasons\":[\" нет обоснования \",\"\"],\"evidence_lines\":[2]}\n```"}}
	g := serve(t, f, time.Second)
	e, err := g.Evaluate(context.Background(), application.EvaluationRequest{MaxScore: 2, Kind: exam.KindQuestion, Node: node, Dialogue: turns})
	if err != nil || e.Verdict != exam.VerdictAnswered || e.Score != 1 || !slices.Equal(e.Reasons, []string{"нет обоснования"}) || e.Evidence[0] != 2 {
		t.Fatalf("evaluation = %+v, %v", e, err)
	}
	if len(f.requests) != 2 || f.requests[0].GetComplexity() != llmgatewayv1.Complexity_COMPLEXITY_LOW || !f.requests[0].GetUrgent() {
		t.Fatalf("requests = %d", len(f.requests))
	}
	for _, r := range f.requests {
		if r.GetParameters().GetReasoningEffort() != llmgatewayv1.ReasoningEffort_REASONING_EFFORT_LOW {
			t.Fatalf("grading reasoning = %v", r.GetParameters().GetReasoningEffort())
		}
	}
	if !strings.Contains(f.requests[0].GetMessages()[1].GetContent(), "[2] Студент: Ответ.") {
		t.Fatal("prompt lacks the numbered dialogue")
	}
}

func TestGatewayKeepsARejectedAnswerOutOfInfoLogsAndErrors(t *testing.T) {
	const invented = "PRIVATE_STUDENT_ANSWER"
	answer := `{"verdict":"` + invented + `"}`
	g := serve(t, &fakeGateway{answers: []string{answer, answer}}, time.Second)
	core, logs := observer.New(zap.InfoLevel)
	_, err := g.Evaluate(logx.With(context.Background(), zap.New(core)), application.EvaluationRequest{MaxScore: 2, Kind: exam.KindQuestion, Node: node, Dialogue: turns})
	if err == nil {
		t.Fatal("an invalid verdict was accepted")
	}
	if strings.Contains(err.Error(), invented) {
		t.Fatalf("the error quotes the model: %v", err)
	}
	for _, entry := range logs.All() {
		for key, value := range entry.ContextMap() {
			if text, ok := value.(string); ok && strings.Contains(text, invented) {
				t.Fatalf("log field %s quotes the model: %s", key, text)
			}
		}
	}
}

func TestGatewayEvidenceAcceptsOnlyIntegerLineNumbers(t *testing.T) {
	for _, evidence := range []string{`[2]`, `["2"]`, `["2.5"]`, `[2.5]`, `["wrong"]`, `[null]`, `["999999999999999999999999999999"]`} {
		t.Run(evidence, func(t *testing.T) {
			answer := `{"verdict":"answered","score":2,"reasons":["основание"],"evidence_lines":` + evidence + `}`
			f := &fakeGateway{answers: []string{answer, answer}}
			g := serve(t, f, time.Second)
			got, err := g.Evaluate(context.Background(), application.EvaluationRequest{MaxScore: 2, Kind: exam.KindQuestion, Node: node, Dialogue: turns})
			valid := evidence == `[2]` || evidence == `["2"]`
			if valid {
				if err != nil || len(got.Evidence) != 1 || got.Evidence[0] != 2 {
					t.Fatalf("evaluation = %+v, %v", got, err)
				}
			} else if err == nil {
				t.Fatal("invalid evidence accepted")
			}
		})
	}
}

func TestGatewayReturnsTheReplyAndLeavesTheRecapOutOfTheDialogue(t *testing.T) {
	f := &fakeGateway{answers: []string{
		`{"verdict":"answered","score":1,"reasons":["основание"],"evidence_lines":[2],"reaction":" Близко. ","explanation":"Близко. На самом деле f = g + h."}`,
		`{"verdict":"answered","score":1,"reasons":["основание"],"evidence_lines":[2]}`,
	}}
	g := serve(t, f, time.Second)
	recap := "Разбор прошлой темы.\n\n"
	dialogue := []exam.Turn{
		{Line: 1, Speaker: exam.SpeakerGeek, Text: recap + "Вопрос?", Recap: recap},
		{Line: 2, Speaker: exam.SpeakerStudent, Text: "Ответ."},
	}
	request := application.EvaluationRequest{MaxScore: 2, Kind: exam.KindQuestion, Node: node, Prerequisites: []string{"Эвристика"}, Dialogue: dialogue}
	a, err := g.Evaluate(context.Background(), request)
	if err != nil || a.Verdict != exam.VerdictAnswered {
		t.Fatalf("assessment = %+v, %v", a, err)
	}
	prompt := f.requests[0].GetMessages()[1].GetContent()
	if strings.Contains(prompt, "Разбор прошлой темы") || !strings.Contains(prompt, "[1] Geek: Вопрос?") {
		t.Fatalf("prompt = %s", prompt)
	}
	// Feedback is a separate operation; grading does not require reply fields.
	if _, err := g.Evaluate(context.Background(), request); err != nil {
		t.Fatalf("assessment without a reply = %+v, %v", a, err)
	}
}

func TestEvaluationGradesTheQuestionNotAllOutcomes(t *testing.T) {
	// A correct answer to the selected concept must not be compared to every outcome.
	n := node
	n.Outcomes = []string{"объяснять, из чего складывается оценка f(n)"}
	prompt := evaluationPrompt(application.EvaluationRequest{MaxScore: 2, Kind: exam.KindQuestion, Node: n, Dialogue: turns})
	for _, want := range []string{
		"на заданный вопрос",
		"по шкале темы: 0, 1 или 2",
		"verdict определи по последней реплике",
		"Не приписывай студенту знания",
		"Принимай разумные примеры вне теории",
		"Не принимай выдуманные факты",
		"все явно запрошенные части покрыты",
		"которых не спрашивали",
		"в том числе ошибочная, неполная или тавтологичная",
		"не требуй конкретных примеров",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("evaluation prompt lacks %q", want)
		}
	}
	if strings.Contains(prompt, n.Outcomes[0]) || strings.Contains(prompt, "образовательных результатов") || strings.Contains(prompt, "35 слов") {
		t.Error("evaluation prompt still grades strictly by the topic text or allows 35 words")
	}
	if strings.Contains(prompt, `Верни JSON: {"verdict":"answered"`) {
		t.Fatal("output instructions force the answered verdict")
	}
}

func TestEvaluationIncludesOnlyExplicitInstructorGuidance(t *testing.T) {
	n := node
	n.AssessmentGuidance = "Оцени объяснение по смыслу, без совпадения с текстом темы."
	p := evaluationPrompt(application.EvaluationRequest{MaxScore: 2, Node: n, Kind: exam.KindQuestion})
	if !strings.Contains(p, n.AssessmentGuidance) || !strings.Contains(p, "Критерии преподавателя") {
		t.Fatal("evaluation lost the instructor's scope")
	}
	if strings.Contains(questionPrompt(application.QuestionRequest{Node: n}), n.AssessmentGuidance) {
		t.Fatal("grading guidance leaked into question generation")
	}
}

func TestOralExamPromptsAskForReasoningAndStayNeutral(t *testing.T) {
	// Decision of 18.10.2026: the exam is oral, so questions ask only for
	// spoken reasoning, grading does not require written work, and the
	// reaction never criticises the student.
	for _, kind := range []exam.TurnKind{exam.KindQuestion, exam.KindFollowup, exam.KindRephrase} {
		p := questionPrompt(application.QuestionRequest{Kind: kind, Node: node, Dialogue: turns})
		if !strings.Contains(p, "Экзамен устный") || !strings.Contains(p, "нарисовать схему") || !strings.Contains(p, "написать код") {
			t.Errorf("%s question prompt lacks the oral format: %s", kind, p)
		}
	}
	if p := questionPrompt(application.QuestionRequest{Kind: exam.KindFollowup, Node: node, Dialogue: turns}); strings.Contains(p, "измени условия задачи") {
		t.Error("follow-up prompt still asks to change the conditions of a problem")
	}
	p := evaluationPrompt(application.EvaluationRequest{MaxScore: 2, Kind: exam.KindQuestion, Node: node, Dialogue: turns})
	for _, want := range []string{"не требуй вычислений, формул, кода, схем", "ошибки распознавания", "Ложное обоснование и круговое объяснение"} {
		if !strings.Contains(p, want) {
			t.Errorf("evaluation prompt lacks %q", want)
		}
	}
	if strings.Contains(p, "где ошибка") {
		t.Error("reaction still points out the student's mistake")
	}
}

func TestExamSpeechDoesNotUseStudentName(t *testing.T) {
	const name = "Андрей"
	q := application.QuestionRequest{PreferredName: name, Kind: exam.KindFollowup, Node: node}
	f := application.FeedbackRequest{EvaluationRequest: application.EvaluationRequest{MaxScore: 2, PreferredName: name, Node: node}}
	for _, prompt := range []string{questionPrompt(q), feedbackPrompt(f)} {
		if strings.Contains(prompt, name) || strings.Contains(prompt, "Студент попросил обращаться к нему по имени") {
			t.Fatalf("stored student name leaked into speech instructions: %s", prompt)
		}
	}
	for _, prompt := range []string{systemPrompt, feedbackSystemPrompt, supportSystemPrompt, contextSystemPrompt} {
		if !strings.Contains(prompt, "имени или прозвищу") && !strings.Contains(prompt, "без имени или прозвища") {
			t.Fatalf("speech system prompt does not forbid addressing by name: %s", prompt)
		}
	}
	fake := &fakeGateway{answers: []string{`{"kind":"repeat","text":"Повтори ответ"}`}}
	g := serve(t, fake, time.Second)
	a := application.Assessment{}
	if err := g.clarifySupport(context.Background(), application.EvaluationRequest{MaxScore: 2, PreferredName: name, Dialogue: turns}, &a); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fake.requests[0].Messages[1].GetContent(), name) {
		t.Fatal("support received the stored name")
	}
}

func TestGatewayFeedbackOnTheLowPool(t *testing.T) {
	f := &fakeGateway{answers: []string{`{"text": "A* складывает g и h."}`}}
	g := serve(t, f, time.Second)
	text, err := g.Feedback(context.Background(), application.FeedbackRequest{Closed: true, EvaluationRequest: application.EvaluationRequest{MaxScore: 2, Node: node, Prerequisites: []string{"Эвристика"}, Dialogue: turns}})
	if err != nil || text != "A* складывает g и h." {
		t.Fatalf("explanation = %q, %v", text, err)
	}
	r := f.requests[0]
	if r.GetComplexity() != llmgatewayv1.Complexity_COMPLEXITY_LOW || !r.GetUrgent() || !strings.Contains(r.GetMessages()[1].GetContent(), "Эвристика") {
		t.Fatalf("complexity = %s prompt = %s", r.GetComplexity(), r.GetMessages()[1].GetContent())
	}
	if r.GetParameters().GetReasoningEffort() != llmgatewayv1.ReasoningEffort_REASONING_EFFORT_LOW {
		t.Fatalf("explanation reasoning = %v", r.GetParameters().GetReasoningEffort())
	}
}

func TestGatewayRejectsEmptyOrExcessiveFeedbackAndRetries(t *testing.T) {
	for _, closed := range []bool{true} {
		for _, invalid := range []string{"", strings.Repeat("слово ", 81)} {
			bad, _ := json.Marshal(map[string]string{"text": invalid})
			f := &fakeGateway{answers: []string{string(bad), `{"text":"Разберём тот же вопрос проще."}`}}
			g := serve(t, f, time.Second)
			text, err := g.Feedback(context.Background(), application.FeedbackRequest{Closed: closed})
			if err != nil || text != "Разберём тот же вопрос проще." || f.count() != 2 {
				t.Fatalf("feedback = %q, %v, calls=%d", text, err, f.count())
			}
		}
	}
}

func TestFeedbackUsesDifferentInstructionsForActualDecisions(t *testing.T) {
	full, partial := 2, 1
	for _, tc := range []struct {
		r    application.FeedbackRequest
		want string
	}{
		{application.FeedbackRequest{EvaluationRequest: application.EvaluationRequest{MaxScore: 2}, Closed: true, Result: exam.NodeState{Score: &full}}, "не ищи недостатков"},
		{application.FeedbackRequest{EvaluationRequest: application.EvaluationRequest{MaxScore: 2}, Closed: true, Result: exam.NodeState{Score: &partial}}, "сам восполни существенный пробел"},
		{application.FeedbackRequest{EvaluationRequest: application.EvaluationRequest{MaxScore: 2}, Closed: true, Result: exam.NodeState{Score: &full, Assisted: true}}, "сам восполни существенный пробел"},
		{application.FeedbackRequest{Closed: true}, "сам восполни существенный пробел"},
	} {
		tc.r.Node = node
		tc.r.Dialogue = turns
		p := feedbackPrompt(tc.r)
		if !strings.Contains(p, tc.want) || !strings.Contains(p, "не придирайся к длине") {
			t.Fatalf("wrong feedback instructions: %s", p)
		}
		if strings.Contains(evaluationPrompt(tc.r.EvaluationRequest), "reaction;") {
			t.Fatal("grading still generates feedback before the engine decision")
		}
	}
}

func TestRephraseTransitionDoesNotCallTheModelOrGiveHints(t *testing.T) {
	f := &fakeGateway{}
	g := serve(t, f, time.Second)
	for _, verdict := range []exam.Verdict{exam.VerdictInsufficient, exam.VerdictAnswered} {
		text, err := g.Feedback(context.Background(), application.FeedbackRequest{Next: exam.KindRephrase, Verdict: verdict})
		variants := rephraseTransitions
		if verdict == exam.VerdictInsufficient {
			variants = offTopicTransitions
		}
		if err != nil || !slices.Contains(variants, text) || f.count() != 0 {
			t.Fatalf("transition = %q, %v, calls=%d", text, err, f.count())
		}
		if verdict == exam.VerdictAnswered && strings.Contains(text, "не связан") {
			t.Fatal("weak answer called off-topic")
		}
	}
}

func TestHintTransitionAlsoAppliesToFollowups(t *testing.T) {
	f := &fakeGateway{}
	g := serve(t, f, time.Second)
	for _, next := range []exam.TurnKind{exam.KindRephrase, exam.KindFollowup} {
		text, err := g.Feedback(context.Background(), application.FeedbackRequest{Next: next, Verdict: exam.VerdictHintRequested})
		if err != nil || !slices.Contains(hintTransitions, text) || f.count() != 0 {
			t.Fatalf("hint transition = %q, %v, calls=%d", text, err, f.count())
		}
	}
}

func TestFollowupTransitionDoesNotCallTheModelOrOverpraiseWeakKnowledge(t *testing.T) {
	f := &fakeGateway{}
	g := serve(t, f, time.Second)
	partial := 1
	text, err := g.Feedback(context.Background(), application.FeedbackRequest{Next: exam.KindFollowup, Result: exam.NodeState{Score: &partial}})
	if err != nil || f.count() != 0 || strings.Contains(text, "?") || !slices.Contains(partialFollowupTransitions, text) {
		t.Fatalf("transition=%q, %v", text, err)
	}
}

func TestTransitionVariesAndDoesNotRepeatThePreviousIntroduction(t *testing.T) {
	r := application.FeedbackRequest{Next: exam.KindRephrase}
	seen := map[string]bool{}
	for i := 0; i < 30; i++ {
		r.Node.ID = fmt.Sprint(i)
		text := transition(r, rephraseTransitions)
		if text != transition(r, rephraseTransitions) {
			t.Fatal("retry changed wording")
		}
		seen[text] = true
		if len(r.Dialogue) > 0 && text == r.Dialogue[0].Text {
			t.Fatal("repeated the previous introduction")
		}
		r.Dialogue = []exam.Turn{{Speaker: exam.SpeakerGeek, Text: text}}
	}
	if len(seen) < 3 {
		t.Fatalf("only %d variants observed", len(seen))
	}
}

func TestExaminerPromptsLeaveOutOutcomes(t *testing.T) {
	n := node
	n.Outcomes = []string{"UNRELATED_OUTCOME_SHOULD_NOT_BE_SENT"}
	for _, kind := range []exam.TurnKind{exam.KindQuestion, exam.KindFollowup, exam.KindRephrase} {
		p := questionPrompt(application.QuestionRequest{Kind: kind, Node: n, Dialogue: turns})
		if strings.Contains(p, n.Outcomes[0]) || !strings.Contains(p, n.Content) {
			t.Fatalf("question prompt has the wrong source: %s", p)
		}
	}
	if p := feedbackPrompt(application.FeedbackRequest{EvaluationRequest: application.EvaluationRequest{MaxScore: 2, Node: n, Dialogue: turns}}); strings.Contains(p, n.Outcomes[0]) {
		t.Fatal("explanation contains outcomes")
	}
}

func TestGatewayAcceptsOnlyAScoreOnTheTopicScale(t *testing.T) {
	for _, tc := range []struct {
		score    string
		maximum  int
		valid    bool
		expected int
	}{
		{`2`, 2, true, 2},
		{`0`, 2, true, 0},
		{`1`, 1, true, 1},
		{`2`, 1, false, 0},
		{`-1`, 2, false, 0},
		{`1.5`, 2, false, 0},
		{`null`, 2, false, 0},
		{`"2"`, 2, false, 0},
	} {
		t.Run(fmt.Sprintf("%s of %d", tc.score, tc.maximum), func(t *testing.T) {
			f := &fakeGateway{answers: []string{`{"verdict":"answered","score":` + tc.score + `,"reasons":[],"evidence_lines":[2]}`}}
			g := serve(t, f, time.Second)
			a, err := g.Evaluate(context.Background(), application.EvaluationRequest{MaxScore: tc.maximum, Node: node, Dialogue: turns})
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v got %v", tc.valid, err)
			}
			if tc.valid && a.Score != tc.expected {
				t.Fatalf("score = %d", a.Score)
			}
			if !tc.valid && f.count() != 2 {
				t.Fatal("an invalid score was not retried")
			}
		})
	}
}

func TestEvaluationPromptStatesTheScaleOfThePrerequisite(t *testing.T) {
	prompt := evaluationPrompt(application.EvaluationRequest{MaxScore: 1, Kind: exam.KindQuestion, Node: node, Dialogue: turns})
	if !strings.Contains(prompt, "по шкале темы: 0 или 1") || strings.Contains(prompt, "0, 1 или 2") {
		t.Fatalf("prompt = %s", prompt)
	}
}

func TestGatewayDoesNotReuseFieldsOfARejectedAnswer(t *testing.T) {
	f := &fakeGateway{answers: []string{
		`{"verdict":"answered","score":2,"reasons":["основание"],"evidence_lines":[2]}`,
		`{"verdict":"answered","reasons":["основание"],"evidence_lines":[2]}`,
	}}
	g := serve(t, f, time.Second)
	if e, err := g.Evaluate(context.Background(), application.EvaluationRequest{MaxScore: 1, Node: node, Dialogue: turns}); err == nil {
		t.Fatalf("the second answer lacks a score but was accepted as %+v", e)
	}
	if len(f.requests) != 2 {
		t.Fatalf("requests = %d", len(f.requests))
	}
}

func TestGatewayFailureIsNotRetried(t *testing.T) {
	f := &fakeGateway{err: status.Error(codes.Unavailable, "no slots")}
	g := serve(t, f, time.Second)
	_, err := g.Question(context.Background(), application.QuestionRequest{Node: node})
	if status.Code(err) != codes.Unavailable || len(f.requests) != 1 {
		t.Fatalf("err = %v after %d calls", err, len(f.requests))
	}
}

func TestFailedCallKeepsTheGatewayRequestID(t *testing.T) {
	request := &llmgatewayv1.CompleteRequest{Complexity: llmgatewayv1.Complexity_COMPLEXITY_LOW}
	f := &fakeGateway{err: status.Error(codes.Unavailable, "no slots"), header: metadata.Pairs("x-request-id", "gateway-error-42")}
	if _, id, err := serve(t, f, time.Second).call(context.Background(), request); err == nil || id != "gateway-error-42" {
		t.Fatalf("header: id = %q, err = %v", id, err)
	}
	detailed, _ := status.New(codes.ResourceExhausted, "queue full").WithDetails(&errdetails.ErrorInfo{Reason: "QUEUE_FULL", Metadata: map[string]string{"request_id": "gateway-error-43"}})
	f = &fakeGateway{err: detailed.Err()}
	if _, id, err := serve(t, f, time.Second).call(context.Background(), request); err == nil || id != "gateway-error-43" {
		t.Fatalf("error info: id = %q, err = %v", id, err)
	}
}

func TestCancellingTheRequestStopsItInTheGateway(t *testing.T) {
	f := &fakeGateway{block: true, cancelled: make(chan struct{})}
	g := serve(t, f, time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := g.Question(ctx, application.QuestionRequest{Node: node})
		done <- err
	}()
	for f.count() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-f.cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("the gateway did not see the cancellation")
	}
	if err := <-done; err == nil {
		t.Fatal("a cancelled request succeeded")
	}
}
