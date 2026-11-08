package application

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AskarKasimov/ai-tutor/services/audio"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/coursegraph"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/exam"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/shared/fault"
)

type memoryStore struct {
	mu       sync.Mutex
	subjects map[string]bool
	sessions map[string][]byte
	requests map[string]Request
}

func newMemoryStore() *memoryStore {
	return &memoryStore{subjects: map[string]bool{"subject": true}, sessions: map[string][]byte{}, requests: map[string]Request{}}
}

func decode(t []byte) exam.Session {
	var s exam.Session
	_ = json.Unmarshal(t, &s)
	return s
}

func (m *memoryStore) SubjectExists(_ context.Context, id string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.subjects[id], nil
}

func (m *memoryStore) Session(_ context.Context, owner, id string) (exam.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, ok := m.sessions[id]
	if s := decode(data); ok && s.OwnerID == owner {
		return s, nil
	}
	return exam.Session{}, ErrNotFound
}

func (m *memoryStore) ActiveSession(_ context.Context, owner, subject string) (exam.Session, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, data := range m.sessions {
		if s := decode(data); s.OwnerID == owner && s.SubjectID == subject && s.Status == exam.SessionActive {
			return s, true, nil
		}
	}
	return exam.Session{}, false, nil
}

func (m *memoryStore) Sessions(_ context.Context, owner, subject string) ([]exam.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []exam.Session{}
	for _, data := range m.sessions {
		if s := decode(data); s.OwnerID == owner && s.SubjectID == subject {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, nil
}

func (m *memoryStore) Request(_ context.Context, owner, key string) (Request, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.requests[owner+"/"+key]
	if ok {
		data, _ := json.Marshal(r.Response)
		r.Response = decode(data)
	}
	return r, ok, nil
}

func (m *memoryStore) Create(_ context.Context, s exam.Session, r Request) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, data := range m.sessions {
		if o := decode(data); o.OwnerID == s.OwnerID && o.SubjectID == s.SubjectID && o.Status == exam.SessionActive {
			return ErrActiveExists
		}
	}
	m.sessions[s.ID], _ = json.Marshal(s)
	m.requests[r.OwnerID+"/"+r.Key] = r
	return nil
}

func (m *memoryStore) SaveRequest(_ context.Context, r Request) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.requests[r.OwnerID+"/"+r.Key]; !ok {
		m.requests[r.OwnerID+"/"+r.Key] = r
	}
	return nil
}

func (m *memoryStore) Update(_ context.Context, s exam.Session, previous int, r *Request) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if decode(m.sessions[s.ID]).Version != previous {
		return ErrConflict
	}
	m.sessions[s.ID], _ = json.Marshal(s)
	if r != nil {
		m.requests[r.OwnerID+"/"+r.Key] = *r
	}
	return nil
}

type graphs struct{ graph *exam.Graph }

func (g *graphs) Current(context.Context, string) (exam.Graph, bool, error) {
	if g.graph == nil {
		return exam.Graph{}, false, nil
	}
	return *g.graph, true, nil
}

type texts map[string]string

func (t texts) Text(_ context.Context, owner, id string) (string, bool, error) {
	text, ok := t[owner+"/"+id]
	return text, ok, nil
}

// examiner grades by the answer text: "сильно" is 2/2/2/2, "частично"
// 2/2/1/1, anything else 0/1/0/0. Its reaction and explanation name the topic.
type examiner struct {
	mu           sync.Mutex
	fail         error
	questions    []QuestionRequest
	evals        int
	evaluated    []EvaluationRequest
	explained    []EvaluationRequest
	feedbacks    []FeedbackRequest
	feedbackFail error
	nameFail     error
}

func (e *examiner) InterpretName(_ context.Context, _, text string) (NameIntent, error) {
	if e.nameFail != nil {
		return NameIntent{}, e.nameFail
	}
	switch text {
	case "Обращайся ко мне, Андрюха. Андрюха.":
		return NameIntent{Name: "Андрюха"}, nil
	case "Меня зовут Кирилл, сильно":
		return NameIntent{Name: "Кирилл", HasAnswer: true}, nil
	case "Меня зовут Никита Зайгайнов, частично":
		return NameIntent{Name: "Никита Зайгайнов", HasAnswer: true}, nil
	case "Меня зовут философ Ягон Дон":
		return NameIntent{Name: "философ Ягон Дон"}, nil
	default:
		return NameIntent{}, nil
	}
}

func (e *examiner) Question(_ context.Context, r QuestionRequest) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.fail != nil {
		return "", e.fail
	}
	e.questions = append(e.questions, r)
	return fmt.Sprintf("%s: %s?", r.Kind, r.Node.Title), nil
}

