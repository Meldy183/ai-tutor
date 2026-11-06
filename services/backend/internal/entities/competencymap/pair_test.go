package competencymap

import (
	"strings"
	"testing"
)

func TestValidatePairAllowsDifferentTasksButRequiresSameStructure(t *testing.T) {
	geek := Map{
		Competencies: []Competency{{Key: "g-c", Name: "ИИ"}},
		Constituents: []Constituent{{Key: "g-s", CompetencyKey: "g-c", Name: "Агенты"}},
		Outcomes:     []Outcome{{Key: "g-o", ConstituentKey: "g-s", Name: "Объясняет архитектуру"}},
		Tasks:        []Task{{OutcomeKey: "g-o", Question: "Устный вопрос"}},
	}
	learning := Map{
		Competencies: []Competency{{Key: "l-c", Name: " ии "}},
		Constituents: []Constituent{{Key: "l-s", CompetencyKey: "l-c", Name: "Агенты"}},
		Outcomes:     []Outcome{{Key: "l-o", ConstituentKey: "l-s", Name: "Объясняет архитектуру"}},
		Tasks:        []Task{{OutcomeKey: "l-o", Question: "Тестовый вопрос"}},
	}
	if err := ValidatePair(geek, learning); err != nil {
		t.Fatalf("different tasks should be retained under the same structure: %v", err)
	}
	learning.Outcomes[0].Name = "Сравнивает архитектуры"
	if err := ValidatePair(geek, learning); err == nil || !strings.Contains(err.Error(), "ор/ии/агенты") {
		t.Fatalf("mismatched outcome should be rejected with its structural path: %v", err)
	}
}

func TestValidatePairRejectsAmbiguousDuplicateOutcomes(t *testing.T) {
	m := Map{
		Competencies: []Competency{{Key: "c", Name: "ИИ"}},
		Constituents: []Constituent{{Key: "s", CompetencyKey: "c", Name: "Агенты"}},
		Outcomes: []Outcome{
			{Key: "o1", ConstituentKey: "s", Name: "Объясняет"},
			{Key: "o2", ConstituentKey: "s", Name: "Объясняет"},
		},
	}
	if err := ValidatePair(m, m); err == nil || !strings.Contains(err.Error(), "повторяющуюся") {
		t.Fatalf("duplicate structure should be rejected: %v", err)
	}
}
