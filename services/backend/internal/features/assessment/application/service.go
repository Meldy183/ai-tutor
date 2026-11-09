package application

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/assessment"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/variant"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/shared/fault"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/shared/logx"
	"go.uber.org/zap"
)

type Evaluation = assessment.Evaluation

type CriterionResult = assessment.CriterionResult

type Transcriptions interface {
	TextByOwner(context.Context, string, string) (string, error)
}

type Grader interface {
	Grade(context.Context, GradingContext, string) (Evaluation, error)
}

type Service struct {
	transcriptions Transcriptions
	variantTasks   variant.TaskReader
	grader         Grader
}

func New(transcriptions Transcriptions, variantTasks variant.TaskReader, grader Grader) *Service {
	return &Service{
		transcriptions: transcriptions,
		variantTasks:   variantTasks,
		grader:         grader,
	}
}

func validText(value string, max int) bool {
	return strings.TrimSpace(value) != "" && utf8.ValidString(value) && !strings.ContainsRune(value, 0) && utf8.RuneCountInString(value) <= max
}

func gradingMaxScore(gradingContext GradingContext) (int, error) {
	switch {
	case gradingContext.Role == "main" && gradingContext.MaxScore == 2:
		return 2, nil
	case gradingContext.Role == "training" && gradingContext.MaxScore == 2:
		return 2, nil
	case gradingContext.Role == "basic" && gradingContext.MaxScore == 1:
		return 1, nil
	default:
		return 0, fault.New(
			fault.Invalid,
			"INVALID_GRADING_CONTEXT",
			"Контекст оценивания содержит некорректную роль или шкалу.",
		)
	}
}

func expectedVerdict(score, maxScore int) string {
	if maxScore == 1 {
		switch score {
		case 1:
			return "correct"
		case 0:
			return "incorrect"
		default:
			return ""
		}
	}
	switch score {
	case maxScore:
		return "correct"
	case 1:
		return "partial"
	case 0:
		return "incorrect"
	default:
		return ""
	}
}

func validateEvaluation(result *Evaluation, gradingContext GradingContext) error {
	maxScore, err := gradingMaxScore(gradingContext)
	if err != nil {
		return err
	}
	if result.Score < 0 || result.Score > maxScore || result.Verdict != expectedVerdict(result.Score, maxScore) {
		return fault.New(fault.Upstream, "INVALID_MODEL_RESPONSE", "Модель вернула некорректную оценку.")
	}
	if len(result.CriterionResults) != len(gradingContext.Criteria) || len(gradingContext.Criteria) == 0 {
		return fault.New(fault.Upstream, "INVALID_MODEL_RESPONSE", "Модель вернула неполные результаты по критериям.")
	}

	contentSatisfied := 0
	contentCount := 0
	mandatoryFailed := false
	for i, criterion := range gradingContext.Criteria {
		item := result.CriterionResults[i]
		if item.Key != criterion.Key || !validText(item.Explanation, 400) || strings.ContainsAny(item.Explanation, "\r\n") {
			return fault.New(fault.Upstream, "INVALID_MODEL_RESPONSE", "Модель вернула некорректные результаты по критериям.")
		}
		// Instructions are a gate, not evidence of subject knowledge.
		if criterion.Key != "instruction_following" {
			contentCount++
			if item.Satisfied {
				contentSatisfied++
			}
		}
		if criterion.Mandatory && !item.Satisfied {
			mandatoryFailed = true
		}
		result.CriterionResults[i].Explanation = strings.TrimSpace(item.Explanation)
	}

	if contentCount == 0 {
		return fault.New(fault.Invalid, "INVALID_GRADING_CONTEXT", "Контекст не содержит предметных критериев.")
	}
	expectedScore := 0
	if !mandatoryFailed {
		if maxScore == 1 {
			if contentSatisfied == contentCount {
				expectedScore = 1
			}
		} else if contentSatisfied == contentCount {
			expectedScore = maxScore
		} else {
			// A boolean criterion only distinguishes full completion. The grader
			// distinguishes partial subject knowledge from a wholly wrong answer
			// using the trusted reference and rubric, not the instruction count.
			if result.Score == 0 || result.Score == 1 {
				expectedScore = result.Score
			}
		}
	}
	if result.Score != expectedScore {
		return fault.New(fault.Upstream, "INVALID_MODEL_RESPONSE", "Оценка модели не согласуется с результатами по критериям.")
	}

	// Formatting from the model must not discard an otherwise valid grade.
	feedback := strings.Join(strings.Fields(result.Feedback), " ")
	if feedback == "" || !utf8.ValidString(feedback) || strings.ContainsRune(feedback, 0) {
		return fault.New(fault.Upstream, "INVALID_MODEL_RESPONSE", "Модель вернула некорректный фидбэк.")
	}
	result.Feedback = feedback
	return nil
}

