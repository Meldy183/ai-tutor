package diagnostic

import "github.com/AskarKasimov/ai-tutor/services/backend/internal/shared/stats"

type competencyShare struct {
	id    string
	share float64
}

// competencyShares normalizes the answers: every asked task of a constituent
// weighs the same, as does every constituent of a competency, however many of
// them it took (decision of 01.11.2026).
func competencyShares(answers []Answer) []competencyShare {
	order := []string{}
	constituentOrder := map[string][]string{}
	tasks := map[string]map[string][]float64{}
	for _, a := range answers {
		if a.GraderMaxScore <= 0 {
			continue
		}
		if tasks[a.CompetencyID] == nil {
			tasks[a.CompetencyID] = map[string][]float64{}
			order = append(order, a.CompetencyID)
		}
		constituent := a.Task.ConstituentID
		if _, ok := tasks[a.CompetencyID][constituent]; !ok {
			constituentOrder[a.CompetencyID] = append(constituentOrder[a.CompetencyID], constituent)
		}
		tasks[a.CompetencyID][constituent] = append(tasks[a.CompetencyID][constituent], float64(a.Score)/float64(a.GraderMaxScore))
	}
	out := make([]competencyShare, 0, len(order))
	for _, id := range order {
		shares := make([]float64, 0, len(constituentOrder[id]))
		for _, constituent := range constituentOrder[id] {
			shares = append(shares, stats.Mean(tasks[id][constituent]))
		}
		out = append(out, competencyShare{id: id, share: stats.Mean(shares)})
	}
	return out
}

// CompetencyShares are the normalized results of the competencies.
func (r Result) CompetencyShares() []float64 {
	competencies := competencyShares(r.Answers)
	shares := make([]float64, len(competencies))
	for i, c := range competencies {
		shares[i] = c.share
	}
	return shares
}

// OpenFindings are accepted non-maximal answers of an open constituent.
// Learning also reads its unasked outcomes from the full bank.
func OpenFindings(answers []Answer) []OutcomeFinding {
	out := []OutcomeFinding{}
	for _, f := range OutcomeFindings(answers) {
		if !f.Mastered {
			out = append(out, f)
		}
	}
	return out
}
