package exam

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"slices"
	"strings"
	"testing"
)

func fixture(t *testing.T) Graph {
	t.Helper()
	data, err := os.ReadFile("../../../../../api/fixtures/course-graph.json")
	if err != nil {
		t.Fatal(err)
	}
	var g Graph
	if err := json.Unmarshal(data, &g); err != nil {
		t.Fatal(err)
	}
	if err := g.Validate(); err != nil {
		t.Fatal(err)
	}
	return g
}

var (
	// strong is the maximum of any topic; the scale caps it for a prerequisite.
	strong  = Evaluation{Verdict: VerdictAnswered, Score: 2, Reasons: []string{"верно"}}
	partial = Evaluation{Verdict: VerdictAnswered, Score: 1}
	weak    = Evaluation{Verdict: VerdictAnswered, Score: 0}
	unknown = Evaluation{Verdict: VerdictDontKnow}
	vague   = Evaluation{Verdict: VerdictInsufficient}
)

func TestRepeatedHintRequestCannotLoop(t *testing.T) {
	e, first := NewExam(fixture(t), 0, nil)
	e.Apply(Evaluation{Verdict: VerdictHintRequested})
	next := e.Apply(Evaluation{Verdict: VerdictHintRequested})
	if next.NodeID == first.NodeID || e.Nodes[first.NodeID].Status != StatusInsufficient {
		t.Fatalf("repeat hint kept topic open: %+v", next)
	}
}

// run answers every step with answer(step) and returns the asked steps.
func run(t *testing.T, e *Exam, first Step, answer func(Step) Evaluation) []Step {
	t.Helper()
	steps := []Step{}
	progress := 0
	for step := first; step.Kind != KindClosing; {
		steps = append(steps, step)
		if len(steps) > 100 {
			t.Fatal("exam does not end")
		}
		step = e.Apply(answer(step))
		if p := e.Progress(); p < progress {
			t.Fatalf("progress went down: %d -> %d", progress, p)
		} else {
			progress = p
		}
		if !e.Completed() && progress >= 100 {
			t.Fatal("progress reached 100 before completion")
		}
	}
	if e.Progress() != 100 {
		t.Fatalf("completed exam progress = %d", e.Progress())
	}
	return steps
}

func asked(steps []Step, kind TurnKind) []string {
	out := []string{}
	for _, s := range steps {
		if s.Kind == kind {
			out = append(out, s.NodeID)
		}
	}
	return out
}

func TestTopLevelAndClosure(t *testing.T) {
	g := fixture(t)
	if got := g.TopLevel(); !slices.Equal(got, []string{"a-star", "simulated-annealing", "mcts", "agents-peas"}) {
		t.Fatalf("top level = %v", got)
	}
	closure := g.Closure("a-star")
	slices.Sort(closure)
	if want := []string{"heuristic", "heuristic-admissibility", "search-space", "uninformed-search"}; !slices.Equal(closure, want) {
		t.Fatalf("closure = %v", closure)
	}
}

func TestResultNodesCarryTheirPrerequisites(t *testing.T) {
	g := fixture(t)
	e, _ := NewExam(g, 0, nil)
	r := Session{ID: "s", Exam: e}.Result()
	for i, n := range r.Nodes {
		if !slices.Equal(n.Requires, g.Nodes[i].Prerequisites) {
			t.Fatalf("%s requires = %v, want %v", n.NodeID, n.Requires, g.Nodes[i].Prerequisites)
		}
		if n.Requires == nil {
			t.Fatalf("%s requires is nil", n.NodeID)
		}
	}
	// The result is a copy: changing it does not touch the graph snapshot.
	for _, n := range r.Nodes {
		if len(n.Requires) > 0 {
			n.Requires[0] = "changed"
			break
		}
	}
	for _, n := range e.Graph.Nodes {
		if slices.Contains(n.Prerequisites, "changed") {
			t.Fatal("result shares prerequisites with the graph")
		}
	}
}

func independent(n int) Graph {
	g := Graph{}
	for i := range n {
		g.Nodes = append(g.Nodes, Node{ID: fmt.Sprintf("n%05d", i), Title: "Тема"})
	}
	return g
}

