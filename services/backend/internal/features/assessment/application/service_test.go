package application

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/variant"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/shared/fault"
)

type transcriptionStub struct {
	owner, id string
	text      string
}

func (s *transcriptionStub) TextByOwner(_ context.Context, owner, id string) (string, error) {
	s.owner, s.id = owner, id
	return s.text, nil
}

type variantTaskStub struct {
	ownerID, variantID, variantTaskID string
	result                            variant.VariantTask
	err                               error
}

func (s *variantTaskStub) TaskForGrading(_ context.Context, ownerID, variantID, variantTaskID string) (variant.VariantTask, error) {
	s.ownerID, s.variantID, s.variantTaskID = ownerID, variantID, variantTaskID
	return s.result, s.err
}

type graderStub struct {
	gradingContext GradingContext
	answer         string
	result         Evaluation
}

func (s *graderStub) Grade(_ context.Context, gradingContext GradingContext, answer string) (Evaluation, error) {
	s.gradingContext, s.answer = gradingContext, answer
	return s.result, nil
}

func text(value string) *string { return &value }

func validVariantTask(role string) variant.VariantTask {
	return variant.VariantTask{
		ID:   "variant-task-1",
		Role: role,
		Task: variant.TaskProfile{
			ID:               "task-1",
			Question:         "Банк прогнозирует возврат кредита в срок.",
			VoiceInstruction: text("Назовите тип задачи и объясните выбор"),
			ReferenceAnswer:  text("Классификация: целевая переменная имеет два класса."),
			Constituent:      variant.ConstituentProfile{Name: "Типы задач машинного обучения"},
			Outcome:          variant.OutcomeProfile{Name: "Определяет тип задачи"},
		},
	}
}

func validEvaluation() Evaluation {
	return Evaluation{
		Score:   2,
		Verdict: "correct",
		CriterionResults: []CriterionResult{
			{Key: "answer_correctness", Satisfied: true, Explanation: "Ответ соответствует эталону."},
			{Key: "instruction_following", Satisfied: true, Explanation: "Тип назван и выбор объяснён."},
		},
		Feedback: "Верно. Оба критерия выполнены. Закрепите тему.",
	}
}
func TestTrustedTrainingSnapshotUsesTwoPointScale(t *testing.T) {
	for _, score := range []int{0, 1, 2} {
		t.Run(string(rune('0'+score)), func(t *testing.T) {
			result := validEvaluation()
			if score == 0 {
				result.Score = 0
				result.Verdict = "incorrect"
				result.CriterionResults[0].Satisfied = false
				result.CriterionResults[1].Satisfied = false
			}
			if score == 1 {
				result.Score = 1
				result.Verdict = "partial"
				result.CriterionResults[0].Satisfied = false
				result.CriterionResults[0].Explanation = "Тип назван верно, но объяснение не связывает его с целевой переменной."
			}
			grader := &graderStub{result: result}
			service := New(&transcriptionStub{text: "Ответ"}, nil, grader)
			got, err := service.EvaluateTrusted(context.Background(), "owner", "transcription", validVariantTask("training"))
			if err != nil || got.Score != score || got.MaxScore != 2 || grader.gradingContext.Role != "training" {
				t.Fatalf("score %d: %+v %v", score, got, err)
			}
		})
	}
}

