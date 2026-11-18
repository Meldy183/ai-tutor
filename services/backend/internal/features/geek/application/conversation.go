package application

import "github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/exam"

const feedbackLeadIn = "Хорошо, пару слов про твой ответ."

var feedbackLeadIns = []string{
	feedbackLeadIn,
	"Так, посмотрим, что у нас получилось.",
	"Давай пройдёмся по твоему рассуждению.",
	"Теперь коротко разберём ответ.",
}

var nextQuestionTransitions = []string{
	"Так, идём дальше.",
	"С этим вопросом закончили. Теперь следующий.",
	"Хорошо. Теперь вопрос по другой теме.",
	"Перейдём к другой теме.",
}

// NextQuestionTransition is deterministic for existing turns and is only used
// when feedback closes a topic and a new main question follows it.
func NextQuestionTransition(turn exam.Turn) string {
	if turn.Speaker != exam.SpeakerGeek || turn.Kind != exam.KindQuestion || turn.Recap == "" {
		return ""
	}
	return nextQuestionTransitions[(turn.Line/2)%len(nextQuestionTransitions)]
}

var unclearPartQuestions = []string{
	"Что именно в вопросе пояснить?",
	"На какой части вопроса остановимся?",
}

func AskUnclearPart(answerLine int) string {
	return unclearPartQuestions[((answerLine-1)/2)%len(unclearPartQuestions)]
}

func ConversationSpeech() []string {
	texts := append([]string{}, feedbackLeadIns...)
	texts = append(texts, nextQuestionTransitions...)
	return append(texts, unclearPartQuestions...)
}
