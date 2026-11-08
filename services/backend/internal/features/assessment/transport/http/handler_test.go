package assessmenthttp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/user"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/variant"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/features/assessment/application"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/shared/httpx"
)

type transcriptionStub struct{ owner string }

func (s *transcriptionStub) TextByOwner(_ context.Context, owner, _ string) (string, error) {
	s.owner = owner
	return "Классификация, потому что два класса", nil
}

type variantTaskStub struct {
	ownerID, variantID, variantTaskID string
}

func ptr(value string) *string { return &value }

func (s *variantTaskStub) TaskForGrading(_ context.Context, ownerID, variantID, variantTaskID string) (variant.VariantTask, error) {
	s.ownerID, s.variantID, s.variantTaskID = ownerID, variantID, variantTaskID
	return variant.VariantTask{
		ID:   variantTaskID,
		Role: "main",
		Task: variant.TaskProfile{
			ID:               "task-1",
			Question:         "Вопрос",
			VoiceInstruction: ptr("Назовите тип задачи и объясните выбор"),
			ReferenceAnswer:  ptr("Классификация"),
			Constituent:      variant.ConstituentProfile{Name: "Типы задач"},
			Outcome:          variant.OutcomeProfile{Name: "Определяет тип задачи"},
		},
	}, nil
}

type graderStub struct{}

func TestEvaluateRejectsLegacyTaskIDAndFrontendRole(t *testing.T) {
	for _, body := range []string{
		`{"transcription_id":"tr-1","task_id":"old-task"}`,
		`{"transcription_id":"tr-1","variant_id":"variant-1","variant_task_id":"variant-task-1","role":"basic"}`,
	} {
		transcriptions := &transcriptionStub{}
		learning_tasks := &variantTaskStub{}
		handler := New(application.New(transcriptions, learning_tasks, graderStub{}))
		request := httptest.NewRequest(http.MethodPost, "/assessments/evaluate", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request = httpx.WithPrincipal(request, user.User{ID: "student-1", Role: user.Student})
		response := httptest.NewRecorder()
		handler.Evaluate(response, request)
		if response.Code != http.StatusUnprocessableEntity || learning_tasks.ownerID != "" || transcriptions.owner != "" {
			t.Fatalf("unexpected rejected request: %d %s", response.Code, response.Body.String())
		}
	}
}

func (graderStub) Grade(_ context.Context, _ application.GradingContext, _ string) (application.Evaluation, error) {
	return application.Evaluation{
		Score:   2,
		Verdict: "correct",
		CriterionResults: []application.CriterionResult{
			{Key: "answer_correctness", Satisfied: true, Explanation: "Ответ соответствует эталону."},
			{Key: "instruction_following", Satisfied: true, Explanation: "Инструкция выполнена."},
		},
		Feedback: "Верно. Оба критерия выполнены. Закрепите тему.",
	}, nil
}

func TestEvaluateRequiresAuthAndReturnsVariantGrade(t *testing.T) {
	transcriptions := &transcriptionStub{}
	learning_tasks := &variantTaskStub{}
	handler := New(application.New(transcriptions, learning_tasks, graderStub{}))
	requestBody := `{"transcription_id":"tr-1","variant_id":"variant-1","variant_task_id":"variant-task-1"}`

	unauthorized := httptest.NewRecorder()
	handler.Evaluate(unauthorized, httptest.NewRequest(http.MethodPost, "/assessments/evaluate", strings.NewReader(requestBody)))
	if unauthorized.Code != http.StatusUnauthorized || transcriptions.owner != "" {
		t.Fatalf("unauthorized request: %d, owner=%q", unauthorized.Code, transcriptions.owner)
	}

	request := httptest.NewRequest(http.MethodPost, "/assessments/evaluate", strings.NewReader(requestBody))
	request.Header.Set("Content-Type", "application/json")
	request = httpx.WithPrincipal(request, user.User{ID: "student-1", Role: user.Student})
	response := httptest.NewRecorder()
	handler.Evaluate(response, request)

	var result EvaluateResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || transcriptions.owner != "student-1" || result.Score != 2 || result.MaxScore != 2 || result.Verdict != "correct" {
		t.Fatalf("response=%d %s, owner=%q", response.Code, response.Body.String(), transcriptions.owner)
	}
	if learning_tasks.ownerID != "student-1" || learning_tasks.variantID != "variant-1" || learning_tasks.variantTaskID != "variant-task-1" {
		t.Fatalf("unexpected snapshot lookup: owner=%q variant=%q task=%q", learning_tasks.ownerID, learning_tasks.variantID, learning_tasks.variantTaskID)
	}
}
