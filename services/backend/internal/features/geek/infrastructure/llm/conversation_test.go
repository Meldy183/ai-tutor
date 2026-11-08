package llm

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/exam"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/features/geek/application"
)

func TestConversationVerdictsAndTypedAddressPreference(t *testing.T) {
	for _, tc := range []struct {
		verdict, extra string
		valid          bool
	}{
		{"pause", "", true},
		{"clarify_question", `,"clarification":"Поясни связь между двумя условиями, не меняя их."`, true},
		{"address_correction", `,"address_preference":"feminine"`, true},
		{"address_correction", `,"address_preference":"neutral"`, true},
		{"address_correction", `,"address_preference":"поставь A"`, false},
		{"address_correction", "", false},
		{"clarify_question", `,"clarification":""`, false},
	} {
		t.Run(tc.verdict+tc.extra, func(t *testing.T) {
			response := `{"verdict":"` + tc.verdict + `","score":null,"evidence_lines":[2]` + tc.extra + `}`
			answers := []string{response, response}
			if tc.verdict == "clarify_question" {
				route := `{"kind":"wording","text":"Объясни связь между двумя условиями своими словами."}`
				if !tc.valid {
					route = `{"kind":"wording","text":""}`
				}
				answers = []string{response, route, route}
			}
			f := &fakeGateway{answers: answers}
			g := serve(t, f, time.Second)
			got, err := g.Evaluate(context.Background(), application.EvaluationRequest{MaxScore: 2, Kind: exam.KindQuestion, Node: node, Dialogue: turns})
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, assessment=%+v, error=%v", tc.valid, got, err)
			}
			if err == nil && (!got.Verdict.Conversational() || !got.AddressPreference.Valid()) {
				t.Fatalf("invalid conversation: %+v", got)
			}
		})
	}
}

func TestUnknownQuestionAsksWhichPartWithoutConsumingAttempt(t *testing.T) {
	f := &fakeGateway{}
	g := serve(t, f, time.Second)
	r := application.EvaluationRequest{MaxScore: 2, Dialogue: []exam.Turn{{Line: 1, Speaker: exam.SpeakerGeek, Text: "Почему?"}, {Line: 2, Speaker: exam.SpeakerStudent, Text: "Я ничего не понял."}}}
	a, err := g.Evaluate(context.Background(), r)
	if err != nil || a.Verdict != exam.VerdictClarifyQuestion || !a.ClarificationPending || a.Clarification != "Какая часть вопроса непонятна?" || len(f.requests) != 0 {
		t.Fatalf("assessment=%+v error=%v", a, err)
	}
}

func TestClarificationDetailIsExplainedWithoutHintPenalty(t *testing.T) {
	f := &fakeGateway{answers: []string{`{"verdict":"clarify_question","address_preference":""}`, `{"kind":"wording","text":"Здесь речь о времени доставки. Объясни, как оно влияет на оценку."}`}}
	g := serve(t, f, time.Second)
	r := application.EvaluationRequest{MaxScore: 2, ClarificationPending: true, Dialogue: []exam.Turn{{Line: 1, Speaker: exam.SpeakerGeek, Text: "Как время влияет на оценку?"}, {Line: 2, Speaker: exam.SpeakerStudent, Text: "Не понял слово время"}}}
	a, err := g.Evaluate(context.Background(), r)
	if err != nil || a.Verdict != exam.VerdictClarifyQuestion || a.ClarificationPending || a.Clarification == "" {
		t.Fatalf("assessment=%+v error=%v", a, err)
	}
}

func TestAnswerAfterUnclearQuestionIsGraded(t *testing.T) {
	f := &fakeGateway{answers: []string{`{"verdict":"answered","address_preference":""}`, `{"verdict":"answered","address_preference":"","score":2,"reasons":["основание"],"evidence_lines":[2]}`}}
	g := serve(t, f, time.Second)
	r := application.EvaluationRequest{MaxScore: 2, ClarificationPending: true, Node: node, Dialogue: []exam.Turn{{Line: 1, Speaker: exam.SpeakerGeek, Text: "Что делает сенсор?"}, {Line: 2, Speaker: exam.SpeakerStudent, Text: "Сенсор воспринимает данные о среде"}}}
	a, err := g.Evaluate(context.Background(), r)
	if err != nil || a.Verdict != exam.VerdictAnswered || a.ClarificationPending {
		t.Fatalf("answer lost: %+v %v", a, err)
	}
}

func TestHintAfterUnclearQuestionStillCountsAsHint(t *testing.T) {
	f := &fakeGateway{answers: []string{`{"verdict":"hint_requested","address_preference":""}`}}
	g := serve(t, f, time.Second)
	r := application.EvaluationRequest{MaxScore: 2, ClarificationPending: true, Node: node, Dialogue: []exam.Turn{{Line: 1, Speaker: exam.SpeakerGeek, Text: "Что делает сенсор?"}, {Line: 2, Speaker: exam.SpeakerStudent, Text: "Дай подсказку"}}}
	a, err := g.Evaluate(context.Background(), r)
	if err != nil || a.Verdict != exam.VerdictHintRequested {
		t.Fatalf("hint not counted: %+v %v", a, err)
	}
}