func TestEvaluateVariantUsesOwnedHistoricalSnapshot(t *testing.T) {
	transcriptions := &transcriptionStub{text: "классификация, потому что два класса"}
	course_map_tasks := &variantTaskStub{result: validVariantTask("main")}
	grader := &graderStub{result: validMainEvaluation()}

	got, err := New(transcriptions, course_map_tasks, grader).EvaluateVariant(
		context.Background(), "student-1", "tr-1", "variant-1", "variant-task-1",
	)
	if err != nil || got.Score != 2 || got.MaxScore != 2 || got.Verdict != "correct" {
		t.Fatalf("unexpected evaluation: %#v, %v", got, err)
	}
	if course_map_tasks.ownerID != "student-1" || course_map_tasks.variantID != "variant-1" || course_map_tasks.variantTaskID != "variant-task-1" {
		t.Fatalf("unexpected snapshot lookup: owner=%q variant=%q task=%q", course_map_tasks.ownerID, course_map_tasks.variantID, course_map_tasks.variantTaskID)
	}
	if transcriptions.owner != "student-1" || transcriptions.id != "tr-1" || grader.answer != transcriptions.text {
		t.Fatalf("unexpected transcription lookup: owner=%q id=%q answer=%q", transcriptions.owner, transcriptions.id, grader.answer)
	}
}

func TestEvaluateVariantBasicUsesOnePointScale(t *testing.T) {
	result := validMainEvaluation()
	result.Score = 1
	result.Verdict = "correct"

	got, err := New(
		&transcriptionStub{text: "классификация, потому что два класса"},
		&variantTaskStub{result: validVariantTask("basic")},
		&graderStub{result: result},
	).EvaluateVariant(context.Background(), "student-1", "tr-1", "variant-1", "variant-task-1")

	if err != nil || got.Score != 1 || got.MaxScore != 1 || got.Verdict != "correct" {
		t.Fatalf("expected basic 1/1 correct, got %#v, %v", got, err)
	}
}

func TestSubjectKnowledgeIsNotReplacedByInstructionFollowing(t *testing.T) {
	for _, tc := range []struct {
		name, role        string
		score             int
		full, instruction bool
		accepted          bool
	}{
		{"wrong content with instruction", "main", 0, false, true, true},
		{"partial content", "main", 1, false, true, true},
		{"incomplete cannot receive full score", "main", 2, false, true, false},
		{"complete content", "main", 2, true, true, true},
		{"main never scores three", "main", 3, true, true, false},
		{"complete cannot receive partial score", "main", 1, true, true, false},
		{"instruction remains mandatory", "main", 1, true, false, false},
		{"wrong basic with instruction", "basic", 0, false, true, true},
		{"incomplete basic cannot earn a point", "basic", 1, false, true, false},
		{"complete basic", "basic", 1, true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gc, err := GradingContextFromVariantTask(validVariantTask(tc.role))
			if err != nil {
				t.Fatal(err)
			}
			result := validEvaluation()
			result.Score = tc.score
			result.Verdict = expectedVerdict(tc.score, gc.MaxScore)
			result.CriterionResults[0].Satisfied = tc.full
			result.CriterionResults[1].Satisfied = tc.instruction
			if err := validateEvaluation(&result, gc); (err == nil) != tc.accepted {
				t.Fatalf("accepted=%v, error=%v", tc.accepted, err)
			}
		})
	}
}

func TestGradingUsesRubricFromSavedMap(t *testing.T) {
	item := validVariantTask("main")
	item.Task.Criteria = text("Назван тип задачи и указано, что целевая переменная дискретная.")
	gc, err := GradingContextFromVariantTask(item)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gc.Criteria[0].Description, *item.Task.Criteria) || gc.ReferenceAnswer != *item.Task.ReferenceAnswer {
		t.Fatalf("saved rubric or reference lost: %+v", gc)
	}
}

func TestWrongContentWithCompletedInstructionIsAcceptedWithoutRetry(t *testing.T) {
	calls := 0
	grader := gradingFunc(func(context.Context, GradingContext, string) (Evaluation, error) {
		calls++
		result := validEvaluation()
		result.Score, result.Verdict = 0, "incorrect"
		result.CriterionResults[0].Satisfied = false
		result.CriterionResults[0].Explanation = "Названа регрессия, хотя целевая переменная имеет два класса."
		return result, nil
	})
	got, err := New(nil, nil, grader).EvaluateTrustedText(context.Background(), validVariantTask("main"), "Регрессия, потому что нужно предсказать число.")
	if err != nil || got.Score != 0 || got.Verdict != "incorrect" || calls != 1 {
		t.Fatalf("wrong content must stay zero despite completed instruction: %+v, %v, calls=%d", got, err, calls)
	}
}

