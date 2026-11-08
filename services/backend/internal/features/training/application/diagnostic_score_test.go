package application

import (
	"context"
	"slices"
	"testing"

	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/diagnostic"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/training"
)

type scoreDiagnostics struct{ session diagnostic.Session }

func (d scoreDiagnostics) Get(context.Context, string, string) (diagnostic.Session, error) {
	return d.session, nil
}

type scorePracticeRepo struct{ Repository }

func (scorePracticeRepo) Practice(context.Context, string) (int64, []training.Task, error) {
	return 1, []training.Task{{OutcomeID: "main"}}, nil
}

func TestPreviewUsesDiagnosticScoreAndStartsFreePracticeAfterPerfectMain(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		main, basic, want, maximum int
		mode                       string
	}{
		{"perfect", 2, -1, 2, 2, "free_practice"},
		{"partial with correct basic", 1, 1, 2, 3, "focused"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			answers := []diagnostic.Answer{{Role: "main", CompetencyID: "c", OutcomeID: "main", Score: tc.main, GraderMaxScore: 2, Task: diagnostic.TaskSnapshot{ID: "main", OutcomeID: "main"}}}
			if tc.basic >= 0 {
				answers = append(answers, diagnostic.Answer{Role: "basic", CompetencyID: "c", OutcomeID: "basic", Score: tc.basic, GraderMaxScore: 1})
			}
			session := diagnostic.Session{Status: diagnostic.StatusCompleted, Answers: answers, Variant: diagnostic.VariantSnapshot{IncludedCompetencyCount: 1}}
			got, err := New(scorePracticeRepo{}, scoreDiagnostics{session}, nil, nil, nil, nil, nil).Preview(context.Background(), "owner", "diagnostic")
			if err != nil || got.DiagnosticScore != tc.want || got.MaximumScore != tc.maximum || got.Mode != tc.mode || len(got.Topics) != 1 {
				t.Fatalf("preview: %+v, %v", got, err)
			}
			if got.Topics[0].OriginalMaxScore == nil || *got.Topics[0].OriginalMaxScore != 2 {
				t.Fatalf("original scale: %+v", got.Topics[0])
			}
		})
	}
}

func TestPreviewKeepsConstituentsIndependentInV2(t *testing.T) {
	answer := func(competency, constituent, role string, score, maximum int) diagnostic.Answer {
		outcome := competency + constituent + role
		return diagnostic.Answer{Role: role, CompetencyID: competency, OutcomeID: outcome, Score: score, GraderMaxScore: maximum,
			Task: diagnostic.TaskSnapshot{ID: outcome, OutcomeID: outcome, ConstituentID: constituent}}
	}
	// Four full constituents and a partial one: 95%, closed despite the partial main.
	closed := []diagnostic.Answer{answer("closed", "1", "main", 3, 3), answer("closed", "2", "main", 3, 3), answer("closed", "3", "main", 3, 3), answer("closed", "4", "main", 3, 3),
		answer("closed", "5", "main", 1, 3), answer("closed", "5", "basic", 1, 1)}
	preview := func(answers []diagnostic.Answer) training.Preview {
		t.Helper()
		session := diagnostic.Session{Status: diagnostic.StatusCompleted, Answers: answers, Variant: diagnostic.VariantSnapshot{IncludedCompetencyCount: 2, AlgorithmVersion: "constituent-map-v2"}}
		got, err := New(scorePracticeRepo{}, scoreDiagnostics{session}, nil, nil, nil, nil, nil).Preview(context.Background(), "owner", "diagnostic")
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	got := preview(append(slices.Clone(closed), answer("open", "1", "main", 1, 3)))
	if got.Mode != "focused" || len(got.Topics) != 2 || got.Topics[0].OutcomeID != "closed5main" || got.Topics[1].OutcomeID != "open1main" {
		t.Fatalf("with an open competency: %+v", got)
	}
	if got := preview(closed); got.Mode != "focused" || len(got.Topics) != 1 || got.Topics[0].OutcomeID != "closed5main" {
		t.Fatalf("all closed: %+v", got)
	}
}

type trainingBankStub struct {
	tasks    []training.Task
	revision int64
}

func (b *trainingBankStub) Tasks(_ context.Context, revision int64) ([]training.Task, error) {
	b.revision = revision
	return b.tasks, nil
}

func TestTrainingIncludesUnaskedBankOutcomesExceptMaximumAnswers(t *testing.T) {
	answer := func(part, id, role string, score, max int) diagnostic.Answer {
		return diagnostic.Answer{CompetencyID: "c", Role: role, Score: score, GraderMaxScore: max, Task: diagnostic.TaskSnapshot{CompetencyID: "c", ConstituentID: part, OutcomeID: id}}
	}
	d := diagnostic.Session{Status: diagnostic.StatusCompleted, Variant: diagnostic.VariantSnapshot{MapRevision: 7, AlgorithmVersion: "constituent-map-v2"}, Answers: []diagnostic.Answer{answer("weak", "main", "main", 1, 2), answer("weak", "failed", "basic", 0, 1), answer("weak", "perfect", "basic", 1, 1), answer("strong", "strong-main", "main", 2, 2)}}
	bank := &trainingBankStub{tasks: []training.Task{{CompetencyID: "c", ConstituentID: "weak", OutcomeID: "unasked"}, {CompetencyID: "c", ConstituentID: "weak", OutcomeID: "perfect"}, {CompetencyID: "c", ConstituentID: "strong", OutcomeID: "strong-unasked"}}}
	got, err := New(scorePracticeRepo{}, scoreDiagnostics{d}, nil, nil, nil, nil, nil).WithBank(bank).Preview(context.Background(), "owner", "diagnostic")
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, topic := range got.Topics {
		ids = append(ids, topic.OutcomeID)
	}
	if bank.revision != 7 || len(ids) != 3 || !slices.Contains(ids, "main") || !slices.Contains(ids, "failed") || !slices.Contains(ids, "unasked") {
		t.Fatalf("revision=%d targets=%v", bank.revision, ids)
	}
}