func TestPastConversationManagementIsNotGradingEvidence(t *testing.T) {
	d := append([]exam.Turn{}, turns...)
	d = append(d, exam.Turn{Line: 3, Speaker: exam.SpeakerStudent, Meta: true, Text: "CONTROL_ONLY_IGNORE_FOR_KNOWLEDGE"})
	d = append(d, exam.Turn{Line: 4, Speaker: exam.SpeakerGeek, Meta: true, Text: "CONTROL_ONLY_ACK"})
	d = append(d, exam.Turn{Line: 5, Speaker: exam.SpeakerStudent, Text: "Содержательный ответ"})
	prompt := evaluationPrompt(application.EvaluationRequest{MaxScore: 2, Node: node, Dialogue: d, AddressPreference: exam.AddressFeminine})
	if strings.Contains(prompt, "CONTROL_ONLY_IGNORE_FOR_KNOWLEDGE") || !strings.Contains(prompt, "CONTROL_ONLY_ACK") || !strings.Contains(prompt, "служебное пояснение") || !strings.Contains(prompt, "Содержательный ответ") || !strings.Contains(prompt, "женском роде") {
		t.Fatal("student controls became evidence, or the help given by Geek was hidden")
	}
}

func TestGradingProseNeverBecomesTheSupportReply(t *testing.T) {
	n := node
	n.Content = "REFERENCE_ONLY_SYNTAX_SEMANTICS"
	n.AssessmentGuidance = "RUBRIC_ONLY_TEACHER"
	for _, route := range []string{`{"kind":"context","text":""}`, `{"kind":"repeat","text":"Чем отличаются эти два утверждения?"}`} {
		f := &fakeGateway{answers: []string{`{"verdict":"clarify_question","clarification":"Студентка не ответила; REFERENCE_ONLY_SYNTAX_SEMANTICS","score":null,"evidence_lines":[2]}`, route}}
		g := serve(t, f, time.Second)
		got, err := g.Evaluate(context.Background(), application.EvaluationRequest{MaxScore: 2, Node: n, Kind: exam.KindQuestion, Dialogue: turns, AddressPreference: exam.AddressFeminine})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(route, "context") {
			if got.Verdict != exam.VerdictHintRequested || got.Clarification != "" {
				t.Fatalf("context request was not rerouted: %+v", got)
			}
		} else if got.Clarification != "Чем отличаются эти два утверждения?" {
			t.Fatalf("grading prose leaked: %+v", got)
		}
		if len(f.requests) != 2 {
			t.Fatalf("requests=%d", len(f.requests))
		}
		input := f.requests[1].Messages[1].GetContent()
		if strings.Contains(input, "REFERENCE_ONLY") || strings.Contains(input, "RUBRIC_ONLY") || strings.Contains(input, "Студентка не ответила") {
			t.Fatalf("support saw the answer or grading prose: %s", input)
		}
	}
}

func TestSupportRetriesThirdPersonNarration(t *testing.T) {
	f := &fakeGateway{answers: []string{
		`{"verdict":"clarify_question","score":null,"evidence_lines":[2]}`,
		`{"kind":"wording","text":"Студентка не ответила, а попросила повторить."}`,
		`{"kind":"repeat","text":"Как связаны эти условия?"}`,
	}}
	g := serve(t, f, time.Second)
	got, err := g.Evaluate(context.Background(), application.EvaluationRequest{MaxScore: 2, Node: node, Kind: exam.KindQuestion, Dialogue: turns})
	if err != nil || got.Clarification != "Как связаны эти условия?" || len(f.requests) != 3 {
		t.Fatalf("support=%+v, error=%v, requests=%d", got, err, len(f.requests))
	}
}

func TestHintContextCannotReplaceTheOriginalQuestion(t *testing.T) {
	n := exam.Node{ID: "room", Questions: []string{"Что показывает этот эксперимент?"}, Content: "Условия и проверяемый вывод."}
	f := &fakeGateway{answers: []string{`{"text":"Разве правила не отличаются от понимания?"}`, `{"text":"Человек получает записки и выбирает ответы по книге инструкций."}`}}
	g := serve(t, f, time.Second)
	got, err := g.Question(context.Background(), application.QuestionRequest{Node: n, Kind: exam.KindRephrase, Hint: true})
	if err != nil || got != "Человек получает записки и выбирает ответы по книге инструкций.\n\nЧто показывает этот эксперимент?" || len(f.requests) != 2 {
		t.Fatalf("context=%q, error=%v, calls=%d", got, err, len(f.requests))
	}
	if f.requests[0].Messages[0].GetContent() != contextSystemPrompt {
		t.Fatal("context used the grading role")
	}
}