func (e *examiner) Evaluate(_ context.Context, r EvaluationRequest) (Assessment, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.fail != nil {
		return Assessment{}, e.fail
	}
	e.evals++
	e.evaluated = append(e.evaluated, r)
	last := r.Dialogue[len(r.Dialogue)-1]
	a := Assessment{}
	switch {
	case strings.Contains(last.Text, "НЕПОНЯТНО"):
		a.Evaluation.Verdict = exam.VerdictClarifyQuestion
		a.Clarification = "Какая часть вопроса непонятна?"
		a.ClarificationPending = true
	case r.ClarificationPending:
		a.Evaluation.Verdict = exam.VerdictClarifyQuestion
		a.Clarification = "Это про условия задачи. Теперь ответь на вопрос."
	case strings.Contains(last.Text, "ПОСТОРОННИЙ РАЗГОВОР"):
		a.Evaluation.Verdict = exam.VerdictInsufficient
	case strings.Contains(last.Text, "поправка обращения"):
		a.Evaluation.Verdict = exam.VerdictAddressCorrection
	case strings.Contains(last.Text, "поясни формулировку"):
		a.Evaluation.Verdict = exam.VerdictClarifyQuestion
		a.Clarification = "Объясни смысл исходного вопроса своими словами; условия остаются прежними."
	case strings.Contains(last.Text, "пауза"):
		a.Evaluation.Verdict = exam.VerdictPause
	case strings.Contains(last.Text, "подсказка"):
		a.Evaluation = exam.Evaluation{Verdict: exam.VerdictHintRequested, Evidence: []int{last.Line}}
	case strings.Contains(last.Text, "не знаю"):
		a.Evaluation = exam.Evaluation{Verdict: exam.VerdictDontKnow}
	case strings.Contains(last.Text, "сильно"):
		a.Evaluation = exam.Evaluation{Verdict: exam.VerdictAnswered, Score: r.MaxScore, Evidence: []int{last.Line}}
	case strings.Contains(last.Text, "на 1"):
		// Partial and staying partial after the followup.
		a.Evaluation = exam.Evaluation{Verdict: exam.VerdictAnswered, Score: min(1, r.MaxScore), Evidence: []int{last.Line}}
	case strings.Contains(last.Text, "частично"):
		// Partial, completed by the followup.
		score := min(1, r.MaxScore)
		if r.Kind == exam.KindFollowup {
			score = r.MaxScore
		}
		a.Evaluation = exam.Evaluation{Verdict: exam.VerdictAnswered, Score: score, Evidence: []int{last.Line}}
	default:
		a.Evaluation = exam.Evaluation{Verdict: exam.VerdictAnswered, Score: 0, Evidence: []int{last.Line}}
	}
	if strings.Contains(last.Text, "женский род") {
		a.AddressPreference = exam.AddressFeminine
	}
	return a, nil
}

func (e *examiner) Feedback(_ context.Context, r FeedbackRequest) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.fail != nil {
		return "", e.fail
	}
	e.feedbacks = append(e.feedbacks, r)
	if e.feedbackFail != nil {
		return "", e.feedbackFail
	}
	if r.Verdict == exam.VerdictDontKnow {
		e.explained = append(e.explained, r.EvaluationRequest)
		return "Объяснение: " + r.Node.Title + ".", nil
	}
	text := "Реакция: " + r.Node.Title + "."
	if r.Closed {
		text += " Разбор: " + r.Node.Title + "."
	}
	return text, nil
}

func wav(samples int) []byte {
	data := make([]byte, 44+samples*2)
	copy(data[0:4], "RIFF")
	binary.LittleEndian.PutUint32(data[4:8], uint32(36+samples*2))
	copy(data[8:16], "WAVEfmt ")
	binary.LittleEndian.PutUint32(data[16:20], 16)
	binary.LittleEndian.PutUint16(data[20:22], 1)
	binary.LittleEndian.PutUint16(data[22:24], 1)
	binary.LittleEndian.PutUint32(data[24:28], 8000)
	binary.LittleEndian.PutUint32(data[28:32], 16000)
	binary.LittleEndian.PutUint16(data[32:34], 2)
	binary.LittleEndian.PutUint16(data[34:36], 16)
	copy(data[36:40], "data")
	binary.LittleEndian.PutUint32(data[40:44], uint32(samples*2))
	return data
}

type voice struct{ calls []string }

func (v *voice) Synthesize(_ context.Context, text string) ([]byte, error) {
	v.calls = append(v.calls, text)
	return wav(10), nil
}

type audioStore map[string][]byte

func (a audioStore) Get(_ context.Context, key string) ([]byte, bool, error) {
	data, ok := a[key]
	return data, ok, nil
}

func (a audioStore) Put(_ context.Context, key string, data []byte) error {
	a[key] = data
	return nil
}

type ids struct{ n int }

func (i *ids) New(prefix string) (string, error) {
	i.n++
	return fmt.Sprintf("%s_%d", prefix, i.n), nil
}

type fixture struct {
	service  *Service
	store    *memoryStore
	graphs   *graphs
	examiner *examiner
	voice    *voice
	audio    audioStore
	key      int
}

func loadGraph(t *testing.T) exam.Graph {
	t.Helper()
	data, err := os.ReadFile("../../../../../../api/fixtures/course-graph.json")
	if err != nil {
		t.Fatal(err)
	}
	var g exam.Graph
	if err := json.Unmarshal(data, &g); err != nil {
		t.Fatal(err)
	}
	g.Version = "v1"
	return g
}