func TestEvaluateVariantRejectsTwoPointsForBasic(t *testing.T) {
	_, err := New(
		&transcriptionStub{text: "классификация, потому что два класса"},
		&variantTaskStub{result: validVariantTask("basic")},
		&graderStub{result: validEvaluation()},
	).EvaluateVariant(context.Background(), "student-1", "tr-1", "variant-1", "variant-task-1")

	var f *fault.Error
	if !errors.As(err, &f) || f.Code != "INVALID_MODEL_RESPONSE" {
		t.Fatalf("expected INVALID_MODEL_RESPONSE for basic score 2, got %v", err)
	}
}

func TestEvaluateVariantRejectsInconsistentModelOutput(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Evaluation)
	}{
		{"out of range score", func(e *Evaluation) { e.Score = 4 }},
		{"inconsistent verdict", func(e *Evaluation) { e.Verdict = "partial" }},
		{"wrong criterion key", func(e *Evaluation) { e.CriterionResults[0].Key = "other" }},
		{"missing criterion", func(e *Evaluation) { e.CriterionResults = e.CriterionResults[:1] }},
		{"missing feedback", func(e *Evaluation) { e.Feedback = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := validMainEvaluation()
			tc.mutate(&result)
			_, err := New(&transcriptionStub{text: "ответ"}, &variantTaskStub{result: validVariantTask("main")}, &graderStub{result: result}).EvaluateVariant(context.Background(), "student-1", "tr-1", "variant-1", "variant-task-1")
			var f *fault.Error
			if !errors.As(err, &f) || f.Code != "INVALID_MODEL_RESPONSE" {
				t.Fatalf("expected INVALID_MODEL_RESPONSE, got %v", err)
			}
		})
	}
}

func TestEvaluateVariantWorksWithoutCriteriaOrEducationalContent(t *testing.T) {
	item := validVariantTask("main")
	item.Task.Criteria = nil
	item.Task.Outcome.EducationalContent = nil

	grader := &graderStub{result: validMainEvaluation()}
	got, err := New(
		&transcriptionStub{text: "классификация, потому что два класса"},
		&variantTaskStub{result: item},
		grader,
	).EvaluateVariant(context.Background(), "student-1", "tr-1", "variant-1", "variant-task-1")

	if err != nil || got.Score != 2 {
		t.Fatalf("expected grading from sparse historical snapshot, got %#v, %v", got, err)
	}
	if grader.gradingContext.MaterialContext.Knowledge != "" {
		t.Fatalf("expected empty optional educational content, got %q", grader.gradingContext.MaterialContext.Knowledge)
	}
	if len(grader.gradingContext.Criteria) != 2 {
		t.Fatalf("expected generated grading criteria, got %#v", grader.gradingContext.Criteria)
	}
}

func TestEvaluateVariantMandatoryCriterionFailureForcesZero(t *testing.T) {
	result := validMainEvaluation()
	result.Score = 0
	result.Verdict = "incorrect"
	result.CriterionResults[1] = CriterionResult{
		Key:         "instruction_following",
		Satisfied:   false,
		Explanation: "Тип задачи словами не назван.",
	}

	got, err := New(
		&transcriptionStub{text: "Потому что целевая переменная принимает два класса."},
		&variantTaskStub{result: validVariantTask("main")},
		&graderStub{result: result},
	).EvaluateVariant(context.Background(), "student-1", "tr-1", "variant-1", "variant-task-1")

	if err != nil || got.Score != 0 || got.Verdict != "incorrect" {
		t.Fatalf("expected mandatory failure to produce 0/incorrect, got %#v, %v", got, err)
	}
}

