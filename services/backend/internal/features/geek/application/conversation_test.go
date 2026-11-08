package application

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/exam"
)

func TestConversationDoesNotSpendAttemptOrHintOrChangeScores(t *testing.T) {
	for _, initial := range []string{"", "частично", "слабо"} {
		f := newFixture(t)
		s, _, err := f.service.Start(context.Background(), "student", "subject", "start")
		if err != nil {
			t.Fatal(err)
		}
		if initial != "" {
			s = f.answer(t, s, Answer{Text: initial})
		}
		nodeID := s.Exam.Current.NodeID
		before, _ := json.Marshal(s.Exam)
		feedbacks, questions := len(f.examiner.feedbacks), len(f.examiner.questions)
		for _, text := range []string{"поправка обращения: женский род", "поясни формулировку", "пауза"} {
			previous := s.Turns[len(s.Turns)-1].ID
			s = f.answer(t, s, Answer{Text: text})
			after, _ := json.Marshal(s.Exam)
			if string(after) != string(before) {
				t.Fatalf("conversation changed exam: %s", text)
			}
			last := s.Turns[len(s.Turns)-1]
			if !last.Meta || !s.Turns[len(s.Turns)-2].Meta || last.ID == previous {
				t.Fatalf("invalid conversational transcript: %+v", last)
			}
			if s.AddressPreference != exam.AddressNeutral {
				t.Fatal("address preference lost on persistence")
			}
		}
		if len(f.examiner.feedbacks) != feedbacks || len(f.examiner.questions) != questions {
			t.Fatal("conversation made extra model calls")
		}
		s = f.answer(t, s, Answer{Text: "сильно"})
		want := 2
		if initial == "слабо" {
			// After a rephrase help caps the top-level topic.
			want = 1
		}
		if state := s.Exam.Nodes[nodeID]; state.Score == nil || *state.Score != want {
			t.Fatalf("answer after conversation: %+v", state)
		}
		if f.examiner.feedbacks[len(f.examiner.feedbacks)-1].AddressPreference != exam.AddressNeutral {
			t.Fatal("feedback did not receive address preference")
		}
	}
}

func TestNameInAnswerIsNotRecognized(t *testing.T) {
	f := newFixture(t)
	s, _, _ := f.service.Start(context.Background(), "student", "subject", "start")
	id := s.Exam.Current.NodeID
	s = f.answer(t, s, Answer{Text: "Меня зовут Кирилл, сильно"})
	if s.PreferredName != "" || s.Exam.Nodes[id].Score == nil || *s.Exam.Nodes[id].Score != 2 {
		t.Fatal("name was stored or answer was not graded")
	}
}

func TestMixedAnswerAndAddressCorrectionStillGradesTheAnswer(t *testing.T) {
	f := newFixture(t)
	s, _, _ := f.service.Start(context.Background(), "student", "subject", "start")
	id := s.Exam.Current.NodeID
	s = f.answer(t, s, Answer{Text: "сильно; женский род"})
	if s.AddressPreference != exam.AddressNeutral || *s.Exam.Nodes[id].Score != 2 {
		t.Fatal("mixed answer lost either knowledge or preference")
	}
}

func TestWholeAudioUsesPreparedQuestionAndFixedSpeechCache(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	s, _, _ := f.service.Start(ctx, "student", "subject", "start")
	question := s.Turns[len(s.Turns)-1]
	if _, err := f.service.speech.Part(ctx, question.Text); err != nil {
		t.Fatal(err)
	}
	calls := len(f.voice.calls)
	if _, err := f.service.TurnAudio(ctx, "student", s.ID, question.ID, AudioWhole); err != nil || len(f.voice.calls) != calls {
		t.Fatalf("prepared question cache missed: %v", err)
	}
	if !strings.Contains(strings.Join(ConversationSpeech(), "\n"), RepeatUnclearSpeech) || !strings.Contains(strings.Join(ConversationSpeech(), "\n"), AskUnclearPart) {
		t.Fatal("fixed speech not queued for warming")
	}
	if _, err := f.service.speech.Part(ctx, RepeatUnclearSpeech); err != nil {
		t.Fatal(err)
	}
	s = f.answer(t, s, Answer{Text: "пауза"})
	reply := s.Turns[len(s.Turns)-1]
	calls = len(f.voice.calls)
	if _, err := f.service.TurnAudio(ctx, "student", s.ID, reply.ID, AudioWhole); err != nil || len(f.voice.calls) != calls {
		t.Fatalf("prepared reply cache missed: %v", err)
	}
}

func TestUnrelatedConversationDoesNotSpendAttempt(t *testing.T) {
	f := newFixture(t)
	s, _, _ := f.service.Start(context.Background(), "student", "subject", "start")
	before, _ := json.Marshal(s.Exam)
	s = f.answer(t, s, Answer{Text: "ПОСТОРОННИЙ РАЗГОВОР"})
	after, _ := json.Marshal(s.Exam)
	last := s.Turns[len(s.Turns)-1]
	if string(before) != string(after) || !last.Meta || !strings.Contains(last.Text, "я не знаю") {
		t.Fatalf("unrelated utterance spent an attempt: %+v", last)
	}
}

