package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"

	llmgatewayv1 "github.com/AskarKasimov/ai-tutor/services/shared/gen/go/llmgateway/v1"

	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/exam"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/resultsummary"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/features/geek/application"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/shared/llmgateway"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/shared/logx"
)

// Gateway calls the LLM gateway over gRPC (llmgateway.v1.Gateway/Complete):
// no model is chosen by the client, only the complexity. Cancelling the
// context, for example when the student leaves, stops the queued or running
// request in the gateway.
type Gateway struct {
	client  llmgatewayv1.GatewayClient
	timeout time.Duration
}

func NewGateway(client llmgatewayv1.GatewayClient, timeout time.Duration) *Gateway {
	return &Gateway{client: client, timeout: timeout}
}

// Dial connects lazily to the gateway at host:port inside the trusted network.
func Dial(address string, timeout time.Duration) (*Gateway, *grpc.ClientConn, error) {
	conn, err := grpc.NewClient(address,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithUnaryInterceptor(llmgateway.ClientInterceptor),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(40<<20)),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("connect LLM gateway %s: %w", address, err)
	}
	return NewGateway(llmgatewayv1.NewGatewayClient(conn), timeout), conn, nil
}

// Summarize writes the result's overall conclusion. The student is waiting for
// it on the opened result, so the call is urgent.
func (g *Gateway) Summarize(ctx context.Context, report resultsummary.Report) (string, error) {
	data, err := json.Marshal(report)
	if err != nil {
		return "", err
	}
	var summary string
	err = g.complete(ctx, "result_summary", llmgatewayv1.Complexity_COMPLEXITY_LOW, true, string(data), func(content []byte) error {
		var out struct {
			Summary string `json:"summary"`
		}
		if err := json.Unmarshal(content, &out); err != nil {
			return err
		}
		if summary = strings.TrimSpace(out.Summary); summary == "" {
			return errors.New("empty summary")
		}
		return nil
	})
	return summary, err
}

func (g *Gateway) Question(ctx context.Context, r application.QuestionRequest) (string, error) {
	// Everything asked during a session uses the fast low pool (decision of
	// 18.10.2026); only a main question or rephrase prepared ahead may take
	// medium, and it is not urgent.
	level := llmgatewayv1.Complexity_COMPLEXITY_LOW
	if r.Ahead {
		level = llmgatewayv1.Complexity_COMPLEXITY_MEDIUM
	}
	var text string
	operation := "question:" + string(r.Kind)
	if r.Hint {
		operation = "hint_context"
	}
	err := g.complete(ctx, operation, level, !r.Ahead, questionPrompt(r), func(content []byte) error {
		var out struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(content, &out); err != nil {
			return err
		}
		if text = strings.TrimSpace(out.Text); text == "" {
			return errors.New("empty text")
		}
		if r.Hint && strings.Contains(text, "?") {
			return errors.New("context must not generate a leading question")
		}
		return nil
	})
	if err == nil && r.Hint {
		question := r.Node.MainQuestion()
		if r.Kind == exam.KindFollowup || question == "" {
			question, _ = lastSupportInput(application.EvaluationRequest{Node: r.Node, Dialogue: r.Dialogue})
		}
		if question != "" {
			text += "\n\n" + question
		}
	}
	return text, err
}