func TestEvaluateVariantRejectsPartialWhenMandatoryCriterionFailed(t *testing.T) {
	result := validMainEvaluation()
	result.Score = 1
	result.Verdict = "partial"
	result.CriterionResults[1].Satisfied = false
	result.CriterionResults[1].Explanation = "Тип задачи словами не назван."

	_, err := New(
		&transcriptionStub{text: "Потому что целевая переменная принимает два класса."},
		&variantTaskStub{result: validVariantTask("main")},
		&graderStub{result: result},
	).EvaluateVariant(context.Background(), "student-1", "tr-1", "variant-1", "variant-task-1")

	var f *fault.Error
	if !errors.As(err, &f) || f.Code != "INVALID_MODEL_RESPONSE" {
		t.Fatalf("expected INVALID_MODEL_RESPONSE, got %v", err)
	}
}

func TestEvaluateKeepsLongFeedbackWhole(t *testing.T) {
	feedback := strings.Repeat("Верно названы признаки, т. е. классы задачи. ", 12) + "Не хватает объяснения причины."
	result := validMainEvaluation()
	result.Feedback = feedback
	got, err := New(
		&transcriptionStub{text: "классификация, потому что два класса"},
		&variantTaskStub{result: validVariantTask("main")},
		&graderStub{result: result},
	).EvaluateVariant(context.Background(), "student-1", "tr-1", "variant-1", "variant-task-1")
	if err != nil || got.Score != result.Score || got.Feedback != feedback {
		t.Fatalf("long feedback must stay whole: %+v, %v", got, err)
	}
}

func TestEvaluateVariantRejectsMalformedFeedback(t *testing.T) {
	for _, feedback := range []string{"", "  \n ", "Наблюдение.\x00Пробел", string([]byte{0xff})} {
		result := validMainEvaluation()
		result.Feedback = feedback
		_, err := New(
			&transcriptionStub{text: "классификация, потому что два класса"},
			&variantTaskStub{result: validVariantTask("main")},
			&graderStub{result: result},
		).EvaluateVariant(context.Background(), "student-1", "tr-1", "variant-1", "variant-task-1")
		var f *fault.Error
		if !errors.As(err, &f) || f.Code != "INVALID_MODEL_RESPONSE" {
			t.Fatalf("expected malformed feedback to be rejected, got %v", err)
		}
	}
}

func TestEvaluateVariantStopsWhenSnapshotLookupFails(t *testing.T) {
	transcriptions := &transcriptionStub{text: "ответ"}
	course_map_tasks := &variantTaskStub{err: fault.New(fault.NotFound, "VARIANT_TASK_NOT_FOUND", "Задание варианта не найдено.")}
	grader := &graderStub{}

	_, err := New(transcriptions, course_map_tasks, grader).EvaluateVariant(
		context.Background(), "student-1", "tr-1", "variant-1", "variant-task-1",
	)
	if err == nil || transcriptions.id != "" || grader.answer != "" {
		t.Fatalf("expected snapshot lookup failure before transcription/model access, got %v", err)
	}
}

func TestEvaluateTrustedTextUsesTrainingScaleAndTaskInstruction(t *testing.T) {
	grader := &graderStub{result: validEvaluation()}
	service := New(nil, nil, grader)

	got, err := service.EvaluateTrustedText(context.Background(), validVariantTask("training"), "Классификация, потому что ответ принимает два класса.")
	if err != nil {
		t.Fatalf("trusted text evaluation failed: %v", err)
	}
	if got.Score != 2 || got.MaxScore != 2 || got.Verdict != "correct" {
		t.Fatalf("expected training scale 2/2, got %+v", got)
	}
	if grader.answer != "Классификация, потому что ответ принимает два класса." {
		t.Fatalf("grader received %q", grader.answer)
	}
	if grader.gradingContext.Criteria[1].Mandatory != true || !strings.Contains(grader.gradingContext.Criteria[1].Description, "Назовите тип задачи") {
		t.Fatalf("trusted grading omitted the mandatory instruction: %+v", grader.gradingContext.Criteria)
	}
}

