package llm

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/coursegraph"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/exam"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/features/geek/application"
	geekgraph "github.com/AskarKasimov/ai-tutor/services/backend/internal/features/geek/infrastructure/graph"
)

func TestImportedMapCriteriaAndSelectedReferenceReachGradingAndFeedback(t *testing.T) {
	header := []string{"Субкомпетенция", "Образовательный результат", "Уровень по Блуму", "Важность", "Что должно войти в тест", "Задание 1", "Критерии оценивания 1", "Задание 2", "Критерии оценивания 2", "Составляющая"}
	rows := [][]string{header,
		{"ИИ", "Называет область", "Помнить", "3", "TRUE", "Назови область ИИ", "Эталон: машинное обучение.", "", "", "1.1 Применение ИИ"},
		{"", "Различает подходы", "Понимать", "3", "TRUE", "Сравни подходы", "Эталон: два разных подхода."},
		{"", "Обосновывает применение", "Применять", "5", "TRUE", "Приведи пример робота", "Эталон: ЧУЖОЙ_ВАРИАНТ.\nБалл (макс. 1): 1 — верно.",
			"Назови задачу обработки языка и объясни полезность ИИ", "Эталон: классификация спама; разнообразие писем.\nАналитическая рубрика (макс. 2):\nК1 «Задача»: 1 — назван подходящий пример; 0 — нет примера.\nК2 «Причина»: 1 — объяснена полезность; 0 — нет объяснения."},
	}
	built, err := coursegraph.BuildMapGraph([]coursegraph.MapSheet{{Name: "Карта", Rows: rows}})
	if err != nil {
		t.Fatal(err)
	}
	graph, ok, err := geekgraph.Decode(built.Graph)
	if err != nil || !ok {
		t.Fatalf("decode: %v, %v", ok, err)
	}
	main := graph.TopLevel()[0]
	for i, n := range graph.Nodes {
		if n.ID == main {
			if len(n.Variants) != 2 {
				t.Fatalf("variants=%d", len(n.Variants))
			}
			graph.Nodes[i] = n.WithVariant(n.Variants[1])
		}
	}
	e, step := exam.NewExam(graph, 0, nil)
	n, _ := e.Graph.Node(step.NodeID)
	n.Outcomes = []string{"UNRELATED_OUTCOME"}
	request := application.EvaluationRequest{MaxScore: e.Graph.MaxScore(n.ID), Kind: step.Kind, Node: n, Dialogue: []exam.Turn{
		{Line: 1, Speaker: exam.SpeakerGeek, Text: n.MainQuestion()},
		{Line: 2, Speaker: exam.SpeakerStudent, Text: "Классификация спама: письма разнообразны, учиться на примерах полезнее жёстких правил."},
	}}
	f := &fakeGateway{answers: []string{
		`{"verdict":"answered","address_preference":"","score":2,"reasons":["Пример спама","Разнообразие писем"],"evidence_lines":[2]}`,
		`{"text":"Ты связал фильтрацию спама с разнообразием писем и обучением на примерах."}`,
	}}
	g := serve(t, f, time.Second)
	a, err := g.Evaluate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	next := e.Apply(a.Evaluation)
	_, err = g.Feedback(context.Background(), application.FeedbackRequest{EvaluationRequest: request, Verdict: a.Verdict, Closed: next.NodeID != n.ID, Next: next.Kind, Result: *e.Nodes[n.ID]})
	if err != nil {
		t.Fatal(err)
	}
	if *e.Nodes[n.ID].Score != 2 || !slices.Equal(e.Nodes[n.ID].Reasons, []string{"Пример спама", "Разнообразие писем"}) {
		t.Fatal("fully demonstrated imported task not mastered")
	}
	for _, r := range f.requests {
		prompt := r.Messages[1].GetContent()
		for _, want := range []string{"классификация спама", "«Задача»", "«Причина»", "Критерии преподавателя"} {
			if !strings.Contains(prompt, want) {
				t.Fatalf("missing %q in %s", want, prompt)
			}
		}
		if strings.Contains(prompt, "ЧУЖОЙ_ВАРИАНТ") || strings.Contains(prompt, "UNRELATED_OUTCOME") {
			t.Fatal("wrong variant or unrelated outcome leaked into prompts")
		}
	}
}