func (g *Gateway) Evaluate(ctx context.Context, r application.EvaluationRequest) (application.Assessment, error) {
	_, request := lastSupportInput(r)
	normalized := strings.ToLower(strings.Trim(strings.TrimSpace(request), ".!?… "))
	if normalized == "я не знаю" || normalized == "не знаю" {
		return application.Assessment{Evaluation: exam.Evaluation{Verdict: exam.VerdictDontKnow}}, nil
	}
	if r.ClarificationPending {
		route, err := g.route(ctx, r)
		if err != nil {
			return application.Assessment{}, err
		}
		if route.Verdict == exam.VerdictClarifyQuestion {
			if err := g.clarifySupport(ctx, r, &route); err != nil {
				return application.Assessment{}, err
			}
			return route, nil
		}
		if route.Verdict != exam.VerdictAnswered {
			return route, nil
		}
		// A real answer must reach the ordinary grader, not the support model.
		r.ClarificationPending = false
	}
	if normalized == "не понял" || normalized == "не поняла" || normalized == "я не понял" || normalized == "я не поняла" || normalized == "не понял вопрос" || normalized == "не поняла вопрос" || normalized == "я ничего не понял" || normalized == "я ничего не поняла" || normalized == "я не понимаю вопрос" {
		return application.Assessment{Evaluation: exam.Evaluation{Verdict: exam.VerdictClarifyQuestion}, Clarification: application.AskUnclearPart, ClarificationPending: true}, nil
	}
	if pureControl(request, "skip") {
		return application.Assessment{Evaluation: exam.Evaluation{Verdict: exam.VerdictDontKnow}}, nil
	}
	var result application.Assessment
	// The student waits for the evaluation, so it uses the fast low pool too.
	err := g.complete(ctx, "evaluate:"+string(r.Kind), llmgatewayv1.Complexity_COMPLEXITY_LOW, true, evaluationPrompt(r), func(content []byte) error {
		var out struct {
			Verdict           exam.Verdict           `json:"verdict"`
			Score             *int                   `json:"score"`
			Reasons           []string               `json:"reasons"`
			Evidence          []json.Number          `json:"evidence_lines"`
			AddressPreference exam.AddressPreference `json:"address_preference"`
		}
		if err := json.Unmarshal(content, &out); err != nil {
			return err
		}
		// Models sometimes quote line numbers. Accept the same integer value,
		// while rejecting fractions, non-numeric strings and overflow.
		e := exam.Evaluation{Verdict: out.Verdict}
		for _, number := range out.Evidence {
			line, err := strconv.Atoi(number.String())
			if err != nil {
				return fmt.Errorf("invalid evidence line: %w", err)
			}
			e.Evidence = append(e.Evidence, line)
		}
		switch out.Verdict {
		case exam.VerdictAnswered:
			if out.Score == nil || *out.Score < 0 || *out.Score > r.MaxScore {
				return fmt.Errorf("score out of 0..%d", r.MaxScore)
			}
			e.Score = *out.Score
			for _, reason := range out.Reasons {
				if reason = strings.TrimSpace(reason); reason != "" {
					e.Reasons = append(e.Reasons, reason)
				}
			}
		case exam.VerdictDontKnow, exam.VerdictHintRequested, exam.VerdictAddressCorrection, exam.VerdictClarifyQuestion, exam.VerdictPause:
		default:
			return fmt.Errorf("unknown verdict %q", out.Verdict)
		}
		if !out.AddressPreference.Valid() || (out.Verdict == exam.VerdictAddressCorrection && out.AddressPreference == "") {
			return errors.New("invalid address preference")
		}
		// Never show grading-model prose to a student, even if the model emits it.
		result = application.Assessment{Evaluation: e, AddressPreference: out.AddressPreference}
		return nil
	})
	if err != nil {
		return application.Assessment{}, err
	}
	if result.Verdict == exam.VerdictClarifyQuestion {
		if err := g.clarifySupport(ctx, r, &result); err != nil {
			return application.Assessment{}, err
		}
	}
	if err := g.checkIndependence(ctx, r, &result); err != nil {
		return application.Assessment{}, err
	}
	return result, nil
}

// Feedback is generated only after the engine has chosen the next step.
func (g *Gateway) Feedback(ctx context.Context, r application.FeedbackRequest) (string, error) {
	if !r.Closed && r.Verdict == exam.VerdictHintRequested {
		return transition(r, hintTransitions), nil
	}
	// A rephrase needs a transition, not another model-generated question or
	// hints. Keep this branch deterministic, including the speaker's agency.
	if !r.Closed && r.Next == exam.KindRephrase {
		if r.Verdict == exam.VerdictInsufficient {
			return transition(r, offTopicTransitions), nil
		}
		return transition(r, rephraseTransitions), nil
	}
	if !r.Closed {
		// Only the actual followup asks for the missing knowledge. Generating
		// a second reaction with the same model repeatedly leaked that answer.
		return transition(r, partialFollowupTransitions), nil
	}
	var text string
	err := g.complete(ctx, "feedback", llmgatewayv1.Complexity_COMPLEXITY_LOW, true, feedbackPrompt(r), func(content []byte) error {
		var out struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(content, &out); err != nil {
			return err
		}
		if text = strings.TrimSpace(out.Text); text == "" {
			return errors.New("empty text")
		}
		limit := 80
		if len(strings.Fields(text)) > limit {
			return fmt.Errorf("feedback exceeds %d words", limit)
		}
		return nil
	})
	return text, err
}