func newFixture(t *testing.T) *fixture {
	g := loadGraph(t)
	f := &fixture{store: newMemoryStore(), graphs: &graphs{graph: &g}, examiner: &examiner{}, voice: &voice{}, audio: audioStore{}}
	f.service = New(f.graphs, f.store, texts{"student/tr": "сильно отвечаю голосом"}, f.examiner, NewSpeechCache(f.voice, f.audio, staticVoiceName(""), time.Minute), &ids{}, func() time.Time { return time.Unix(100, 0) }, Config{})
	// Ties of coverage keep study order, so the tests know the next topic.
	f.service.rng = func() *rand.Rand { return nil }
	return f
}

func (f *fixture) nextKey() string {
	f.key++
	return fmt.Sprintf("key-%d", f.key)
}

func (f *fixture) answer(t *testing.T, s exam.Session, a Answer) exam.Session {
	t.Helper()
	turn, ok := s.CurrentTurn()
	if !ok {
		t.Fatal("no current turn")
	}
	a.TurnID = turn.ID
	next, err := f.service.Answer(context.Background(), "student", s.ID, f.nextKey(), a)
	if err != nil {
		t.Fatal(err)
	}
	return next
}

func code(err error) string {
	var f *fault.Error
	if errors.As(err, &f) {
		return f.Code
	}
	return ""
}

func TestStartIsIdempotentAndReturnsTheActiveSession(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	s, created, err := f.service.Start(ctx, "student", "subject", "start")
	if err != nil || !created || s.Status != exam.SessionActive || len(s.Turns) != 1 {
		t.Fatalf("start: %+v created=%v err=%v", s, created, err)
	}
	first := s.Turns[0]
	if first.Kind != exam.KindQuestion || *first.NodeID != "a-star" || first.Text != "Что изменится в работе A*, если эвристика начнёт переоценивать расстояние до цели?" {
		t.Fatalf("first turn = %+v (a node question is used as is)", first)
	}
	if again, created, err := f.service.Start(ctx, "student", "subject", "start"); err != nil || !created || again.ID != s.ID {
		t.Fatalf("replay: %v %v %v", again.ID, created, err)
	}
	if again, created, err := f.service.Start(ctx, "student", "subject", "other"); err != nil || created || again.ID != s.ID {
		t.Fatalf("second start: %v %v %v", again.ID, created, err)
	}
	if _, _, err := f.service.Start(ctx, "student", "missing", "missing"); code(err) != "SUBJECT_NOT_FOUND" {
		t.Fatalf("missing subject: %v", err)
	}
	if _, err := f.service.Get(ctx, "intruder", s.ID); code(err) != "SESSION_NOT_FOUND" {
		t.Fatalf("other owner: %v", err)
	}
}

func TestResumeKeyKeepsItsResult(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	s, _, _ := f.service.Start(ctx, "student", "subject", "initial")
	resumed, created, err := f.service.Start(ctx, "student", "subject", "resume")
	if err != nil || created || resumed.ID != s.ID {
		t.Fatalf("resume: %v %v", created, err)
	}
	// After an answer and after completion the resume key still returns the
	// session as it was resumed, and never starts a new diagnostic.
	next := f.answer(t, s, Answer{DontKnow: true})
	again, created, err := f.service.Start(ctx, "student", "subject", "resume")
	if err != nil || created || again.ID != s.ID || again.Version != resumed.Version || next.Version == resumed.Version {
		t.Fatalf("replay after an answer: version %d, want %d (%v)", again.Version, resumed.Version, err)
	}
	for next.Status == exam.SessionActive {
		next = f.answer(t, next, Answer{DontKnow: true})
	}
	if again, created, err := f.service.Start(ctx, "student", "subject", "resume"); err != nil || created || again.ID != s.ID {
		t.Fatalf("replay after completion started %v %v", created, err)
	}
	if _, _, err := f.service.Start(ctx, "student", "other", "resume"); code(err) != "IDEMPOTENCY_KEY_REUSED" {
		t.Fatalf("resume key with another course: %v", err)
	}
}

func TestHistoryListsOwnDiagnosticsOfTheCourse(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	s, _, _ := f.service.Start(ctx, "student", "subject", "first")
	for s.Status == exam.SessionActive {
		s = f.answer(t, s, Answer{DontKnow: true})
	}
	next, _, _ := f.service.Start(ctx, "student", "subject", "second")
	history, err := f.service.History(ctx, "student", "subject")
	if err != nil || len(history) != 2 || history[0].ID != next.ID || history[1].Status != exam.SessionCompleted {
		t.Fatalf("history = %d %v", len(history), err)
	}
	if other, _ := f.service.History(ctx, "intruder", "subject"); len(other) != 0 {
		t.Fatal("another student's diagnostics listed")
	}
	if _, err := f.service.History(ctx, "student", " "); code(err) != "VALIDATION_ERROR" {
		t.Fatalf("blank course: %v", err)
	}
}