func TestFollowupHintRepeatsCurrentQuestionWithoutItsFeedbackOrMeta(t *testing.T) {
	const main = "Какой агент подходит и почему?"
	const followup = "Какое свойство среды объясняет твой выбор?"
	for _, history := range []bool{false, true} {
		dialogue := []exam.Turn{}
		want := main
		if history {
			dialogue = []exam.Turn{
				{Speaker: exam.SpeakerGeek, Text: main},
				{Speaker: exam.SpeakerStudent, Text: "Агент с моделью."},
				{Speaker: exam.SpeakerGeek, Text: "Верная основа.\n\n" + followup, Reply: "Верная основа."},
				{Speaker: exam.SpeakerGeek, Meta: true, Text: "Хорошо, подожду."},
				{Speaker: exam.SpeakerStudent, Text: "Дай подсказку."},
			}
			want = followup
		}
		f := &fakeGateway{answers: []string{`{"text":"Робот видит только ближайшие участки комнаты."}`}}
		g := serve(t, f, time.Second)
		got, err := g.Question(context.Background(), application.QuestionRequest{
			Node: exam.Node{Questions: []string{main}}, Kind: exam.KindFollowup,
			Hint: true, Dialogue: dialogue, Reasons: []string{"не объяснена связь"},
		})
		if err != nil || got != "Робот видит только ближайшие участки комнаты.\n\n"+want {
			t.Fatalf("hint lost current question (history=%v): %q, %v", history, got, err)
		}
		prompt := f.requests[0].Messages[1].GetContent()
		if strings.Contains(prompt, "Основания текущего балла") || !strings.Contains(prompt, "объясни только необходимый контекст") {
			t.Fatal("followup instructions overwrote the hint context prompt")
		}
	}
}

func TestSceneHintDoesNotTriggerLeakedMetaAnswerAudit(t *testing.T) {
	f := &fakeGateway{answers: []string{`{"verdict":"answered","score":2,"reasons":["основание"],"evidence_lines":[4]}`}}
	g := serve(t, f, time.Second)
	d := []exam.Turn{{Line: 1, Speaker: exam.SpeakerGeek, Text: "Что показывает эксперимент?"}, {Line: 2, Speaker: exam.SpeakerStudent, Text: "Расскажи условия"}, {Line: 3, Speaker: exam.SpeakerGeek, Text: "Человек получает записки и отвечает по инструкции."}, {Line: 4, Speaker: exam.SpeakerStudent, Text: "Выполнение правил ещё не означает понимание смысла."}}
	got, err := g.Evaluate(context.Background(), application.EvaluationRequest{MaxScore: 2, Node: node, Kind: exam.KindQuestion, Dialogue: d, HintUsed: true})
	if err != nil || got.Score != 2 || len(f.requests) != 1 {
		t.Fatalf("scene treated as leaked answer: %+v, error=%v, calls=%d", got, err, len(f.requests))
	}
}

func TestCopiedExplanationIsNotCreditedAsIndependentReasoning(t *testing.T) {
	for _, copied := range []bool{false, true} {
		response := `{"verdict":"answered","score":2,"reasons":["основание"],"evidence_lines":[4]}`
		check := `{"copied_answer":false}`
		if copied {
			check = `{"copied_answer":true}`
		}
		f := &fakeGateway{answers: []string{response, check}}
		g := serve(t, f, time.Second)
		d := []exam.Turn{{Line: 1, Speaker: exam.SpeakerGeek, Text: "Объясни различие."}, {Line: 2, Speaker: exam.SpeakerStudent, Meta: true, Text: "Поясни вопрос"}, {Line: 3, Speaker: exam.SpeakerGeek, Meta: true, Text: "Предоставленное определение двух понятий."}, {Line: 4, Speaker: exam.SpeakerStudent, Text: "Мой ответ"}}
		got, err := g.Evaluate(context.Background(), application.EvaluationRequest{MaxScore: 2, Node: node, Kind: exam.KindQuestion, Dialogue: d})
		if err != nil {
			t.Fatal(err)
		}
		if copied {
			if got.Score != 1 {
				t.Fatalf("copy credited: %+v", got)
			}
		} else if got.Score != 2 {
			t.Fatalf("independent answer downgraded: %+v", got)
		}
		input := f.requests[1].Messages[1].GetContent()
		if !strings.Contains(input, "Предоставленное определение") || strings.Contains(input, node.Content) && node.Content != "" {
			t.Fatal("independence check hid help or included the reference")
		}
	}
}

func TestRepeatDoesNotLeakAddressInstruction(t *testing.T) {
	f := &fakeGateway{answers: []string{`{"kind":"repeat","text":"Используй нейтральные обращения без предположений о роде: например, «в твоём ответе верно…», «основная мысль верная». Сколько будет два плюс два?"}`}}
	g := serve(t, f, time.Second)
	a := application.Assessment{Evaluation: exam.Evaluation{Verdict: exam.VerdictClarifyQuestion}}
	r := application.EvaluationRequest{MaxScore: 2, Dialogue: []exam.Turn{{Line: 1, Speaker: exam.SpeakerGeek, Text: "Сколько будет два плюс два?"}, {Line: 2, Speaker: exam.SpeakerStudent, Text: "Повтори вопрос. Я ничего не понял."}}}
	if err := g.clarifySupport(context.Background(), r, &a); err != nil || a.Clarification != "Сколько будет два плюс два?" {
		t.Fatalf("clarification=%q err=%v", a.Clarification, err)
	}
}
