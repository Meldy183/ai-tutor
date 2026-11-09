package llm

import (
	"fmt"
	"hash/fnv"
	"strings"

	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/exam"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/features/geek/application"
)

var hintTransitions = []string{
	"Давай сначала проясню ситуацию, а затем попробуй сформулировать вывод.",
	"Конечно, дам немного контекста. Затем попробуй ответить своими словами.",
	"Начнём с короткого пояснения к вопросу, после него вернёмся к твоему рассуждению.",
}

var rephraseTransitions = []string{
	"По этому ответу пока не могу убедиться в понимании. Попробую спросить проще.",
	"Пока не хватает ясности в ответе. Давай я задам тот же вопрос другими словами.",
	"Мне пока трудно понять, как ты это объясняешь. Попробую переформулировать вопрос.",
	"Суть ответа пока остаётся неясной. Зайду с другой стороны и спрошу проще.",
	"Пока не получается уверенно оценить понимание. Возможно, вопрос прозвучал неясно — переформулирую его.",
	"Я пока не вижу достаточно опоры для вывода. Попробую яснее сформулировать тот же вопрос.",
}

var offTopicTransitions = []string{
	"Пока ответ не связан с вопросом или его трудно разобрать. Возможно, я спросил неясно — попробую переформулировать.",
	"Пока не удаётся связать ответ с тем, о чём я спросил. Давай сформулирую вопрос иначе.",
	"Мне пока трудно понять ответ в контексте вопроса. Попробую спросить по-другому.",
	"Сейчас не получается выделить ответ именно на этот вопрос. Возможно, стоит сформулировать его яснее — попробую ещё раз.",
	"Пока не могу разобрать, как твой ответ относится к вопросу. Давай я спрошу проще.",
	"Кажется, мы пока не попали в суть вопроса. Попробую переформулировать его понятнее.",
}

var partialFollowupTransitions = []string{
	"В ответе пока остались моменты, которые нужно прояснить. Давай уточним их, прежде чем подводить итог.",
	"Мне нужно чуть лучше понять твоё рассуждение. Уточню один момент перед итогом.",
	"Пока не всё в ответе ясно. Давай остановимся на одном моменте и разберём его точнее.",
	"Есть момент, который пока остаётся открытым. Задам уточняющий вопрос.",
	"Чтобы уверенно оценить ответ, мне нужно кое-что прояснить. Давай уточним один момент.",
	"Пока рано подводить итог. Уточню одну часть твоего ответа.",
}

// PreparedSpeech warms the exact parts requested by the conversation player.
func PreparedSpeech() []string {
	texts := application.ConversationSpeech()
	for _, group := range [][]string{hintTransitions, rephraseTransitions, offTopicTransitions, partialFollowupTransitions} {
		for _, text := range group {
			texts = append(texts, application.ReplySpeechParts(text)...)
		}
	}
	return texts
}

// Vary wording without another model call or hints. Selection is stable for
// retries, but the latest occurrence from this group is never repeated. The
// initial Geek turn may contain the previous topic's recap, so inspect it too.
func transition(r application.FeedbackRequest, variants []string) string {
	previous := -1
	for i := len(r.Dialogue) - 1; i >= 0 && previous < 0; i-- {
		if r.Dialogue[i].Speaker != exam.SpeakerGeek {
			continue
		}
		for j, text := range variants {
			if strings.Contains(r.Dialogue[i].Text, text) {
				previous = j
				break
			}
		}
	}
	h := fnv.New64a()
	fmt.Fprintf(h, "%s:%s:%s", r.Node.ID, r.Kind, r.Next)
	for _, turn := range r.Dialogue {
		fmt.Fprintf(h, "\n%d:%s:%s", turn.Line, turn.Speaker, turn.Text)
	}
	count := len(variants)
	if previous >= 0 {
		count--
	}
	index := int(h.Sum64() % uint64(count))
	if previous >= 0 && index >= previous {
		index++
	}
	return variants[index]
}