func (s *Service) gradeAndValidate(ctx context.Context, gradingContext GradingContext, answer string) (Evaluation, error) {
	maxScore, err := gradingMaxScore(gradingContext)
	if err != nil {
		return Evaluation{}, err
	}
	if !validText(answer, 20000) {
		return Evaluation{}, fault.New(fault.Invalid, "INVALID_ANSWER", "Ответ пустой, слишком длинный или повреждён.")
	}
	// Retry malformed model output once, including JSON rejected by the adapter.
	// Every attempt is validated; technical failures never become a student score.
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return Evaluation{}, err
		}
		result, err := s.grader.Grade(ctx, gradingContext, answer)
		if err == nil {
			err = validateEvaluation(&result, gradingContext)
		}
		if err == nil {
			result.MaxScore = maxScore
			return result, nil
		}
		var modelErr *fault.Error
		if attempt == 1 || !errors.As(err, &modelErr) || modelErr.Code != "INVALID_MODEL_RESPONSE" {
			return Evaluation{}, err
		}
		logx.From(ctx).Warn("model evaluation rejected; retrying", zap.String("error_code", modelErr.Code),
			zap.String("error_message", modelErr.Message), zap.Error(modelErr.Cause))
	}
}

func (s *Service) evaluateWithContext(ctx context.Context, ownerID, transcriptionID string, gradingContext GradingContext) (Evaluation, error) {
	if !validText(transcriptionID, 128) {
		return Evaluation{}, fault.Validation("transcription_id", "Укажите сохранённую расшифровку.")
	}
	answer, err := s.transcriptions.TextByOwner(ctx, ownerID, transcriptionID)
	if err != nil {
		return Evaluation{}, err
	}
	if !validText(answer, 20000) {
		return Evaluation{}, fault.New(fault.Invalid, "INVALID_TRANSCRIPTION", "Расшифровка слишком длинная или повреждена.")
	}
	return s.gradeAndValidate(ctx, gradingContext, answer)
}

func (s *Service) EvaluateVariant(ctx context.Context, ownerID, transcriptionID, variantID, variantTaskID string) (Evaluation, error) {
	if !validText(variantID, 128) {
		return Evaluation{}, fault.Validation("variant_id", "Укажите идентификатор варианта.")
	}
	if !validText(variantTaskID, 128) {
		return Evaluation{}, fault.Validation("variant_task_id", "Укажите идентификатор позиции задания варианта.")
	}
	if s.variantTasks == nil {
		return Evaluation{}, fault.New(fault.Unavailable, "VARIANT_GRADING_UNAVAILABLE", "Оценивание заданий варианта временно недоступно.")
	}

	item, err := s.variantTasks.TaskForGrading(ctx, ownerID, variantID, variantTaskID)
	if err != nil {
		return Evaluation{}, err
	}
	gradingContext, err := GradingContextFromVariantTask(item)
	if err != nil {
		return Evaluation{}, err
	}
	return s.evaluateWithContext(ctx, ownerID, transcriptionID, gradingContext)
}

// EvaluateTrusted evaluates a server-owned snapshot; HTTP clients cannot supply it.
func (s *Service) EvaluateTrusted(ctx context.Context, ownerID, transcriptionID string, item variant.VariantTask) (Evaluation, error) {
	gradingContext, err := GradingContextFromVariantTask(item)
	if err != nil {
		return Evaluation{}, err
	}
	return s.evaluateWithContext(ctx, ownerID, transcriptionID, gradingContext)
}

// EvaluateTrustedText grades a final answer against a server-owned exercise snapshot.
// Unlike EvaluateTrusted, it never looks up browser-supplied transcription data.
func (s *Service) EvaluateTrustedText(ctx context.Context, item variant.VariantTask, answer string) (Evaluation, error) {
	gradingContext, err := GradingContextFromVariantTask(item)
	if err != nil {
		return Evaluation{}, err
	}
	return s.gradeAndValidate(ctx, gradingContext, answer)
}