func TestStartWithoutGraph(t *testing.T) {
	f := newFixture(t)
	f.graphs.graph = nil
	if _, _, err := f.service.Start(context.Background(), "student", "subject", "k"); code(err) != "GRAPH_NOT_READY" {
		t.Fatalf("err = %v", err)
	}
}

func TestDiagnosticRunsToTheResult(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	s, _, _ := f.service.Start(ctx, "student", "subject", "start")
	if _, err := f.service.Result(ctx, "student", s.ID); code(err) != "SESSION_NOT_COMPLETED" {
		t.Fatalf("early result: %v", err)
	}
	// a-star: don't know (button) -> its prerequisites; every other topic is
	// answered fully by voice or text.
	s = f.answer(t, s, Answer{DontKnow: true})
	if f.examiner.evals != 0 {
		t.Fatal("the don't know button must not be evaluated by the model")
	}
	for s.Status == exam.SessionActive {
		turn, _ := s.CurrentTurn()
		if turn.Kind == exam.KindFollowup {
			s = f.answer(t, s, Answer{TranscriptionID: "tr"})
		} else {
			s = f.answer(t, s, Answer{Text: "частично, отвечаю текстом"})
		}
	}
	last := s.Turns[len(s.Turns)-1]
	if last.Kind != exam.KindClosing || s.CompletedAt == nil || s.Exam.Progress() != 100 {
		t.Fatalf("last turn %+v completed=%v", last, s.CompletedAt)
	}
	for i, turn := range s.Turns {
		if turn.Line != i+1 {
			t.Fatalf("line %d at %d", turn.Line, i)
		}
	}
	r, err := f.service.Result(ctx, "student", s.ID)
	if err != nil {
		t.Fatal(err)
	}
	// a-star is a gap; its prerequisites are mastered, so it is the gap itself.
	if *r.Grade != exam.GradeA || *r.ScorePercent != 91 || r.Score != 18.3 || r.Maximum != 20 || len(r.Gaps) != 1 || r.Gaps[0].NodeID != "a-star" {
		t.Fatalf("result = %+v gaps=%+v", r, r.Gaps)
	}
	if _, err := f.service.Answer(ctx, "student", s.ID, "late", Answer{TurnID: last.ID, Text: "ещё"}); code(err) != "SESSION_NOT_ACTIVE" {
		t.Fatalf("answer after completion: %v", err)
	}
}

func TestGeekRepliesToTheAnswerBeforeTheNextQuestion(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	s, _, _ := f.service.Start(ctx, "student", "subject", "start")
	if s.Turns[0].Recap != "" || strings.HasPrefix(s.Turns[0].Text, "Реакция") {
		t.Fatalf("the first question has a reply: %+v", s.Turns[0])
	}
	first, _ := s.Exam.Graph.Node(*s.Turns[0].NodeID)

	// The topic goes on with the followup: a reaction without the answer.
	s = f.answer(t, s, Answer{Text: "частично"})
	followup := s.Turns[len(s.Turns)-1]
	if followup.Kind != exam.KindFollowup || followup.Recap != "" ||
		!strings.HasPrefix(followup.Text, "Реакция: "+first.Title+".\n\n") {
		t.Fatalf("followup = %+v", followup)
	}

	// The topic is closed: the explanation leads the next question as a recap.
	s = f.answer(t, s, Answer{Text: "частично"})
	next := s.Turns[len(s.Turns)-1]
	recap := "Реакция: " + first.Title + ". Разбор: " + first.Title + ".\n\n"
	if *next.NodeID == first.ID || next.Recap != recap || !strings.HasPrefix(next.Text, recap) || len(next.Text) == len(recap) {
		t.Fatalf("next question = %+v", next)
	}

	// The button is not evaluated; its explanation is asked on its own and
	// must keep the prerequisites that Geek may ask next.
	node, _ := s.Exam.Graph.Node(*next.NodeID)
	s = f.answer(t, s, Answer{DontKnow: true})
	if len(f.examiner.explained) != 1 || f.examiner.explained[0].Node.ID != node.ID ||
		len(f.examiner.explained[0].Prerequisites) != len(node.Prerequisites) {
		t.Fatalf("explained = %+v", f.examiner.explained)
	}
	after := s.Turns[len(s.Turns)-1]
	if after.Recap != "Объяснение: "+node.Title+".\n\n" || !strings.HasPrefix(after.Text, after.Recap) {
		t.Fatalf("turn after the button = %+v", after)
	}
}

