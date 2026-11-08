package modelapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/AskarKasimov/ai-tutor/services/backend/internal/features/assessment/application"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/shared/fault"
)

func testContext() application.GradingContext {
	return application.GradingContext{
		TaskID:           "variant-task-1",
		Role:             "main",
		MaxScore:         2,
		Question:         "Вопрос",
		Options:          []string{"Классификация"},
		VoiceInstruction: "Назовите тип",
		ReferenceAnswer:  "Классификация: два класса.",
		Outcome:          application.OutcomeContext{Title: "Определяет тип задачи", Taxonomy: "Понимание", Level: "базовый"},
		Criteria: []application.Criterion{
			{Key: "task_type", Description: "Правильно назван тип задачи.", Mandatory: true},
			{Key: "justification", Description: "Выбор объяснён через два класса."},
		},
		MaterialContext: application.MaterialContext{Knowledge: "Классификация выбирает класс из конечного набора.", Skills: "Определять тип целевой переменной."},
	}
}

func TestGradeSendsTrustedScaleCriteriaMaterialsAndParsesStructuredResult(t *testing.T) {
	var request struct {
		Model           string `json:"model"`
		Temperature     *int   `json:"temperature"`
		ReasoningEffort string `json:"reasoning_effort"`
		MaxTokens       int    `json:"max_tokens"`
		Messages        []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"score\":2,\"verdict\":\"correct\",\"criterion_results\":[{\"key\":\"task_type\",\"satisfied\":true,\"explanation\":\"Названа классификация.\"},{\"key\":\"justification\",\"satisfied\":true,\"explanation\":\"Указаны два класса.\"}],\"feedback\":\"Верно. Оба критерия выполнены. Закрепите различия типов задач.\"}"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	result, err := New(server.Client(), server.URL+"/v1", "gpt-oss-120b", time.Second).Grade(context.Background(), testContext(), "классификация, потому что два класса")
	if err != nil || result.Score != 2 || result.Verdict != "correct" || len(result.CriterionResults) != 2 || result.Feedback == "" {
		t.Fatalf("unexpected model result: %#v, %v", result, err)
	}
	if request.Model != "gpt-oss-120b" || request.Temperature == nil || *request.Temperature != 0 || request.ReasoningEffort != "medium" || request.MaxTokens != 4096 {
		t.Fatalf("unexpected model configuration: %+v", request)
	}
	content := request.Messages[1].Content
	for _, required := range []string{`"role":"main"`, `"max_score":2`, `"student_answer":"классификация, потому что два класса"`, `"criteria":[`, `"mandatory":true`, `"material_context":`, `"reference_answer":"Классификация: два класса."`} {
		if !strings.Contains(content, required) {
			t.Fatalf("model request omitted %s: %s", required, content)
		}
	}
	if !strings.Contains(request.Messages[0].Content, "mandatory=true") ||
		!strings.Contains(request.Messages[0].Content, "criterion_results — массив") ||
		!strings.Contains(request.Messages[0].Content, "feedback — одна строка") ||
		!strings.Contains(request.Messages[0].Content, "связный мини-отзыв") ||
		!strings.Contains(request.Messages[0].Content, "180 символов") {
		t.Fatalf("system prompt omitted grading or concise feedback rule: %s", request.Messages[0].Content)
	}
}

func TestGradeRejectsInvalidTrustedScale(t *testing.T) {
	gradingContext := testContext()
	gradingContext.Role = "basic"
	gradingContext.MaxScore = 2

	_, err := New(http.DefaultClient, "http://127.0.0.1:1/v1", "gpt-oss-120b", time.Second).
		Grade(context.Background(), gradingContext, "ответ")

	var f *fault.Error
	if !errors.As(err, &f) || f.Code != "INVALID_GRADING_CONTEXT" {
		t.Fatalf("expected INVALID_GRADING_CONTEXT, got %v", err)
	}
}

func TestGradeRejectsMalformedStructuredResult(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
	}{
		{"missing verdict", `{"score":2,"feedback":""}`},
		{"missing satisfied", `{"score":2,"verdict":"correct","criterion_results":[{"key":"task_type","explanation":"Верно"}],"feedback":"Верно"}`},
		{"null satisfied", `{"score":2,"verdict":"correct","criterion_results":[{"key":"task_type","satisfied":null,"explanation":"Верно"}],"feedback":"Верно"}`},
		{"missing explanation", `{"score":2,"verdict":"correct","criterion_results":[{"key":"task_type","satisfied":true}],"feedback":"Верно"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
					"message": map[string]string{"content": tc.content}, "finish_reason": "stop",
				}}})
			}))
			defer server.Close()
			_, err := New(server.Client(), server.URL+"/v1", "gpt-oss-120b", time.Second).Grade(context.Background(), testContext(), "ответ")
			var f *fault.Error
			if !errors.As(err, &f) || f.Code != "INVALID_MODEL_RESPONSE" {
				t.Fatalf("expected INVALID_MODEL_RESPONSE, got %v", err)
			}
		})
	}
}

func TestGradeRejectsTruncatedModelResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":null},"finish_reason":"length"}]}`))
	}))
	defer server.Close()
	_, err := New(server.Client(), server.URL+"/v1", "gpt-oss-120b", time.Second).Grade(context.Background(), testContext(), "ответ")
	var f *fault.Error
	if !errors.As(err, &f) || f.Code != "INVALID_MODEL_RESPONSE" {
		t.Fatalf("expected INVALID_MODEL_RESPONSE, got %v", err)
	}
}

func TestGradeMapsUnavailableStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	_, err := New(server.Client(), server.URL+"/v1", "gpt-oss-120b", time.Second).Grade(context.Background(), testContext(), "ответ")
	var f *fault.Error
	if !errors.As(err, &f) || f.Code != "MODEL_UNAVAILABLE" {
		t.Fatalf("expected MODEL_UNAVAILABLE, got %v", err)
	}
}

func TestGradeMapsGatewayTimeoutStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusGatewayTimeout)
	}))
	defer server.Close()

	_, err := New(server.Client(), server.URL+"/v1", "gpt-oss-120b", time.Second).Grade(context.Background(), testContext(), "ответ")
	var f *fault.Error
	if !errors.As(err, &f) || f.Code != "MODEL_TIMEOUT" {
		t.Fatalf("expected MODEL_TIMEOUT, got %v", err)
	}
}

func TestGradeMapsRequestDeadline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(250 * time.Millisecond):
		}
	}))
	defer server.Close()
	_, err := New(server.Client(), server.URL, "gpt-oss-120b", 20*time.Millisecond).Grade(context.Background(), testContext(), "ответ")
	var f *fault.Error
	if !errors.As(err, &f) || f.Code != "MODEL_TIMEOUT" {
		t.Fatalf("expected MODEL_TIMEOUT, got %v", err)
	}
}

func TestGradeRejectsMissingSatisfied(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"score\":0,\"verdict\":\"incorrect\",\"criterion_results\":[{\"key\":\"task_type\",\"explanation\":\"Тип не назван.\"},{\"key\":\"justification\",\"satisfied\":false,\"explanation\":\"Объяснения нет.\"}],\"feedback\":\"Ответ неверен. Не выполнены критерии. Назовите тип и объясните выбор.\"}"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	_, err := New(server.Client(), server.URL+"/v1", "gpt-oss-120b", time.Second).Grade(context.Background(), testContext(), "ответ")
	var f *fault.Error
	if !errors.As(err, &f) || f.Code != "INVALID_MODEL_RESPONSE" {
		t.Fatalf("expected INVALID_MODEL_RESPONSE, got %v", err)
	}
}

func TestGradeRejectsNullSatisfied(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"score\":0,\"verdict\":\"incorrect\",\"criterion_results\":[{\"key\":\"task_type\",\"satisfied\":null,\"explanation\":\"Тип не назван.\"},{\"key\":\"justification\",\"satisfied\":false,\"explanation\":\"Объяснения нет.\"}],\"feedback\":\"Ответ неверен. Не выполнены критерии. Назовите тип и объясните выбор.\"}"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	_, err := New(server.Client(), server.URL+"/v1", "gpt-oss-120b", time.Second).Grade(context.Background(), testContext(), "ответ")
	var f *fault.Error
	if !errors.As(err, &f) || f.Code != "INVALID_MODEL_RESPONSE" {
		t.Fatalf("expected INVALID_MODEL_RESPONSE, got %v", err)
	}
}