func TestEvaluateTrustedTextRejectsBlankAnswer(t *testing.T) {
	grader := &graderStub{result: validEvaluation()}
	_, err := New(nil, nil, grader).EvaluateTrustedText(context.Background(), validVariantTask("training"), " \t ")
	if err == nil || grader.answer != "" {
		t.Fatalf("expected blank answer to fail before grading; err=%v answer=%q", err, grader.answer)
	}
}

func TestEvaluateTrustedTextRejectsScoreVerdictMismatch(t *testing.T) {
	result := validEvaluation()
	result.Score = 1
	grader := &graderStub{result: result}
	_, err := New(nil, nil, grader).EvaluateTrustedText(context.Background(), validVariantTask("training"), "Ответ ученика")
	var f *fault.Error
	if !errors.As(err, &f) || f.Code != "INVALID_MODEL_RESPONSE" {
		t.Fatalf("expected inconsistent score/verdict rejection, got %v", err)
	}
}

func TestEvaluateTrustedTextAllowsNumberOnlyWhenMandatoryInstructionIsMet(t *testing.T) {
	result := validEvaluation()
	result.Score = 0
	result.Verdict = "incorrect"
	result.CriterionResults[1] = CriterionResult{Key: "instruction_following", Satisfied: false, Explanation: "Тип задачи указан только номером."}
	grader := &graderStub{result: result}
	got, err := New(nil, nil, grader).EvaluateTrustedText(context.Background(), validVariantTask("training"), "1")
	if err != nil || got.Score != 0 || got.Verdict != "incorrect" {
		t.Fatalf("expected a number-only answer to fail the required instruction, got %+v, %v", got, err)
	}
	if !grader.gradingContext.Criteria[1].Mandatory || !strings.Contains(grader.gradingContext.Criteria[1].Description, "Назовите тип задачи") {
		t.Fatalf("number-only answer was not checked against the instruction: %+v", grader.gradingContext.Criteria)
	}
}

func TestEvaluateVariantNormalizesFeedbackLineBreaks(t *testing.T) {
	result := validMainEvaluation()
	result.Feedback = " Верно.\r\nОба критерия выполнены. \n Причина.\rУточнение. "
	got, err := New(&transcriptionStub{text: "ответ"}, &variantTaskStub{result: validVariantTask("main")}, &graderStub{result: result}).EvaluateVariant(context.Background(), "student-1", "tr-1", "variant-1", "variant-task-1")
	if want := "Верно. Оба критерия выполнены. Причина. Уточнение."; err != nil || got.Feedback != want || got.Score != 2 {
		t.Fatalf("expected normalized feedback and preserved grade, got %+v, %v", got, err)
	}
}

func (f gradingFunc) Grade(ctx context.Context, gc GradingContext, answer string) (Evaluation, error) {
	return f(ctx, gc, answer)
}

func TestEvaluateVariantRetriesInvalidModelResponse(t *testing.T) {
	for _, kind := range []string{"feedback", "JSON", "score"} {
		t.Run(kind, func(t *testing.T) {
			calls := 0
			grader := gradingFunc(func(_ context.Context, _ GradingContext, answer string) (Evaluation, error) {
				calls++
				if answer != "ответ" {
					t.Fatalf("retry changed answer: %q", answer)
				}
				result := validMainEvaluation()
				if calls == 1 {
					switch kind {
					case "feedback":
						result.Feedback = ""
					case "JSON":
						return Evaluation{}, fault.New(fault.Upstream, "INVALID_MODEL_RESPONSE", "Некорректный JSON")
					case "score":
						result.Score = 4
					}
				}
				return result, nil
			})
			got, err := New(&transcriptionStub{text: "ответ"}, &variantTaskStub{result: validVariantTask("main")}, grader).EvaluateVariant(context.Background(), "owner", "tr", "variant", "task")
			if err != nil || got.Score != 2 || got.MaxScore != 2 || got.Feedback == "" || calls != 2 {
				t.Fatalf("expected recovery after invalid %s, got %+v, %v (%d calls)", kind, got, err, calls)
			}
		})
	}
}