// complete asks for a JSON object and hands each answer to parse, which
// decodes it into a fresh value and checks it; an invalid answer is retried once.
// A call the student waits for is urgent: the gateway runs it before queued
// background work (graph building, translation) on any free compatible instance.
func (g *Gateway) complete(ctx context.Context, operation string, level llmgatewayv1.Complexity, urgent bool, prompt string, parse func([]byte) error) error {
	logger := logx.From(ctx).With(zap.String("llm_operation", operation))
	system := systemPrompt
	if operation == "name_intent" {
		system = nameIntentPrompt
	}
	if operation == "name_verify" {
		system = nameVerifyPrompt
	}
	if operation == "route" {
		system = routerSystemPrompt
	}
	if operation == "feedback" {
		system = feedbackSystemPrompt
	}
	if operation == "result_summary" {
		system = resultsummary.SystemPrompt
	}
	if operation == "support" {
		system = supportSystemPrompt
	}
	if operation == "independence" {
		system = independenceSystemPrompt
	}
	if operation == "hint_context" {
		system = contextSystemPrompt
	}
	request := &llmgatewayv1.CompleteRequest{
		Messages: []*llmgatewayv1.Message{
			{Role: llmgatewayv1.Role_ROLE_SYSTEM, Content: proto.String(system)},
			{Role: llmgatewayv1.Role_ROLE_USER, Content: proto.String(prompt)},
		},
		Complexity: level,
		Urgent:     urgent,
		Parameters: &llmgatewayv1.GenerationParameters{
			Temperature:     proto.Float64(0.2),
			ResponseFormat:  llmgatewayv1.ResponseFormat_RESPONSE_FORMAT_JSON_OBJECT.Enum(),
			ReasoningEffort: llmgatewayv1.ReasoningEffort_REASONING_EFFORT_LOW.Enum(),
		},
	}
	var last error
	for attempt := 1; attempt <= 2; attempt++ {
		attemptLogger := logger.With(zap.Int("attempt", attempt))
		content, requestID, err := g.call(logx.With(ctx, attemptLogger), request)
		if err != nil {
			attemptLogger.Warn("llm answer rejected", zap.String("gateway_request_id", requestID), zap.Error(err))
			return err
		}
		if err = parse([]byte(stripFence(content))); err == nil {
			return nil
		}
		// The raw error may repeat what the model wrote; it stays for the retry prompt.
		reason := llmgateway.SafeReason(err)
		attemptLogger.Debug("llm answer rejected", zap.Error(err))
		attemptLogger.Warn("llm answer rejected", zap.String("gateway_request_id", requestID), zap.String("reason", reason))
		last = fmt.Errorf("invalid model answer: %s", reason)
		if attempt == 1 && (strings.HasPrefix(operation, "evaluate_rubric:") || operation == "review_rubric") {
			request.Messages = append(request.Messages,
				&llmgatewayv1.Message{Role: llmgatewayv1.Role_ROLE_ASSISTANT, Content: proto.String(content)},
				&llmgatewayv1.Message{Role: llmgatewayv1.Role_ROLE_USER, Content: proto.String("Исправь предыдущий JSON: " + err.Error() + ". Верни весь результат по исходной схеме, включая answer_complete и все пункты marks для answered. Не меняй шкалу, используй реальные строки студента. Для служебного verdict marks должны быть пустыми.")},
			)
		}
		if operation == "feedback" && attempt == 1 {
			request.Messages = append(request.Messages,
				&llmgatewayv1.Message{Role: llmgatewayv1.Role_ROLE_ASSISTANT, Content: proto.String(content)},
				&llmgatewayv1.Message{Role: llmgatewayv1.Role_ROLE_USER, Content: proto.String("Исправь формат предыдущей реплики: " + err.Error() + ". Верни только JSON с text; при открытой теме — реакция без вопроса или задания, затем приложение добавит вопрос.")},
			)
		}
	}
	return last
}

func (g *Gateway) call(ctx context.Context, request *llmgatewayv1.CompleteRequest) (content, requestID string, err error) {
	ctx, cancel := context.WithTimeout(ctx, g.timeout)
	defer cancel()
	var header metadata.MD
	response, err := g.client.Complete(ctx, request, grpc.Header(&header))
	if err != nil {
		return "", llmgateway.RequestID(header, err), fmt.Errorf("call LLM gateway: %w", err)
	}
	var completion struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(response.GetCompletionJson(), &completion); err != nil || len(completion.Choices) == 0 {
		return string(response.GetCompletionJson()), response.GetRequestId(), errors.New("LLM gateway returned no choices")
	}
	choice := completion.Choices[0]
	if choice.FinishReason == "length" {
		return choice.Message.Content, response.GetRequestId(), errors.New("LLM answer was cut by the token limit")
	}
	return choice.Message.Content, response.GetRequestId(), nil
}

// stripFence removes a Markdown code fence some models put around JSON.
func stripFence(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```json")
		s = strings.TrimPrefix(s, "```")
		s = strings.TrimSuffix(strings.TrimSpace(s), "```")
	}
	return strings.TrimSpace(s)
}