func TestRepliesKeepEveryUncheckedPrerequisiteOfTheChain(t *testing.T) {
	f := newFixture(t)
	*f.graphs.graph = exam.Graph{Version: "chain", Nodes: []exam.Node{
		{ID: "leaf", Title: "Лист", Content: "Лист."},
		{ID: "middle", Title: "Середина", Content: "Середина.", Prerequisites: []string{"leaf"}},
		{ID: "top", Title: "Верх", Content: "Верх.", Prerequisites: []string{"middle"}},
	}}
	ctx := context.Background()
	s, _, _ := f.service.Start(ctx, "student", "subject", "start")
	// Evaluated answers send the chain to the model too.
	s = f.answer(t, s, Answer{Text: "слабо"})
	s = f.answer(t, s, Answer{DontKnow: true})
	s = f.answer(t, s, Answer{DontKnow: true})
	if turn, _ := s.CurrentTurn(); *turn.NodeID != "leaf" {
		t.Fatalf("current topic = %s", *turn.NodeID)
	}
	want := [][]string{{"Середина", "Лист"}, {"Лист"}}
	if len(f.examiner.explained) != 2 {
		t.Fatalf("explained = %+v", f.examiner.explained)
	}
	for i, r := range f.examiner.explained {
		if !slices.Equal(r.Prerequisites, want[i]) {
			t.Fatalf("explanation %d of %s keeps %v, want %v", i, r.Node.ID, r.Prerequisites, want[i])
		}
	}
	if !slices.Equal(f.examiner.evaluated[0].Prerequisites, want[0]) {
		t.Fatalf("evaluation keeps %v", f.examiner.evaluated[0].Prerequisites)
	}
}

func TestModelFailureLeavesTheSessionAndTheRetrySucceeds(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	s, _, _ := f.service.Start(ctx, "student", "subject", "start")
	turn := s.Turns[0]
	f.examiner.fail = errors.New("gateway down")
	_, err := f.service.Answer(ctx, "student", s.ID, "answer", Answer{TurnID: turn.ID, Text: "частично"})
	if code(err) != "MODEL_UNAVAILABLE" {
		t.Fatalf("err = %v", err)
	}
	if stored, _ := f.service.Get(ctx, "student", s.ID); len(stored.Turns) != 1 || stored.Version != s.Version {
		t.Fatalf("session changed on failure: %d turns", len(stored.Turns))
	}
	f.examiner.fail = nil
	next, err := f.service.Answer(ctx, "student", s.ID, "answer", Answer{TurnID: turn.ID, Text: "частично"})
	if err != nil || len(next.Turns) != 3 {
		t.Fatalf("retry: %v %d", err, len(next.Turns))
	}
	replay, err := f.service.Answer(ctx, "student", s.ID, "answer", Answer{TurnID: turn.ID, Text: "частично"})
	if err != nil || replay.Version != next.Version || f.examiner.evals != 1 {
		t.Fatalf("replay: %v version=%d evals=%d", err, replay.Version, f.examiner.evals)
	}
	if _, err := f.service.Answer(ctx, "student", s.ID, "answer", Answer{TurnID: turn.ID, Text: "другое"}); code(err) != "IDEMPOTENCY_KEY_REUSED" {
		t.Fatalf("reused key: %v", err)
	}
	if _, err := f.service.Answer(ctx, "student", s.ID, "stale", Answer{TurnID: turn.ID, Text: "ещё раз"}); code(err) != "TURN_NOT_CURRENT" {
		t.Fatalf("stale turn: %v", err)
	}
}

func TestFeedbackReceivesTheEngineOutcome(t *testing.T) {
	for _, tc := range []struct {
		name, answer string
		closed       bool
		next         exam.TurnKind
		score        *int
	}{
		{"complete", "сильно", true, exam.KindQuestion, ptr(2)},
		{"partial", "частично", false, exam.KindFollowup, nil},
		{"weak", "слабо", false, exam.KindRephrase, nil},
		{"give-up", "не знаю", true, exam.KindQuestion, ptr(0)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			s, _, err := f.service.Start(context.Background(), "student", "subject", "start")
			if err != nil {
				t.Fatal(err)
			}
			s = f.answer(t, s, Answer{Text: tc.answer})
			r := f.examiner.feedbacks[0]
			if r.Closed != tc.closed || r.Next != tc.next {
				t.Fatalf("feedback decision = %+v", r)
			}
			if tc.score != nil && (r.Result.Score == nil || *r.Result.Score != *tc.score) {
				t.Fatalf("feedback score = %v, want %d", r.Result.Score, *tc.score)
			}
			if tc.score != nil && *tc.score == 2 && len(r.Prerequisites) != 0 {
				t.Fatalf("already credited prerequisites still protected: %v", r.Prerequisites)
			}
		})
	}
}

func TestFeedbackFailureDoesNotCommitTheEvaluation(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	s, _, _ := f.service.Start(ctx, "student", "subject", "start")
	a := Answer{TurnID: s.Turns[0].ID, Text: "сильно"}
	f.examiner.feedbackFail = errors.New("feedback unavailable")
	_, err := f.service.Answer(ctx, "student", s.ID, "answer", a)
	if code(err) != "MODEL_UNAVAILABLE" {
		t.Fatalf("err = %v", err)
	}
	stored, _ := f.service.Get(ctx, "student", s.ID)
	if stored.Version != s.Version || len(stored.Turns) != 1 || stored.Exam.Nodes[*s.Turns[0].NodeID].Score != nil {
		t.Fatal("feedback failure committed grading or dialogue")
	}
	f.examiner.feedbackFail = nil
	if _, err := f.service.Answer(ctx, "student", s.ID, "answer", a); err != nil {
		t.Fatal(err)
	}
}

