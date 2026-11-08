package application

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/diagnostic"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/shared/fault"
)

type mockReader struct {
	result diagnostic.Result
	err    error
}

func (m *mockReader) Result(ctx context.Context, ownerID, sessionID string) (diagnostic.Result, error) {
	if m.err != nil {
		return diagnostic.Result{}, m.err
	}
	return m.result, nil
}

type mockSynthesizer struct {
	summary string
	err     error
}

func (m *mockSynthesizer) Synthesize(ctx context.Context, report DeterministicReport) (string, error) {
	if m.err != nil {
		return "", m.err
	}
	return m.summary, nil
}

func (m *mockSynthesizer) SynthesizeAnswers(ctx context.Context, report SessionFeedbackReport) (string, error) {
	if m.err != nil {
		return "", m.err
	}
	return m.summary, nil
}

type mockStore struct {
	data map[string]diagnostic.OverallFeedback
}

func (m *mockStore) Get(_ context.Context, ownerID, sessionID string) (diagnostic.OverallFeedback, bool, error) {
	v, ok := m.data[ownerID+"/"+sessionID]
	return v, ok, nil
}

func (m *mockStore) Save(_ context.Context, ownerID string, feedback diagnostic.OverallFeedback) error {
	if current, exists := m.data[ownerID+"/"+feedback.SessionID]; !exists || current.AnalysisVersion < feedback.AnalysisVersion {
		m.data[ownerID+"/"+feedback.SessionID] = feedback
	}
	return nil
}

func TestAnalyzeResultDeterministicRules(t *testing.T) {
	result := diagnostic.Result{
		SessionID:               "sess-1",
		DiagnosticScore:         7,
		MaximumScore:            9,
		IncludedCompetencyCount: 3,
		SkippedCompetencies: []diagnostic.SkippedCompetency{
			{ID: "c-skip", Name: "Рекомендательные системы", Code: "INSUFFICIENT_DISTINCT_OUTCOMES"},
		},
		Answers: []diagnostic.Answer{
			// Competency 1: Main=3 (Mastered)
			{
				Role:         "main",
				CompetencyID: "c-1",
				Score:        3,
				Task: diagnostic.TaskSnapshot{
					CompetencyID:   "c-1",
					CompetencyName: "Обучение с учителем",
					OutcomeID:      "o-1",
					OutcomeName:    "Линейная регрессия",
					TaxonomyCode:   "analysis",
				},
			},
			// Competency 2: Main=1, Basic1=1, Basic2=1 (Partial)
			{
				Role:         "main",
				CompetencyID: "c-2",
				Score:        1,
				Feedback:     "Ответ частично верен. Не указана целевая переменная. Закрепите классификацию.",
				Task: diagnostic.TaskSnapshot{
					CompetencyID:   "c-2",
					CompetencyName: "Постановки задач ML",
					OutcomeID:      "o-2-main",
					OutcomeName:    "Классификация и регрессия",
					TaxonomyCode:   "analysis",
				},
			},
			{
				Role:         "basic",
				CompetencyID: "c-2",
				Score:        1,
				Task: diagnostic.TaskSnapshot{
					CompetencyID:   "c-2",
					CompetencyName: "Постановки задач ML",
					OutcomeID:      "o-2-b1",
					OutcomeName:    "Определение типа задачи",
					TaxonomyCode:   "understanding",
				},
			},
			{
				Role:         "basic",
				CompetencyID: "c-2",
				Score:        1,
				Task: diagnostic.TaskSnapshot{
					CompetencyID:   "c-2",
					CompetencyName: "Постановки задач ML",
					OutcomeID:      "o-2-b2",
					OutcomeName:    "Целевая переменная",
					TaxonomyCode:   "knowledge",
				},
			},
			// Competency 3: Main=0, Basic1=0 (Hard Gap, Importance=5), Basic2=1
			{
				Role:         "main",
				CompetencyID: "c-3",
				Score:        0,
				Task: diagnostic.TaskSnapshot{
					CompetencyID:   "c-3",
					CompetencyName: "Метрики качества",
					OutcomeID:      "o-3-main",
					OutcomeName:    "ROC-AUC и PR-AUC",
					TaxonomyCode:   "analysis",
				},
			},
			{
				Role:         "basic",
				CompetencyID: "c-3",
				Score:        0,
				Feedback:     "Неверно. Перепутаны точность и полнота. Повторите формулу Precision.",
				CriterionResults: []diagnostic.CriterionResult{
					{Key: "formula", Satisfied: false, Explanation: "Студент не помнит формулу точности"},
				},
				Task: diagnostic.TaskSnapshot{
					CompetencyID:   "c-3",
					CompetencyName: "Метрики качества",
					OutcomeID:      "o-3-b1",
					OutcomeName:    "Precision и Recall",
					TaxonomyCode:   "knowledge",
					Importance:     5,
				},
			},
			{
				Role:         "basic",
				CompetencyID: "c-3",
				Score:        1,
				Task: diagnostic.TaskSnapshot{
					CompetencyID:   "c-3",
					CompetencyName: "Метрики качества",
					OutcomeID:      "o-3-b2",
					OutcomeName:    "Accuracy",
					TaxonomyCode:   "understanding",
					Importance:     3,
				},
			},
		},
	}

	report := AnalyzeResult(result)

	if report.ScorePercentage != 77 {
		t.Fatalf("ScorePercentage = %d, want 77", report.ScorePercentage)
	}

	if len(report.Strengths) != 4 {
		t.Fatalf("Strengths count = %d, want 4 checked course_map_outcomes", len(report.Strengths))
	}

	if len(report.PartialCompetencies) != 1 {
		t.Fatalf("PartialCompetencies count = %d, want 1", len(report.PartialCompetencies))
	}
	if report.PartialCompetencies[0].CompetencyName != "Постановки задач ML" {
		t.Errorf("Partial competency = %s", report.PartialCompetencies[0].CompetencyName)
	}

	if len(report.ConfirmedGaps) != 2 {
		t.Fatalf("ConfirmedGaps count = %d, want main and failed basic", len(report.ConfirmedGaps))
	}
	gap := report.ConfirmedGaps[1]
	if gap.OutcomeName != "Precision и Recall" || gap.Importance != 5 {
		t.Errorf("Gap = %+v", gap)
	}
	if len(gap.FailedCriteria) != 1 || gap.FailedCriteria[0] != "Студент не помнит формулу точности" {
		t.Errorf("Failed criteria = %v", gap.FailedCriteria)
	}

	if len(report.TrainingRecommendations) != 3 {
		t.Fatalf("TrainingRecommendations count = %d, want 3 checked gaps", len(report.TrainingRecommendations))
	}
	rec := report.TrainingRecommendations[0]
	if rec.Priority != 1 || rec.OutcomeName != "Precision и Recall" {
		t.Errorf("Recommendation = %+v", rec)
	}

	if len(report.UnverifiedCompetencies) != 1 || report.UnverifiedCompetencies[0].Name != "Рекомендательные системы" {
		t.Errorf("Unverified = %+v", report.UnverifiedCompetencies)
	}
}

