package diagnostic

import "github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/exam"

func ConstituentKey(competencyID, constituentID string) string {
	return competencyID + "\x00" + constituentID
}

func AnswerMaximum(a Answer) int {
	if a.GraderMaxScore > 0 {
		return a.GraderMaxScore
	}
	if a.Role == "basic" {
		return exam.NestedMaxScore
	}
	return exam.RootMaxScore
}

// OpenConstituents keeps evidence of different constituents independent.
func OpenConstituents(answers []Answer) map[string]bool {
	groups := map[string][]exam.TaskScore{}
	for _, a := range answers {
		key := ConstituentKey(a.CompetencyID, a.Task.ConstituentID)
		groups[key] = append(groups[key], exam.TaskScore{Score: a.Score, Maximum: AnswerMaximum(a), Main: a.Role == "main"})
	}
	open := map[string]bool{}
	for key, scores := range groups {
		open[key] = !exam.ConstituentMastered(scores)
	}
	return open
}
