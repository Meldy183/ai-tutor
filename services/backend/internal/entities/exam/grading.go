package exam

import (
	"math"

	"github.com/AskarKasimov/ai-tutor/services/backend/internal/shared/stats"
)

// Grade is the letter of the course result; topics are scored in points.
type Grade string

const (
	GradeA Grade = "A"
	GradeB Grade = "B"
	GradeC Grade = "C"
	GradeD Grade = "D"
)

// CompetencyPoints is what a competency weighs in the overall score, in the exam
// and the learning diagnostic alike, however many constituents and tasks it took
// (decision of 01.11.2026).
const CompetencyPoints = 5

// OverallScore is the score of all competencies by their shares, rounded to one
// decimal, its maximum and the percent. Without competencies there is no percent.
func OverallScore(competencies []float64) (score float64, maximum int, percent *int) {
	if len(competencies) == 0 {
		return 0, 0, nil
	}
	share := stats.Mean(competencies)
	maximum = CompetencyPoints * len(competencies)
	p := stats.Percent(share)
	return math.Round(share*float64(maximum)*10) / 10, maximum, &p
}

// CourseGrade translates the percent of the overall score into the overall
// grade, the same way in the exam and the learning diagnostic: at least 90% is
// A, 70% B, 60% C, otherwise D (decision of 01.11.2026). With nothing to grade
// there is no grade.
func CourseGrade(percent *int) *Grade {
	if percent == nil {
		return nil
	}
	grade := GradeD
	switch {
	case *percent >= 90:
		grade = GradeA
	case *percent >= 70:
		grade = GradeB
	case *percent >= 60:
		grade = GradeC
	}
	return &grade
}

// The topic scale (decision of 08.11.2026): a top-level topic scores 0, 1 or
// 2, any prerequisite at any depth 0 or 1. Help caps a top-level topic at 1.
const (
	RootMaxScore    = 2
	NestedMaxScore  = 1
	AssistedRootCap = 1
)

// MaxScore is the maximum of a topic: RootMaxScore at the top level,
// NestedMaxScore below it.
func (g Graph) MaxScore(id string) int { return maxScore(g.required(), id) }

// maxScore shares one index of required topics, so an operation over many
// topics does not rebuild it for each of them.
func maxScore(required map[string]bool, id string) int {
	if required[id] {
		return NestedMaxScore
	}
	return RootMaxScore
}

// capScore keeps a score within the topic's scale and applies the cap of help.
func capScore(score, maximum int, assisted bool) int {
	score = max(0, min(score, maximum))
	if assisted && maximum == RootMaxScore {
		score = min(score, AssistedRootCap)
	}
	return score
}
