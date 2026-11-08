package application

import (
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/diagnostic"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/learning"
	"reflect"
	"testing"
)

func TestPlanIncludesEveryUnmasteredOutcomeOfOpenConstituents(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		scores                   []int
		mixed, skipped, reimport bool
		want                     []string
	}{
		{"bases mastered", []int{0, 1, 1}, false, false, false, []string{"main", "extra"}},
		{"partial main and bases mastered", []int{1, 1, 1}, false, false, false, []string{"main", "extra"}},
		{"one failed base", []int{1, 0, 1}, false, false, false, []string{"main", "b1", "extra"}},
		{"whole constituent", []int{0, 0, 0}, false, false, false, []string{"main", "b1", "b2", "extra"}},
		{"partial main not whole", []int{1, 0, 0}, false, false, false, []string{"main", "b1", "b2", "extra"}},
		{"mixed constituents", []int{0, 0, 0}, true, false, false, []string{"main", "b1", "b2", "extra", "unrelated"}},
		{"skipped not proven whole", []int{0, 0, 0}, false, true, false, []string{"main", "b1", "b2", "extra"}},
		{"reimport uses snapshot", []int{0, 0, 0}, false, false, true, []string{"main", "b1", "b2"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, repo, _, _ := newService(t, 1)
			session := diagnostic.Session{Variant: diagnostic.VariantSnapshot{MapRevision: 7}}
			for i, id := range []string{"main", "b1", "b2"} {
				role := "basic"
				if i == 0 {
					role = "main"
				}
				part := "part"
				if tc.mixed && i == 2 {
					part = "other"
				}
				session.Answers = append(session.Answers, diagnostic.Answer{CompetencyID: "c1", Role: role, Score: tc.scores[i], GraderMaxScore: map[string]int{"main": 2, "basic": 1}[role], Skipped: tc.skipped && i == 2,
					Task: diagnostic.TaskSnapshot{OutcomeID: id, OutcomeName: "snapshot " + id, ConstituentID: part, ConstituentName: part}})
			}
			repo.course_map_outcomes = nil
			revision := int64(7)
			if tc.reimport {
				revision = 8
			}
			for _, id := range []string{"main", "b1", "b2", "extra", "unrelated"} {
				part := "part"
				if id == "unrelated" || tc.mixed && id == "b2" {
					part = "other"
				}
				repo.course_map_outcomes = append(repo.course_map_outcomes, learning.Outcome{CompetencyID: "c1", ConstituentID: part, MapRevision: revision, OutcomeID: id, OutcomeName: "map " + id})
			}
			outcomes, err := s.outcomes(t.Context(), session)
			if err != nil {
				t.Fatal(err)
			}
			got := []string{}
			for _, o := range outcomes {
				got = append(got, o.OutcomeID)
				if tc.reimport && o.OutcomeName != "snapshot "+o.OutcomeID {
					t.Fatal("historical snapshot replaced by current map")
				}
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestPlanKeepsAnOpenConstituentInAStrongCompetency(t *testing.T) {
	s, repo, _, _ := newService(t, 1)
	repo.course_map_outcomes = nil
	answer := func(competency, constituent, role string, score, maximum int) diagnostic.Answer {
		return diagnostic.Answer{CompetencyID: competency, Role: role, Score: score, GraderMaxScore: maximum,
			Task: diagnostic.TaskSnapshot{OutcomeID: competency + constituent + role, ConstituentID: constituent}}
	}
	session := diagnostic.Session{Answers: []diagnostic.Answer{
		answer("closed", "1", "main", 2, 2), answer("closed", "2", "main", 2, 2), answer("closed", "3", "main", 2, 2), answer("closed", "4", "main", 2, 2),
		answer("closed", "5", "main", 0, 2), answer("closed", "5", "basic", 1, 1), answer("closed", "5", "basic", 1, 1),
		answer("open", "1", "main", 0, 2), answer("open", "1", "basic", 0, 1),
	}}
	got, err := s.outcomes(t.Context(), session)
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, o := range got {
		ids = append(ids, o.OutcomeID)
	}
	if !reflect.DeepEqual(ids, []string{"closed5main", "open1main", "open1basic"}) {
		t.Fatalf("outcomes = %v", ids)
	}
}

func TestPlanIncludesBothUnaskedOutcomesOfAFiveOutcomeConstituent(t *testing.T) {
	service, repo, _, _ := newService(t, 1)
	repo.course_map_outcomes = nil
	for _, id := range []string{"main", "failed-basic", "full-basic", "unasked-1", "unasked-2"} {
		repo.course_map_outcomes = append(repo.course_map_outcomes, learning.Outcome{CompetencyID: "c1", ConstituentID: "part", MapRevision: 7, OutcomeID: id})
	}
	session := diagnostic.Session{Variant: diagnostic.VariantSnapshot{MapRevision: 7}}
	for i, id := range []string{"main", "failed-basic", "full-basic"} {
		role, maximum := "basic", 1
		if i == 0 {
			role, maximum = "main", 2
		}
		session.Answers = append(session.Answers, diagnostic.Answer{CompetencyID: "c1", Role: role, Score: []int{1, 0, 1}[i], GraderMaxScore: maximum, Task: diagnostic.TaskSnapshot{OutcomeID: id, ConstituentID: "part"}})
	}
	outcomes, err := service.outcomes(t.Context(), session)
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, outcome := range outcomes {
		ids = append(ids, outcome.OutcomeID)
	}
	if !reflect.DeepEqual(ids, []string{"main", "failed-basic", "unasked-1", "unasked-2"}) {
		t.Fatalf("learning outcomes=%v", ids)
	}
}
