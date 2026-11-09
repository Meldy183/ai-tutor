package application

import (
	"context"

	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/diagnostic"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/learning"
)

// outcomes includes every unmastered outcome of an open constituent in the
// same map revision, including the outcomes the diagnostic did not ask.
// Saved plans are not rebuilt; reimported maps cannot alter historical evidence.
func (s *Service) outcomes(ctx context.Context, session diagnostic.Session) ([]learning.Outcome, error) {
	// A constituent closes independently of its parent competency.
	findings := diagnostic.OpenFindings(session.Answers)
	failed := map[string]diagnostic.Answer{}
	full := map[string]bool{}
	competencies := []string{}
	seenComp := map[string]bool{}
	for _, f := range findings {
		a := f.Answer
		id := a.Task.OutcomeID
		if id == "" {
			id = a.OutcomeID
		}
		if id == "" {
			continue
		}
		failed[id] = a
		if a.Task.ConstituentID != "" {
			full[a.CompetencyID+"\x00"+a.Task.ConstituentID] = true
		}
		if !seenComp[a.CompetencyID] {
			competencies = append(competencies, a.CompetencyID)
			seenComp[a.CompetencyID] = true
		}
	}
	if len(failed) == 0 {
		return nil, nil
	}
	fromMap, err := s.repo.Outcomes(ctx, competencies)
	if err != nil {
		return nil, err
	}
	result := []learning.Outcome{}
	seen := map[string]bool{}
	for _, o := range fromMap {
		if o.MapRevision != session.Variant.MapRevision {
			continue
		}
		a, testedGap := failed[o.OutcomeID]
		if testedGap && a.CompetencyID != o.CompetencyID {
			continue
		}
		if !testedGap && (!full[o.CompetencyID+"\x00"+o.ConstituentID] || masteredOutcome(session.Answers, o.OutcomeID)) {
			continue
		}
		if !seen[o.OutcomeID] {
			result = append(result, o)
			seen[o.OutcomeID] = true
		}
	}
	// Missing map rows (including historical maps) fall back to the immutable
	// checked task, never to every outcome of its parent competency.
	for _, f := range findings {
		a := f.Answer
		id := a.Task.OutcomeID
		if id == "" {
			id = a.OutcomeID
		}
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		result = append(result, learning.Outcome{CompetencyID: a.CompetencyID,
			CompetencyName: a.Task.CompetencyName, ConstituentID: a.Task.ConstituentID,
			OutcomeID: id, OutcomeName: a.Task.OutcomeName, TaxonomyCode: a.Task.TaxonomyCode,
			Importance: int(a.Task.Importance), EducationalContent: a.Task.EducationalContent})
	}
	return result, nil
}

func masteredOutcome(answers []diagnostic.Answer, outcomeID string) bool {
	for _, a := range answers {
		id := a.Task.OutcomeID
		if id == "" {
			id = a.OutcomeID
		}
		if id == outcomeID && a.Score >= diagnostic.AnswerMaximum(a) && !a.Skipped {
			return true
		}
	}
	return false
}