func TestQuestionExplanationDoesNotReduceGrade(t *testing.T) {
	f := newFixture(t)
	s, _, _ := f.service.Start(context.Background(), "student", "subject", "start")
	id := s.Exam.Current.NodeID
	before, _ := json.Marshal(s.Exam)
	s = f.answer(t, s, Answer{Text: "НЕПОНЯТНО"})
	if !s.ClarificationPending {
		t.Fatal("missing clarification state")
	}
	s = f.answer(t, s, Answer{Text: "условия задачи"})
	after, _ := json.Marshal(s.Exam)
	if s.ClarificationPending || string(before) != string(after) {
		t.Fatal("explanation changed the attempt")
	}
	s = f.answer(t, s, Answer{Text: "сильно"})
	if s.Exam.Nodes[id].Score == nil || *s.Exam.Nodes[id].Score != 2 {
		t.Fatal("explanation reduced the grade")
	}
}

func TestReplySpeechPartsPreserveTextAndBoundLongClauses(t *testing.T) {
	for _, text := range []string{"", "Одна мысль.", "Первая мысль. Вторая мысль! Третья?", "Вероятность 0.5, т. е. половина. Дальше — вывод.", "Он сказал «да». Затем объяснил почему.", strings.Repeat("Здесь длинная мысль, ", 20) + "и вывод."} {
		parts := ReplySpeechParts(text)
		if strings.Join(strings.Fields(strings.Join(parts, " ")), " ") != strings.Join(strings.Fields(text), " ") {
			t.Fatalf("text changed: %q -> %q", text, parts)
		}
		for _, p := range parts {
			if len([]rune(p)) > 180 || strings.TrimSpace(p) == "" {
				t.Fatalf("invalid part %q", p)
			}
		}
	}
	if got := ReplySpeechParts("Вероятность 0.5, т. е. половина. Дальше — вывод."); len(got) != 2 {
		t.Fatalf("abbreviation/decimal split: %q", got)
	}
}

func TestChunkAudioAndIntroUseTextCacheAndValidateBounds(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	s, _, _ := f.service.Start(ctx, "student", "subject", "start")
	s = f.answer(t, s, Answer{Text: "сильно"})
	last := &s.Turns[len(s.Turns)-1]
	last.Reply = "Первое объяснение. Второе объяснение."
	last.Text = last.Reply + "\n\nСледующий вопрос?"
	last.AudioIntro = feedbackLeadIn
	data, _ := json.Marshal(s)
	f.store.sessions[s.ID] = data
	for _, part := range []struct {
		kind  AudioPart
		chunk []int
		want  string
	}{{AudioIntro, nil, feedbackLeadIn}, {AudioReply, []int{0}, "Первое объяснение."}, {AudioReply, []int{1}, "Второе объяснение."}} {
		if _, err := f.service.TurnAudio(ctx, "student", s.ID, last.ID, part.kind, part.chunk...); err != nil {
			t.Fatal(err)
		}
		if f.voice.calls[len(f.voice.calls)-1] != part.want {
			t.Fatalf("wrong speech: %q", f.voice.calls)
		}
	}
	calls := len(f.voice.calls)
	f.service.TurnAudio(ctx, "student", s.ID, last.ID, AudioReply, 0)
	if len(f.voice.calls) != calls {
		t.Fatal("chunk cache missed")
	}
	for _, c := range []int{-1, 2} {
		if _, err := f.service.TurnAudio(ctx, "student", s.ID, last.ID, AudioReply, c); err == nil {
			t.Fatal("bad chunk accepted")
		}
	}
	if _, err := f.service.TurnAudio(ctx, "student", s.ID, last.ID, AudioQuestion, 0); err == nil {
		t.Fatal("chunk accepted for question")
	}
}

func TestNextQuestionTransitionOnlyAfterClosedTopic(t *testing.T) {
	turn := exam.Turn{Speaker: exam.SpeakerGeek, Kind: exam.KindQuestion, Recap: "фидбек", Line: 3}
	first := NextQuestionTransition(turn)
	if first == "" || first != NextQuestionTransition(turn) {
		t.Fatal("transition must be stable and non-empty")
	}
	turn.Line = 5
	if NextQuestionTransition(turn) == first {
		t.Fatal("consecutive transitions repeated")
	}
	turn.Kind = exam.KindFollowup
	if NextQuestionTransition(turn) != "" {
		t.Fatal("followup must not announce a new topic")
	}
	turn.Kind = exam.KindQuestion
	turn.Recap = ""
	if NextQuestionTransition(turn) != "" {
		t.Fatal("first question must not have a transition")
	}
}

func TestTransitionAudioUsesTheSamePreparedText(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	s, _, _ := f.service.Start(ctx, "student", "subject", "start")
	s = f.answer(t, s, Answer{Text: "сильно"})
	last := &s.Turns[len(s.Turns)-1]
	last.Kind = exam.KindQuestion
	last.Recap = "Ответ.\n\n"
	want := NextQuestionTransition(*last)
	data, _ := json.Marshal(s)
	f.store.sessions[s.ID] = data
	if _, err := f.service.TurnAudio(ctx, "student", s.ID, last.ID, AudioTransition); err != nil {
		t.Fatal(err)
	}
	if got := f.voice.calls[len(f.voice.calls)-1]; got != want {
		t.Fatalf("transition mismatch: %q != %q", got, want)
	}
	if !strings.Contains(strings.Join(ConversationSpeech(), "\n"), want) {
		t.Fatal("transition is not queued for prewarming")
	}
}