func TestMapFeedbackUsesTheSelectedVariantAndPreservesCurrentTraversal(t *testing.T) {
	for _, answer := range []string{"сильно", "частично", "на 1"} {
		t.Run(answer, func(t *testing.T) {
			f := newFixture(t)
			variants := []exam.Variant{
				{Question: "Первое задание?", Rephrase: "Проще первое?", Content: "Эталон первого", AssessmentGuidance: "Рубрика первого"},
				{Question: "Второе задание?", Rephrase: "Проще второе?", Content: "Эталон второго", AssessmentGuidance: "Рубрика второго"},
			}
			main := exam.Node{ID: "main", Title: "Основная компетенция", Questions: []string{variants[0].Question},
				Content: variants[0].Content, AssessmentGuidance: variants[0].AssessmentGuidance,
				Variants: variants, Prerequisites: []string{"basic"}}
			*f.graphs.graph = exam.Graph{Version: "map", Source: coursegraph.SourceMap, Nodes: []exam.Node{
				{ID: "basic", Title: "Базовая компетенция", Content: "База", Questions: []string{"Базовое задание?"}}, main,
			}}
			// Exercise the second variant, rather than accidentally using fields
			// of the first one retained on the published graph's node.
			f.service.rng = func() *rand.Rand {
				for seed := uint64(0); ; seed++ {
					rng := rand.New(rand.NewPCG(seed, 1))
					if rng.IntN(2) == 1 {
						return rand.New(rand.NewPCG(seed, 1))
					}
				}
			}
			f.service.cfg.MaxQuestions = 1
			s, _, err := f.service.Start(context.Background(), "student", "subject", "start")
			if err != nil {
				t.Fatal(err)
			}
			selected, _ := s.Exam.Graph.Node("main")
			if selected.MainQuestion() != variants[1].Question || s.Exam.MaxQuestions != 0 {
				t.Fatalf("map variant/limit = %+v, %d", selected, s.Exam.MaxQuestions)
			}
			s = f.answer(t, s, Answer{Text: answer})
			if answer != "сильно" {
				s = f.answer(t, s, Answer{Text: answer})
			}
			r := f.examiner.feedbacks[len(f.examiner.feedbacks)-1]
			if !r.Closed || r.Node.Content != variants[1].Content || r.Node.AssessmentGuidance != variants[1].AssessmentGuidance ||
				f.examiner.evaluated[0].Node.MainQuestion() != variants[1].Question {
				t.Fatalf("feedback lost selected variant: %+v", r)
			}
			if answer == "на 1" {
				if *r.Result.Score != 1 || s.Exam.Current.NodeID != "basic" ||
					!slices.Equal(r.Prerequisites, []string{"Базовая компетенция"}) {
					t.Fatalf("1 must descend and protect the basic task: %+v", r)
				}
				return
			}
			if *r.Result.Score != 2 {
				t.Fatalf("score = %v", *r.Result.Score)
			}
			if s.Exam.Nodes["basic"].Status != exam.StatusCredited || len(r.Prerequisites) != 0 {
				t.Fatal("the maximum no longer credits prerequisites")
			}
		})
	}
}