func TestEvaluateVariantRetryIsBounded(t *testing.T) {
	calls := 0
	grader := gradingFunc(func(context.Context, GradingContext, string) (Evaluation, error) {
		calls++
		result := validMainEvaluation()
		result.Feedback = ""
		return result, nil
	})
	got, err := New(&transcriptionStub{text: "ответ"}, &variantTaskStub{result: validVariantTask("main")}, grader).EvaluateVariant(context.Background(), "owner", "tr", "variant", "task")
	var f *fault.Error
	if !errors.As(err, &f) || f.Code != "INVALID_MODEL_RESPONSE" || calls != 2 || got.Verdict != "" {
		t.Fatalf("expected two failed attempts and no fabricated grade, got %+v, %v (%d calls)", got, err, calls)
	}
}

func TestEvaluateVariantDoesNotRetryProviderFailures(t *testing.T) {
	for _, code := range []string{"MODEL_TIMEOUT", "MODEL_UNAVAILABLE", "MODEL_UPSTREAM_ERROR"} {
		t.Run(code, func(t *testing.T) {
			calls := 0
			failure := fault.New(fault.Upstream, code, "Ошибка провайдера")
			grader := gradingFunc(func(context.Context, GradingContext, string) (Evaluation, error) {
				calls++
				return Evaluation{}, failure
			})
			_, err := New(&transcriptionStub{text: "ответ"}, &variantTaskStub{result: validVariantTask("main")}, grader).EvaluateVariant(context.Background(), "owner", "tr", "variant", "task")
			if err != failure || calls != 1 {
				t.Fatalf("expected original error without retry, got %v (%d calls)", err, calls)
			}
		})
	}
}

func TestEvaluateVariantDoesNotRetryAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	grader := gradingFunc(func(context.Context, GradingContext, string) (Evaluation, error) {
		calls++
		cancel()
		return Evaluation{}, fault.New(fault.Upstream, "INVALID_MODEL_RESPONSE", "Ошибка формата")
	})
	_, err := New(&transcriptionStub{text: "ответ"}, &variantTaskStub{result: validVariantTask("main")}, grader).EvaluateVariant(ctx, "owner", "tr", "variant", "task")
	if err == nil || calls != 1 {
		t.Fatalf("expected no retry after cancellation, got %v (%d calls)", err, calls)
	}
}

func TestMainUsesTwoPointsAndRejectsThree(t *testing.T) {
	for _, score := range []int{0, 1, 2, 3} {
		result := validEvaluation()
		result.Score = score
		if score == 0 {
			result.Verdict = "incorrect"
			result.CriterionResults[0].Satisfied = false
			result.CriterionResults[1].Satisfied = false
		}
		if score == 1 {
			result.Verdict = "partial"
			result.CriterionResults[0].Satisfied = false
		}
		got, err := New(&transcriptionStub{text: "ответ"}, nil, &graderStub{result: result}).EvaluateTrusted(context.Background(), "owner", "tr", validVariantTask("main"))
		if score == 3 {
			if err == nil {
				t.Fatal("main score 3 must be rejected")
			}
			continue
		}
		if err != nil || got.Score != score || got.MaxScore != 2 {
			t.Fatalf("score %d: %+v, %v", score, got, err)
		}
	}
}

func validMainEvaluation() Evaluation { result := validEvaluation(); result.Score = 2; return result }

type gradingFunc func(context.Context, GradingContext, string) (Evaluation, error)