func TestAnalyzeResultTreatsUserSkippedAnswerAsZero(t *testing.T) {
	result := diagnostic.Result{MaximumScore: 3, Answers: []diagnostic.Answer{
		{CompetencyID: "c1", OutcomeID: "main", Role: "main", Score: 1, Task: diagnostic.TaskSnapshot{CompetencyName: "Тема", OutcomeID: "main"}},
		{CompetencyID: "c1", OutcomeID: "basic", Role: "basic", Skipped: true, Task: diagnostic.TaskSnapshot{CompetencyName: "Тема", OutcomeID: "basic"}},
	}}
	report := AnalyzeResult(result)
	if len(report.UnverifiedCompetencies) != 0 || len(report.PartialCompetencies) != 1 || len(report.ConfirmedGaps) != 1 || report.ConfirmedGaps[0].OutcomeID != "basic" {
		t.Fatalf("skipped zero-score basic should be a confirmed gap: %+v", report)
	}
	if strings.Contains(report.ConfirmedGaps[0].Advice, "следующим вопросом") {
		t.Fatalf("unexpected next question prompt in advice: %s", report.ConfirmedGaps[0].Advice)
	}
}

func TestFeedbackServiceCachingAndErrorHandling(t *testing.T) {
	reader := &mockReader{
		result: diagnostic.Result{
			SessionID:       "s-123",
			DiagnosticScore: 4,
			MaximumScore:    4,
		},
	}
	synthesizer := &mockSynthesizer{summary: "Отличный результат, все темы освоены на максимум!"}
	store := &mockStore{data: make(map[string]diagnostic.OverallFeedback)}

	svc := New(reader, synthesizer, store, func() time.Time { return time.Unix(1000, 0) })

	// First call: calls synthesizer and stores the feedback
	fb, err := svc.GetFeedback(context.Background(), "user-1", "s-123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fb.Summary != "Отличный результат, все темы освоены на максимум!" {
		t.Errorf("summary = %q", fb.Summary)
	}

	if _, ok := store.data["user-1/s-123"]; !ok {
		t.Fatal("feedback must be stored for its owner")
	}

	// The stored text is read back: the model is not asked again.
	synthesizer.err = errors.New("should not be called")
	storedFb, err := svc.GetFeedback(context.Background(), "user-1", "s-123")
	if err != nil {
		t.Fatalf("stored feedback lookup failed: %v", err)
	}
	if storedFb.Summary != fb.Summary {
		t.Errorf("stored summary mismatch")
	}

	// The owner is checked before the stored feedback is read.
	reader.err = fault.New(fault.NotFound, "DIAGNOSTIC_SESSION_NOT_FOUND", "")
	if _, err = svc.GetFeedback(context.Background(), "user-2", "s-123"); err == nil {
		t.Fatal("another user must not read the stored feedback")
	}
	reader.err = nil

	// Validation error for empty ID
	_, err = svc.GetFeedback(context.Background(), "user-1", "")
	var fe *fault.Error
	if !errors.As(err, &fe) || fe.Code != "VALIDATION_ERROR" {
		t.Fatalf("expected validation error, got %v", err)
	}
}