func TestAnswerValidation(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	s, _, _ := f.service.Start(ctx, "student", "subject", "start")
	turn := s.Turns[0].ID
	for name, a := range map[string]Answer{
		"none":  {TurnID: turn},
		"two":   {TurnID: turn, Text: "a", DontKnow: true},
		"blank": {TurnID: turn, Text: "   "},
		"long":  {TurnID: turn, Text: strings.Repeat("я", 4001)},
	} {
		if _, err := f.service.Answer(ctx, "student", s.ID, name, a); code(err) != "VALIDATION_ERROR" {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if _, err := f.service.Answer(ctx, "student", s.ID, "tr", Answer{TurnID: turn, TranscriptionID: "foreign"}); code(err) != "TRANSCRIPTION_NOT_FOUND" {
		t.Fatalf("foreign transcription: %v", err)
	}
}

func TestRebuiltGraphCancelsTheActiveDiagnostic(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	s, _, _ := f.service.Start(ctx, "student", "subject", "start")
	rebuilt := *f.graphs.graph
	rebuilt.Version = "v2"
	f.graphs.graph = &rebuilt
	if got, err := f.service.Get(ctx, "student", s.ID); err != nil || got.Status != exam.SessionCancelled {
		t.Fatalf("get: %v %v", got.Status, err)
	}
	next, created, err := f.service.Start(ctx, "student", "subject", "restart")
	if err != nil || !created || next.ID == s.ID || next.Exam.Graph.Version != "v2" {
		t.Fatalf("restart: %v %v", created, err)
	}
}

func TestTurnAudioIsSynthesizedByPartsAndCached(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	g := *f.graphs.graph
	g.Nodes[4].Questions = []string{strings.Repeat("Очень длинный вопрос про поиск. ", 30)}
	f.graphs.graph = &g
	s, _, _ := f.service.Start(ctx, "student", "subject", "start")
	turn := s.Turns[0]
	data, err := f.service.TurnAudio(ctx, "student", s.ID, turn.ID, AudioWhole)
	if err != nil || !audio.ValidWAV(data) || len(f.voice.calls) < 2 {
		t.Fatalf("audio: %v calls=%d", err, len(f.voice.calls))
	}
	for _, call := range f.voice.calls {
		if len([]rune(call)) > speechLimit {
			t.Fatalf("part of %d runes", len([]rune(call)))
		}
	}
	calls := len(f.voice.calls)
	if _, err := f.service.TurnAudio(ctx, "student", s.ID, turn.ID, AudioWhole); err != nil || len(f.voice.calls) != calls {
		t.Fatalf("cached audio synthesized again: %v", err)
	}
	next := f.answer(t, s, Answer{Text: "частично"})
	if _, err := f.service.TurnAudio(ctx, "student", s.ID, next.Turns[1].ID, AudioWhole); code(err) != "TURN_NOT_FOUND" {
		t.Fatalf("student turn audio: %v", err)
	}
}

func TestTurnAudioVoicesTheReplyAndTheQuestionApart(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	s, _, _ := f.service.Start(ctx, "student", "subject", "start")
	first := s.Turns[0]
	if first.Reply != "" {
		t.Fatalf("the first question has a reply: %+v", first)
	}
	if _, err := f.service.TurnAudio(ctx, "student", s.ID, first.ID, AudioReply); code(err) != "TURN_PART_NOT_FOUND" {
		t.Fatalf("reply part of the first question: %v", err)
	}
	if _, err := f.service.TurnAudio(ctx, "student", s.ID, first.ID, "everything"); code(err) != "VALIDATION_ERROR" {
		t.Fatalf("unknown part: %v", err)
	}

	s = f.answer(t, s, Answer{Text: "частично"})
	led := s.Turns[len(s.Turns)-1]
	reply, question := led.Parts()
	if led.Reply == "" || reply != led.Reply || question == "" || led.Text != led.Reply+"\n\n"+question {
		t.Fatalf("turn parts: reply=%q question=%q text=%q", reply, question, led.Text)
	}
	f.voice.calls = nil
	if data, err := f.service.TurnAudio(ctx, "student", s.ID, led.ID, AudioReply); err != nil || !audio.ValidWAV(data) {
		t.Fatalf("reply audio: %v", err)
	}
	if data, err := f.service.TurnAudio(ctx, "student", s.ID, led.ID, AudioQuestion); err != nil || !audio.ValidWAV(data) {
		t.Fatalf("question audio: %v", err)
	}
	if len(f.voice.calls) != 2 || f.voice.calls[0] != reply || f.voice.calls[1] != question {
		t.Fatalf("synthesized %q", f.voice.calls)
	}

	// A part is cached by its text: the same prepared question in another
	// student's diagnostic is not synthesized again.
	other, _, _ := f.service.Start(ctx, "other", "subject", "start-other")
	if _, err := f.service.TurnAudio(ctx, "other", other.ID, other.Turns[0].ID, AudioQuestion); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.TurnAudio(ctx, "student", s.ID, s.Turns[0].ID, AudioQuestion); err != nil || len(f.voice.calls) != 3 {
		t.Fatalf("the shared question was synthesized again: %v calls=%q", err, f.voice.calls)
	}
}

type rawMaterialsFake struct{ current RawMaterial }

func (f rawMaterialsFake) CurrentRaw(context.Context, string) (RawMaterial, error) {
	return f.current, nil
}

func TestRawMaterialRequiresOwnerAndMatchingDiagnosticSourceRevision(t *testing.T) {
	f := newFixture(t)
	revision := "revision-1"
	filename := "lecture.pdf"
	node := f.graphs.graph.Nodes[0]
	node.Sources = []exam.SourceCitation{{MaterialID: "material", MaterialRevision: &revision}}
	session := exam.Session{ID: "diagnostic", OwnerID: "student", Status: exam.SessionCompleted,
		Exam: &exam.Exam{Graph: exam.Graph{Version: "v1", Nodes: []exam.Node{node}}}}
	data, _ := json.Marshal(session)
	f.store.sessions[session.ID] = data
	f.service.SetRawMaterials(rawMaterialsFake{RawMaterial{Filename: filename, Revision: revision, Data: []byte("%PDF"), Found: true}})
	got, err := f.service.RawMaterial(context.Background(), "student", session.ID, "material")
	if err != nil || string(got.Data) != "%PDF" {
		t.Fatalf("raw material: %v %#v", err, got)
	}
	if _, err = f.service.RawMaterial(context.Background(), "other", session.ID, "material"); code(err) != "NOT_FOUND" {
		t.Fatalf("foreign session: %v", err)
	}
	if _, err = f.service.RawMaterial(context.Background(), "student", session.ID, "unlinked"); code(err) != "NOT_FOUND" {
		t.Fatalf("unlinked material: %v", err)
	}
	node.Sources = append(node.Sources, exam.SourceCitation{MaterialID: "legacy-source"})
	session.Exam.Graph.Nodes[0] = node
	data, _ = json.Marshal(session)
	f.store.sessions[session.ID] = data
	if _, err = f.service.RawMaterial(context.Background(), "student", session.ID, "legacy-source"); code(err) != "SOURCE_UNAVAILABLE" {
		t.Fatalf("legacy source without revision: %v", err)
	}
	f.service.SetRawMaterials(rawMaterialsFake{RawMaterial{Filename: filename, Revision: "revision-2", Found: true}})
	if _, err = f.service.RawMaterial(context.Background(), "student", session.ID, "material"); code(err) != "SOURCE_UNAVAILABLE" {
		t.Fatalf("replaced material: %v", err)
	}
}

func TestRequestedHintBypassesPreparedRephraseAndKeepsTopicOpen(t *testing.T) {
	f := newFixture(t)
	for i := range f.graphs.graph.Nodes {
		f.graphs.graph.Nodes[i].Rephrase = "Заранее подготовленная переформулировка?"
	}
	s, _, err := f.service.Start(context.Background(), "student", "subject", "start")
	if err != nil {
		t.Fatal(err)
	}
	id := *s.Turns[0].NodeID
	s = f.answer(t, s, Answer{Text: "Не знаю этот пример, нужна подсказка, а затем попробую ответить"})
	last := s.Turns[len(s.Turns)-1]
	if last.NodeID == nil || *last.NodeID != id || last.Kind != exam.KindRephrase || strings.Contains(last.Text, "Заранее подготовленная") {
		t.Fatalf("hint turn = %+v", last)
	}
	if len(f.examiner.questions) == 0 || !f.examiner.questions[len(f.examiner.questions)-1].Hint {
		t.Fatal("context hint was not generated")
	}
	s = f.answer(t, s, Answer{Text: "сильно"})
	if state := s.Exam.Nodes[id]; state.Score == nil || *state.Score != 1 || state.Status != exam.StatusAssisted {
		t.Fatalf("hint result = %+v", state)
	}
	if !f.examiner.evaluated[len(f.examiner.evaluated)-1].HintUsed {
		t.Fatal("grader did not receive hint context")
	}
}

func TestPreparedRephraseIsAskedWithoutTheModel(t *testing.T) {
	f := newFixture(t)
	for i := range f.graphs.graph.Nodes {
		f.graphs.graph.Nodes[i].Rephrase = "Проще про «" + f.graphs.graph.Nodes[i].Title + "»?"
	}
	ctx := context.Background()
	s, _, _ := f.service.Start(ctx, "student", "subject", "start")
	node, _ := s.Exam.Graph.Node(*s.Turns[0].NodeID)
	if node.MainQuestion() == "" || s.Turns[0].Text != node.Questions[0] {
		t.Fatalf("the first topic needs a prepared main question: %+v", s.Turns[0])
	}
	// A weak answer is graded D, so Geek rephrases the main question.
	s = f.answer(t, s, Answer{Text: "слабо"})
	turn := s.Turns[len(s.Turns)-1]
	if turn.Kind != exam.KindRephrase || !strings.HasSuffix(turn.Text, "\n\nПроще про «"+node.Title+"»?") {
		t.Fatalf("rephrase = %+v", turn)
	}
	if _, question := turn.Parts(); question != "Проще про «"+node.Title+"»?" {
		t.Fatalf("voiced question part = %q", question)
	}
	for _, r := range f.examiner.questions {
		if r.Kind == exam.KindRephrase {
			t.Fatal("a prepared rephrase was generated again")
		}
	}
}

func TestRephraseIsGeneratedWhenItIsNotPrepared(t *testing.T) {
	for name, edit := range map[string]func(*exam.Node){
		// Graphs and diagnostics from before the decision of 18.10.2026.
		"no rephrase": func(n *exam.Node) { n.Rephrase = "" },
		// The main question is generated at runtime, so a stored rephrase
		// rephrases another question.
		"no main question": func(n *exam.Node) { n.Questions, n.Rephrase = nil, "Чужая переформулировка?" },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			for i := range f.graphs.graph.Nodes {
				edit(&f.graphs.graph.Nodes[i])
			}
			s, _, _ := f.service.Start(context.Background(), "student", "subject", "start")
			node, _ := s.Exam.Graph.Node(*s.Turns[0].NodeID)
			s = f.answer(t, s, Answer{Text: "слабо"})
			turn := s.Turns[len(s.Turns)-1]
			last := f.examiner.questions[len(f.examiner.questions)-1]
			if turn.Kind != exam.KindRephrase || last.Kind != exam.KindRephrase || last.Ahead ||
				!strings.HasSuffix(turn.Text, "rephrase: "+node.Title+"?") {
				t.Fatalf("rephrase = %+v, request = %+v", turn, last)
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }
