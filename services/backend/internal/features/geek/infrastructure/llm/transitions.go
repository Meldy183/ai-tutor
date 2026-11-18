package llm

import (
	"fmt"
	"hash/fnv"
	"strings"

	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/exam"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/features/geek/application"
)

var hintTransitions = []string{
	"Давай сначала разберёмся с условием.",
	"Я поясню, что здесь происходит. Потом вернёмся к вопросу.",
	"Сначала напомню исходную ситуацию. Потом попробуй ещё раз.",
}

var rephraseTransitions = []string{
	"Так, спрошу по-другому.",
	"Давай зайдём с другой стороны.",
	"Попробую точнее сформулировать вопрос.",
}

var partialFollowupTransitions = []string{
	"Так, здесь уточню один момент.",
	"Давай задержимся на этом шаге.",
}

// PreparedSpeech warms the exact parts requested by the conversation player.
func PreparedSpeech() []string {
	texts := application.ConversationSpeech()
	for _, group := range [][]string{hintTransitions, rephraseTransitions, partialFollowupTransitions} {
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