func TestProgressStartsAtZeroAndCountsAnsweredQuestions(t *testing.T) {
	// The first question on screen used to show 1 of 8 asked as 12%.
	for _, limit := range []int{0, 8} {
		e, first := NewExam(independent(10), limit, nil)
		if first.Kind != KindQuestion || e.Progress() != 0 {
			t.Fatalf("limit=%d: progress at the first question = %d", limit, e.Progress())
		}
	}
	// Under the limit an answer moves the bar even before its topic closes:
	// the followup is on screen, one question of 8 is answered.
	e, _ := NewExam(independent(10), 8, nil)
	if next := e.Apply(partial); next.Kind != KindFollowup {
		t.Fatalf("next = %+v", next)
	}
	if p := e.Progress(); p != 12 {
		t.Fatalf("progress after 1 of 8 answered = %d", p)
	}
}

func TestUnclearAnswerWithoutRoomForARephraseIsInsufficient(t *testing.T) {
	e, _ := NewExam(independent(3), 1, nil)
	if next := e.Apply(vague); next.Kind != KindClosing {
		t.Fatalf("next = %+v", next)
	}
	if s := e.Nodes["n00000"]; s.Status != StatusInsufficient || s.Assisted {
		t.Fatalf("n00000 = %+v", s)
	}
}

func BenchmarkNewExam(b *testing.B) {
	shared := Graph{}
	for i := range 200 {
		shared.Nodes = append(shared.Nodes, Node{ID: fmt.Sprintf("b%03d", i), Title: "База"})
	}
	for i := range 2000 {
		shared.Nodes = append(shared.Nodes, Node{ID: fmt.Sprintf("t%04d", i), Title: "Тема",
			Prerequisites: []string{fmt.Sprintf("b%03d", i%200), fmt.Sprintf("b%03d", (i*7)%200)}})
	}
	for name, g := range map[string]Graph{"isolated-2000": independent(2000), "shared-2000": shared} {
		b.Run(name, func(b *testing.B) {
			for b.Loop() {
				NewExam(g, 8, nil)
			}
		})
	}
}

func showGraph(t *testing.T) Graph {
	t.Helper()
	data, err := os.ReadFile("../../../../../api/fixtures/intro-to-ai.weeks-1-2.ru.graph.json")
	if err != nil {
		t.Fatal(err)
	}
	var g Graph
	if err := json.Unmarshal(data, &g); err != nil {
		t.Fatal(err)
	}
	return g
}

func TestTopLevelTopicsCoveringMoreOfTheGraphComeFirst(t *testing.T) {
	g := showGraph(t)
	// AI-01-18 covers 15, 14 and 13; AI-01-08 covers 07 and 01; AI-01-17 adds
	// 16 (13 is covered). The rest add only themselves and keep study order.
	want := []string{"AI-01-18", "AI-01-08", "AI-01-17", "AI-01-02", "AI-01-03", "AI-01-04", "AI-01-06", "AI-01-09", "AI-01-10", "AI-01-19"}
	if got := coverageOrder(g, nil); !slices.Equal(got, want) {
		t.Fatalf("order = %v", got)
	}
	// A shuffle changes only the order among ties.
	tails := map[string]bool{}
	for seed := range uint64(20) {
		got := coverageOrder(g, rand.New(rand.NewPCG(seed, seed)))
		if !slices.Equal(got[:3], want[:3]) {
			t.Fatalf("seed %d: order = %v", seed, got)
		}
		tail := slices.Clone(got[3:])
		key := strings.Join(tail, ",")
		slices.Sort(tail)
		if !slices.Equal(tail, slices.Sorted(slices.Values(want[3:]))) {
			t.Fatalf("seed %d: tail = %v", seed, got[3:])
		}
		tails[key] = true
	}
	if len(tails) < 2 {
		t.Fatal("ties are never shuffled")
	}
	// The exam asks in this order, and a limit stops it after the topics that
	// cover the most: a strong student checks 18, 08, 17 and their
	// prerequisites (9 of 16 topics) within 3 questions.
	e, first := NewExam(g, 3, nil)
	steps := run(t, e, first, func(Step) Evaluation { return strong })
	if got := asked(steps, KindQuestion); !slices.Equal(got, want[:3]) {
		t.Fatalf("questions = %v", got)
	}
	checked := 0
	for _, s := range e.Nodes {
		if s.Status != StatusUnchecked {
			checked++
		}
	}
	if checked != 9 {
		t.Fatalf("checked %d topics", checked)
	}
}