func TestAnalyzeAnswersFormatting(t *testing.T) {
	items := []AnswerFeedbackItem{
		{
			TaskID:     "snapshot-task-014",
			Topic:      "Кластеризация (типы задач ML)",
			Question:   "Магазин хочет разделить покупателей...",
			Transcript: "Регрессия, потому что...",
			Score:      0,
			MaxScore:   2,
			Feedback:   "Ответ не соответствует требуемому типу задачи. Выбран неверный вариант и не дано объяснение. Укажите «кластеризация» и объясните, что группы нужно находить без заранее известных классов.",
		},
		{
			TaskID:     "snapshot-task-016",
			Topic:      "Диагностика переобучения (Overfitting / Underfitting)",
			Question:   "Модель почти без ошибок отвечает...",
			Transcript: "Недообучение",
			Score:      0,
			MaxScore:   2,
			Feedback:   "Ответ неверный: модель переобучена, а не недообучена. Не указана связь между высоким качеством на обучении и плохим на новых данных. Укажите правильный диагноз и объясните, почему он соответствует описанному поведению.",
		},
		{
			TaskID:     "snapshot-task-001",
			Topic:      "Классификация (типы задач ML)",
			Question:   "Банк прогнозирует вернет ли кредит...",
			Transcript: "Классификация",
			Score:      1,
			MaxScore:   2,
			Feedback:   "Ответ частично верен. Не приведено объяснение критериев деления на классы. Приведите обоснование деления целевой переменной.",
		},
		{
			TaskID:     "snapshot-task-002",
			Topic:      "Регрессия (типы задач ML)",
			Question:   "Оценка стоимости квартиры...",
			Transcript: "Регрессия",
			Score:      2,
			MaxScore:   2,
			Feedback:   "Верно. Всё правильно. Продолжайте.",
		},
	}

	report := AnalyzeAnswers(items)

	if report.Score != 3 || report.MaxScore != 8 {
		t.Fatalf("Score = %d, MaxScore = %d, want 3/8", report.Score, report.MaxScore)
	}

	if len(report.Strengths) != 1 {
		t.Fatalf("Strengths count = %d, want 1", len(report.Strengths))
	}
	if report.Strengths[0] != "Тема «Регрессия (типы задач ML)» — уверенный ответ (2/2)" {
		t.Errorf("Strength = %q", report.Strengths[0])
	}

	if len(report.Partials) != 1 {
		t.Fatalf("Partials count = %d, want 1", len(report.Partials))
	}

	if len(report.Gaps) != 2 {
		t.Fatalf("Gaps count = %d, want 2", len(report.Gaps))
	}

	// Verify structured gap
	gap0 := report.Gaps[0]
	if !strings.Contains(gap0, "Тема «Кластеризация (типы задач ML)» — выявлен пробел (0/2)") {
		t.Errorf("Gap0 header missing: %s", gap0)
	}
	if !strings.Contains(gap0, "Выбран неверный вариант и не дано объяснение") {
		t.Errorf("Gap0 mistake missing: %s", gap0)
	}
	if !strings.Contains(gap0, "Что повторить по теме:") {
		t.Errorf("Gap0 repeat section missing: %s", gap0)
	}
	if !strings.Contains(gap0, "Обучение без учителя") {
		t.Errorf("Gap0 subtopics missing: %s", gap0)
	}

	gap1 := report.Gaps[1]
	if !strings.Contains(gap1, "модель переобучена, а не недообучена") {
		t.Errorf("Gap1 mistake missing: %s", gap1)
	}
	if !strings.Contains(gap1, "Анализ кривых обучения") {
		t.Errorf("Gap1 subtopics missing: %s", gap1)
	}

	if len(report.Recommendations) != 3 {
		t.Fatalf("Recommendations count = %d, want 3", len(report.Recommendations))
	}
}