func TestValidateRejectsCyclesAndUnknownPrerequisites(t *testing.T) {
	cycle := Graph{Nodes: []Node{{ID: "a", Title: "A", Prerequisites: []string{"b"}}, {ID: "b", Title: "B", Prerequisites: []string{"a"}}}}
	unknown := Graph{Nodes: []Node{{ID: "a", Title: "A", Prerequisites: []string{"x"}}}}
	duplicate := Graph{Nodes: []Node{{ID: "a", Title: "A"}, {ID: "a", Title: "A"}}}
	for name, g := range map[string]Graph{"cycle": cycle, "unknown": unknown, "duplicate": duplicate, "empty": {}} {
		if err := g.Validate(); !errors.Is(err, ErrInvalidGraph) {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestCourseGradeThresholds(t *testing.T) {
	for _, tc := range []struct {
		percent int
		want    Grade
	}{
		{100, GradeA}, {90, GradeA}, {89, GradeB}, {70, GradeB}, {69, GradeC}, {60, GradeC}, {59, GradeD}, {0, GradeD},
	} {
		if got := CourseGrade(&tc.percent); *got != tc.want {
			t.Errorf("CourseGrade(%d) = %s, want %s", tc.percent, *got, tc.want)
		}
	}
	if CourseGrade(nil) != nil {
		t.Fatal("nothing to grade gives no grade")
	}
}

func TestResultCarriesTheReplyThatClosedATopic(t *testing.T) {
	e, _ := NewExam(pair(), 0, nil)
	top, base := "top", "base"
	r := Session{ID: "s", Exam: e, Turns: []Turn{
		{Speaker: SpeakerGeek, NodeID: &top, Text: "Вопрос?"},
		{Speaker: SpeakerStudent, NodeID: &top, Text: "Ответ"},
		{Speaker: SpeakerGeek, NodeID: &base, Text: "Разбор.\n\nВопрос?", Recap: "Разбор.\n\n"},
		{Speaker: SpeakerStudent, NodeID: &base, Text: "Ответ"},
		{Speaker: SpeakerGeek, Text: "Спасибо"},
	}}.Result()
	for _, n := range r.Nodes {
		want := ""
		if n.NodeID == top {
			want = "Разбор."
		}
		if n.Reply != want {
			t.Fatalf("%s reply = %q", n.NodeID, n.Reply)
		}
	}
}

func pair() Graph {
	return Graph{Nodes: []Node{
		{ID: "base", Title: "База"},
		{ID: "top", Title: "Верх", Prerequisites: []string{"base"}},
	}}
}

func TestGapOverAnUnknownPrerequisiteIsNotListed(t *testing.T) {
	e, first := NewExam(pair(), 0, nil)
	run(t, e, first, func(s Step) Evaluation {
		if s.NodeID == "top" {
			return unknown
		}
		return vague
	})
	if e.Nodes["base"].Status != StatusInsufficient {
		t.Fatalf("base = %s", e.Nodes["base"].Status)
	}
	if got := e.Gaps(); len(got) != 0 {
		t.Fatalf("gaps = %v: the mastery of top's prerequisite is unknown", got)
	}
	// The same holds for a prerequisite left unchecked by the question limit.
	e, first = NewExam(pair(), 1, nil)
	run(t, e, first, func(Step) Evaluation { return unknown })
	if e.Nodes["base"].Status != StatusUnchecked || len(e.Gaps()) != 0 {
		t.Fatalf("base = %s gaps = %v", e.Nodes["base"].Status, e.Gaps())
	}
}

func TestNodePreparedQuestionsBelongToTheMainQuestion(t *testing.T) {
	cases := []struct {
		name           string
		node           Node
		main, rephrase string
		prepared       bool
	}{
		{"both", Node{Questions: []string{" Почему? ", "Ещё"}, Rephrase: " Отчего? "}, "Почему?", "Отчего?", true},
		{"no rephrase", Node{Questions: []string{"Почему?"}}, "Почему?", "", false},
		// A main question generated at runtime is rephrased at runtime too.
		{"no main question", Node{Rephrase: "Отчего?"}, "", "", false},
		{"blank main question", Node{Questions: []string{"  "}, Rephrase: "Отчего?"}, "", "", false},
		{"blank rephrase", Node{Questions: []string{"Почему?"}, Rephrase: " "}, "Почему?", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.node.MainQuestion() != c.main || c.node.PreparedRephrase() != c.rephrase || c.node.Prepared() != c.prepared {
				t.Fatalf("main=%q rephrase=%q prepared=%v", c.node.MainQuestion(), c.node.PreparedRephrase(), c.node.Prepared())
			}
		})
	}
}

// A topic of a competency map has several tasks; the exam picks one when it
// starts and asks and grades only it (decision of 19.10.2026).
func TestExamPicksOneVariantOfEveryTopic(t *testing.T) {
	variants := []Variant{
		{Question: "Устно?", Rephrase: "Проще устно?", Content: "Эталон устный", AssessmentGuidance: "Балл 1"},
		{Question: "Академически?", Rephrase: "Проще академически?", Content: "Эталон академический", AssessmentGuidance: "Рубрика"},
	}
	g := Graph{Version: "v", Nodes: []Node{{ID: "a", Title: "А", Questions: []string{"Устно?"}, Content: "Эталон устный", Variants: variants}}}
	if !g.Nodes[0].Prepared() {
		t.Fatal("every variant is prepared")
	}
	unprepared := g.Nodes[0]
	unprepared.Variants = []Variant{variants[0], {Question: "Академически?"}}
	if unprepared.Prepared() {
		t.Fatal("a variant without its rephrase is not prepared")
	}
	e, _ := NewExam(g, 0, nil)
	if n, _ := e.Graph.Node("a"); n.MainQuestion() != "Устно?" || len(n.Variants) != 0 {
		t.Fatalf("without rng = %+v", n)
	}
	seen := map[string]bool{}
	for seed := range uint64(32) {
		e, _ := NewExam(g, 0, rand.New(rand.NewPCG(seed, seed)))
		n, _ := e.Graph.Node("a")
		v := variants[0]
		if n.MainQuestion() == variants[1].Question {
			v = variants[1]
		}
		if n.PreparedRephrase() != v.Rephrase || n.Content != v.Content || n.AssessmentGuidance != v.AssessmentGuidance || len(n.Variants) != 0 {
			t.Fatalf("picked node = %+v", n)
		}
		seen[n.MainQuestion()] = true
	}
	if len(seen) != 2 {
		t.Fatalf("variants asked = %v", seen)
	}
	if len(g.Nodes[0].Variants) != 2 {
		t.Fatal("the course graph keeps its variants")
	}
}

func courseGrade(e *Exam) (*Grade, *int) {
	r := Session{Exam: e}.Result()
	return r.Grade, r.ScorePercent
}

func score(e *Exam, id string) int {
	if s := e.Nodes[id].Score; s != nil {
		return *s
	}
	return -1
}

func TestTopicScaleIsTwoAtTheTopAndOneBelow(t *testing.T) {
	g := fixture(t)
	if g.MaxScore("a-star") != RootMaxScore || g.MaxScore("heuristic") != NestedMaxScore || g.MaxScore("search-space") != NestedMaxScore {
		t.Fatalf("max a-star=%d heuristic=%d search-space=%d", g.MaxScore("a-star"), g.MaxScore("heuristic"), g.MaxScore("search-space"))
	}
	// A score beyond the scale is clamped to the topic's maximum.
	e, first := NewExam(pair(), 0, nil)
	if first.NodeID != "top" {
		t.Fatalf("first = %+v", first)
	}
	e.Apply(Evaluation{Verdict: VerdictAnswered, Score: 5})
	if score(e, "top") != 2 || e.Nodes["base"].Status != StatusCredited {
		t.Fatalf("top = %+v, base = %s", e.Nodes["top"], e.Nodes["base"].Status)
	}
}

func TestStrongStudentIsAskedOnlyTopLevelTopics(t *testing.T) {
	e, first := NewExam(fixture(t), 0, nil)
	steps := run(t, e, first, func(Step) Evaluation { return strong })
	if got := asked(steps, KindQuestion); !slices.Equal(got, []string{"a-star", "simulated-annealing", "mcts", "agents-peas"}) {
		t.Fatalf("questions = %v", got)
	}
	if got := asked(steps, KindFollowup); len(got) != 0 {
		t.Fatalf("a full answer needs no followup: %v", got)
	}
	for _, id := range []string{"heuristic", "search-space", "hill-climbing", "exploration-exploitation"} {
		if e.Nodes[id].Status != StatusCredited {
			t.Fatalf("%s status = %s", id, e.Nodes[id].Status)
		}
	}
	grade, percent := courseGrade(e)
	if *grade != GradeA || *percent != 100 || len(e.Gaps()) != 0 || len(e.WeakSpots()) != 0 {
		t.Fatalf("grade=%s percent=%d gaps=%v weak=%v", *grade, *percent, e.Gaps(), e.WeakSpots())
	}
}

func TestFollowupCompletesAPartialTopLevelAnswer(t *testing.T) {
	e, first := NewExam(pair(), 0, nil)
	if next := e.Apply(partial); next.Kind != KindFollowup || next.NodeID != first.NodeID {
		t.Fatalf("next = %+v", next)
	}
	if next := e.Apply(strong); next.Kind != KindClosing {
		t.Fatalf("next = %+v", next)
	}
	if score(e, "top") != 2 || e.Nodes["top"].Status != StatusIndependent || e.Nodes["base"].Status != StatusCredited {
		t.Fatalf("top = %+v base = %s", e.Nodes["top"], e.Nodes["base"].Status)
	}
}

func TestPartialTopLevelAnswerChecksItsPrerequisites(t *testing.T) {
	e, first := NewExam(fixture(t), 0, nil)
	steps := run(t, e, first, func(s Step) Evaluation {
		if s.NodeID == "a-star" {
			return partial
		}
		return strong
	})
	if got := asked(steps, KindQuestion); !slices.Equal(got[:3], []string{"a-star", "uninformed-search", "heuristic-admissibility"}) {
		t.Fatalf("questions = %v", got)
	}
	// A prerequisite has no partial score: 1 masters it and credits its own
	// prerequisites, so search-space and heuristic are not asked.
	for id, want := range map[string]Status{"a-star": StatusIndependent, "uninformed-search": StatusIndependent, "heuristic-admissibility": StatusIndependent, "search-space": StatusCredited, "heuristic": StatusCredited} {
		if e.Nodes[id].Status != want {
			t.Fatalf("%s = %s, want %s", id, e.Nodes[id].Status, want)
		}
	}
	if score(e, "a-star") != 1 || score(e, "uninformed-search") != 1 {
		t.Fatalf("a-star = %d uninformed-search = %d", score(e, "a-star"), score(e, "uninformed-search"))
	}
	if got := e.WeakSpots(); !slices.Equal(got, []string{"a-star"}) {
		t.Fatalf("weak spots = %v", got)
	}
	if got := asked(steps, KindFollowup); !slices.Equal(got, []string{"a-star"}) {
		t.Fatalf("followups = %v", got)
	}
}

func TestAFollowupNeverLowersTheObservedScore(t *testing.T) {
	for _, followup := range []Evaluation{weak, unknown, vague} {
		e, _ := NewExam(pair(), 0, nil)
		e.Apply(partial)
		e.Apply(followup)
		if score(e, "top") != 1 || e.Nodes["top"].Status != StatusIndependent {
			t.Fatalf("followup %+v: top = %+v", followup, e.Nodes["top"])
		}
	}
}

func TestGapDescendsToTheDeepestUnmasteredTopic(t *testing.T) {
	e, first := NewExam(fixture(t), 0, nil)
	steps := run(t, e, first, func(s Step) Evaluation {
		switch s.NodeID {
		case "a-star":
			return weak
		case "heuristic-admissibility":
			return unknown
		}
		return strong
	})
	// a-star scores 0 twice (with a rephrase), then its prerequisites are
	// asked before the next top-level topic.
	if got := asked(steps, KindQuestion); !slices.Equal(got[:4], []string{"a-star", "uninformed-search", "heuristic-admissibility", "heuristic"}) {
		t.Fatalf("questions = %v", got)
	}
	if got := asked(steps, KindRephrase); !slices.Equal(got, []string{"a-star"}) {
		t.Fatalf("rephrases = %v (don't know is never rephrased)", got)
	}
	if e.Nodes["a-star"].Status != StatusGap || !e.Nodes["a-star"].Assisted || e.Nodes["heuristic-admissibility"].Status != StatusGap || !e.Nodes["heuristic-admissibility"].DontKnow {
		t.Fatalf("a-star=%+v admissibility=%+v", e.Nodes["a-star"], e.Nodes["heuristic-admissibility"])
	}
	if e.Nodes["search-space"].Status != StatusCredited {
		t.Fatalf("search-space = %s", e.Nodes["search-space"].Status)
	}
	if got := e.Gaps(); !slices.Equal(got, []string{"heuristic-admissibility"}) {
		t.Fatalf("gaps = %v", got)
	}
	// a-star's constituent is the mean of a-star 0/2, uninformed-search 1/1,
	// heuristic-admissibility 0/1 and heuristic 1/1; the other three
	// top-level topics are full: (1/2 + 3) / 4.
	grade, percent := courseGrade(e)
	if *grade != GradeB || *percent != 87 {
		t.Fatalf("grade=%s percent=%d", *grade, *percent)
	}
}

func TestHelpCapsATopLevelTopicAtOne(t *testing.T) {
	// After a rephrase: a full answer is assisted, scores 1 and checks the
	// prerequisites without a followup.
	e, _ := NewExam(pair(), 0, nil)
	if next := e.Apply(weak); next.Kind != KindRephrase {
		t.Fatalf("next = %+v", next)
	}
	if next := e.Apply(strong); next.Kind != KindQuestion || next.NodeID != "base" {
		t.Fatalf("next = %+v", next)
	}
	if s := e.Nodes["top"]; score(e, "top") != 1 || s.Status != StatusAssisted || !s.Assisted {
		t.Fatalf("top = %+v", s)
	}
	// A hint on the main question keeps the topic open and caps it the same way.
	e, first := NewExam(pair(), 0, nil)
	next := e.Apply(Evaluation{Verdict: VerdictHintRequested})
	if next.NodeID != first.NodeID || next.Kind != KindRephrase || !next.Hint || e.Nodes["top"].Score != nil || !e.Nodes["top"].HintUsed {
		t.Fatalf("hint closed topic or lost context: %+v, %+v", next, e.Nodes["top"])
	}
	data, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	var restored Exam
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	restored.Apply(strong)
	if score(&restored, "top") != 1 || restored.Nodes["top"].Status != StatusAssisted {
		t.Fatalf("restored top = %+v", restored.Nodes["top"])
	}
	// A prerequisite has nothing to cap: help leaves its 1.
	e, _ = NewExam(pair(), 0, nil)
	e.Apply(weak)
	e.Apply(weak)
	e.Apply(weak)
	e.Apply(strong)
	if score(e, "base") != 1 || e.Nodes["base"].Status != StatusAssisted {
		t.Fatalf("base = %+v", e.Nodes["base"])
	}
}

func TestHintOnFollowupKeepsTheFollowup(t *testing.T) {
	e, first := NewExam(pair(), 0, nil)
	e.Apply(partial)
	next := e.Apply(Evaluation{Verdict: VerdictHintRequested})
	if next.NodeID != first.NodeID || next.Kind != KindFollowup || !next.Hint {
		t.Fatalf("next = %+v", next)
	}
	e.Apply(weak)
	if score(e, "top") != 1 || e.Nodes["top"].Status != StatusAssisted {
		t.Fatalf("top = %+v", e.Nodes["top"])
	}
}

func TestInsufficientAfterRephraseIsExcludedFromTheScore(t *testing.T) {
	e, first := NewExam(pair(), 0, nil)
	if next := e.Apply(vague); next.Kind != KindRephrase || next.NodeID != first.NodeID {
		t.Fatalf("next = %+v", next)
	}
	e.Apply(vague)
	if s := e.Nodes["top"]; s.Status != StatusInsufficient || !s.Assisted || s.Score != nil {
		t.Fatalf("top = %+v", s)
	}
	// Insufficient data does not descend.
	if !e.Completed() {
		t.Fatalf("current = %+v", e.Current)
	}
	r := Session{Exam: e}.Result()
	if r.ScorePercent != nil || r.Nodes[1].Score != nil {
		t.Fatalf("result = %+v", r)
	}
}

func TestSingleAnswerConcludesEveryTopicAtOnce(t *testing.T) {
	newExam := func() *Exam {
		e, _ := NewExam(pair(), 0, nil)
		e.SingleAnswer = true
		return e
	}
	// 0: a gap without a rephrase.
	e := newExam()
	if next := e.Apply(weak); next.Kind != KindQuestion || next.NodeID != "base" {
		t.Fatalf("next = %+v", next)
	}
	if e.Nodes["top"].Status != StatusGap || e.Nodes["top"].Assisted {
		t.Fatalf("top = %+v", e.Nodes["top"])
	}
	// 1: concluded without a followup, the prerequisites are checked.
	e = newExam()
	if next := e.Apply(partial); next.Kind != KindQuestion || next.NodeID != "base" {
		t.Fatalf("next = %+v", next)
	}
	// 2: the prerequisites are credited.
	e = newExam()
	if next := e.Apply(strong); next.Kind != KindClosing || e.Nodes["base"].Status != StatusCredited {
		t.Fatalf("next = %+v, base = %s", next, e.Nodes["base"].Status)
	}
	// No hint and no rephrase: an unclear answer is insufficient.
	e = newExam()
	e.Apply(Evaluation{Verdict: VerdictHintRequested})
	if e.Nodes["top"].Status != StatusInsufficient || e.Nodes["top"].HintUsed {
		t.Fatalf("top = %+v", e.Nodes["top"])
	}
}

func TestMaxQuestionsEndsTheExam(t *testing.T) {
	e, first := NewExam(independent(10), 3, nil)
	steps := run(t, e, first, func(Step) Evaluation { return partial })
	// n00000: question + followup; n00001: question, and its followup does not
	// fit, so the main answer decides.
	if q := len(asked(steps, KindQuestion)) + len(asked(steps, KindFollowup)); q != 3 {
		t.Fatalf("asked %d times with a limit of 3", q)
	}
	if s := e.Nodes["n00001"]; s.Status != StatusIndependent || score(e, "n00001") != 1 {
		t.Fatalf("n00001 = %+v", s)
	}
	if e.Nodes["n00002"].Status != StatusUnchecked {
		t.Fatal("topics beyond the limit stay unchecked")
	}
}

func TestFollowupsAndRephrasesNeverExceedTheLimit(t *testing.T) {
	// Every partial answer needs a followup; with "rephrased" the main answer
	// is 0 and the rephrase is partial. Rephrases count in the limit (decision
	// of 18.10.2026). A followup or rephrase due at the last slot does not
	// fit: the answer decides instead, and a main answer of 0 is a gap.
	for _, rephrased := range []bool{false, true} {
		for _, limit := range []int{1, 3, 8} {
			e, first := NewExam(independent(10), limit, nil)
			steps := run(t, e, first, func(s Step) Evaluation {
				if rephrased && s.Kind == KindQuestion {
					return weak
				}
				return partial
			})
			if len(steps) != limit || e.Asked != limit {
				t.Fatalf("rephrased=%v limit=%d: asked %d (Asked=%d)", rephrased, limit, len(steps), e.Asked)
			}
			if rephrased && limit > 1 && len(asked(steps, KindRephrase)) == 0 {
				t.Fatalf("limit=%d: no rephrase", limit)
			}
			for _, id := range asked(steps, KindQuestion) {
				s := e.Nodes[id]
				switch {
				case rephrased && !slices.Contains(asked(steps, KindRephrase), id):
					if s.Status != StatusGap || s.Assisted {
						t.Fatalf("limit=%d: %s without room for a rephrase = %+v", limit, id, s)
					}
				case score(e, id) != 1 || (s.Status == StatusAssisted) != rephrased:
					t.Fatalf("rephrased=%v limit=%d: %s = %+v", rephrased, limit, id, s)
				}
			}
		}
	}
}

// The course grade is not stored: a completed diagnostic is graded by the
// current rule when read, here the mean of 1/2 and 2/2 is 75%.
func TestCompletedSessionIsGradedByTheCurrentRule(t *testing.T) {
	e, _ := NewExam(Graph{Nodes: []Node{{ID: "a", Title: "А"}, {ID: "b", Title: "Б"}}}, 0, nil)
	for id, points := range map[string]int{"a": 1, "b": 2} {
		points := points
		e.Nodes[id].Status, e.Nodes[id].Score = StatusIndependent, &points
	}
	completed := int64(1792400000)
	stored, err := json.Marshal(Session{ID: "s", Status: SessionCompleted, CompletedAt: &completed, Exam: e})
	if err != nil {
		t.Fatal(err)
	}
	var read Session
	if err := json.Unmarshal(stored, &read); err != nil {
		t.Fatal(err)
	}
	r := read.Result()
	if r.Grade == nil || *r.Grade != GradeB || *r.ScorePercent != 75 || r.Score != 7.5 || r.Maximum != 10 {
		t.Fatalf("result = %+v", r)
	}
	if n := r.Nodes[0]; n.Score == nil || *n.Score != 1 || n.Maximum != 2 {
		t.Fatalf("node = %+v", n)
	}
}

// A partly answered topic must not score below a failed one with the same
// prerequisites, so the points of every asked topic count (decision of 01.11.2026).
func TestCourseScoreNormalizesAConstituent(t *testing.T) {
	graph := Graph{Nodes: []Node{
		{ID: "b1", Title: "Б1"},
		{ID: "b2", Title: "Б2"},
		{ID: "main", Title: "Главная", Prerequisites: []string{"b1", "b2"}},
	}}
	grade := func(main Evaluation) (*Grade, *int, float64, int) {
		e, first := NewExam(graph, 0, nil)
		run(t, e, first, func(s Step) Evaluation {
			if s.NodeID == "main" {
				return main
			}
			return strong
		})
		r := Session{Exam: e}.Result()
		return r.Grade, r.ScorePercent, r.Score, r.Maximum
	}
	g, p, points, maximum := grade(unknown)
	if *g != GradeC || *p != 66 || points != 3.3 || maximum != 5 {
		t.Fatalf("failed main: %s %d%% %v/%d", *g, *p, points, maximum)
	}
	g, p, points, maximum = grade(partial)
	if *g != GradeB || *p != 83 || points != 4.2 || maximum != 5 {
		t.Fatalf("partial main: %s %d%% %v/%d", *g, *p, points, maximum)
	}
	// A full answer credits the prerequisites: the one asked task is the whole constituent.
	g, p, points, maximum = grade(strong)
	if *g != GradeA || *p != 100 || points != 5 || maximum != 5 {
		t.Fatalf("full main: %s %d%% %v/%d", *g, *p, points, maximum)
	}
}

func TestDontKnowAndScoredZeroWeighTheSame(t *testing.T) {
	for _, answer := range []Evaluation{unknown, weak} {
		e, first := NewExam(Graph{Nodes: []Node{{ID: "a", Title: "А"}, {ID: "b", Title: "Б"}}}, 0, nil)
		run(t, e, first, func(s Step) Evaluation {
			if s.NodeID == "a" {
				return answer
			}
			return strong
		})
		r := Session{Exam: e}.Result()
		if *r.ScorePercent != 50 || r.Nodes[0].Score == nil || *r.Nodes[0].Score != 0 {
			t.Fatalf("%+v: result = %+v", answer, r)
		}
	}
}

func TestAnUnaskedTopLevelTopicIsNoConstituent(t *testing.T) {
	e, first := NewExam(Graph{Nodes: []Node{
		{ID: "base", Title: "Основа"},
		{ID: "a", Title: "А", Prerequisites: []string{"base"}},
		{ID: "b", Title: "Б", Prerequisites: []string{"base"}},
	}}, 2, nil)
	var top string
	run(t, e, first, func(s Step) Evaluation {
		if s.NodeID != "base" {
			top = s.NodeID
			return unknown
		}
		return strong
	})
	unasked := "a"
	if top == "a" {
		unasked = "b"
	}
	if e.Nodes[unasked].Status != StatusUnchecked {
		t.Fatalf("%s status = %s", unasked, e.Nodes[unasked].Status)
	}
	r := Session{Exam: e}.Result()
	if r.Score != 2.5 || r.Maximum != 5 || *r.ScorePercent != 50 || *r.Grade != GradeD {
		t.Fatalf("result = %v/%d %d%% %s", r.Score, r.Maximum, *r.ScorePercent, *r.Grade)
	}
}

func TestACompetencyWeighsTheSameWithMoreConstituents(t *testing.T) {
	e, first := NewExam(Graph{Nodes: []Node{
		{ID: "a1", Title: "А1", Competency: "А"},
		{ID: "a2", Title: "А2", Competency: "А"},
		{ID: "a3", Title: "А3", Competency: "А"},
		{ID: "b", Title: "Б", Competency: "Б"},
	}}, 0, nil)
	run(t, e, first, func(s Step) Evaluation {
		if s.NodeID == "b" {
			return unknown
		}
		return strong
	})
	// Without competencies it would be 3 of 4, 75%.
	r := Session{Exam: e}.Result()
	if r.Score != 5 || r.Maximum != 10 || *r.ScorePercent != 50 || *r.Grade != GradeD {
		t.Fatalf("result = %v/%d %d%% %s", r.Score, r.Maximum, *r.ScorePercent, *r.Grade)
	}
}

func TestResultCarriesTheReasonsOfAScore(t *testing.T) {
	e, first := NewExam(pair(), 0, nil)
	run(t, e, first, func(Step) Evaluation { return strong })
	r := Session{Exam: e}.Result()
	for _, n := range r.Nodes {
		switch n.NodeID {
		case "top":
			if n.Score == nil || *n.Score != 2 || n.Maximum != 2 || !slices.Equal(n.Reasons, []string{"верно"}) {
				t.Fatalf("top = %+v", n)
			}
		case "base":
			if n.Score != nil || n.Maximum != 1 || n.Status != StatusCredited {
				t.Fatalf("base = %+v", n)
			}
		}
	}
}
