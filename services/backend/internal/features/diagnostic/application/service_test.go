package application

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand/v2"
	"sync"
	"testing"
	"time"

	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/assessment"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/diagnostic"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/transcription"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/variant"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/features/diagnostic/infrastructure/memory"
)

type variantStub struct {
	value variant.Variant
	calls int
}

func (s *variantStub) Get(context.Context, string, string) (variant.Variant, error) {
	s.calls++
	return s.value, nil
}

type voiceStub struct {
	mu            sync.Mutex
	transcribes   int
	syntheses     int
	transcription transcription.Transcription
}

func (s *voiceStub) Transcribe(_ context.Context, ownerID string, _ []byte, _ string) (transcription.Transcription, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.transcribes++
	value := s.transcription
	value.OwnerID = ownerID
	return value, nil
}

type graderStub struct {
	mu              sync.Mutex
	results         []gradeResult
	calls           int
	ownerID         string
	transcriptionID string
	variantID       string
	variantTaskID   string
	started         chan struct{}
	continueCall    chan struct{}
}

type gradeResult struct {
	evaluation assessment.Evaluation
	err        error
}

func (s *graderStub) EvaluateVariant(ctx context.Context, ownerID, transcriptionID, variantID, variantTaskID string) (assessment.Evaluation, error) {
	s.mu.Lock()
	s.calls++
	s.ownerID, s.transcriptionID, s.variantID, s.variantTaskID = ownerID, transcriptionID, variantID, variantTaskID
	if ownerID == "" || transcriptionID == "" || variantID == "" || variantTaskID == "" {
		s.mu.Unlock()
		return assessment.Evaluation{}, errors.New("grader IDs are empty")
	}
	index := min(s.calls-1, len(s.results)-1)
	result := s.results[index]
	started, continueCall := s.started, s.continueCall
	s.mu.Unlock()
	if started != nil {
		select {
		case started <- struct{}{}:
		default:
		}
	}
	if continueCall != nil {
		select {
		case <-continueCall:
		case <-ctx.Done():
			return assessment.Evaluation{}, ctx.Err()
		}
	}
	return result.evaluation, result.err
}

type idStub struct{ next int }

func (s *idStub) New(prefix string) (string, error) {
	s.next++
	return fmt.Sprintf("%s-%d", prefix, s.next), nil
}

func newTestService(results ...gradeResult) (*Service, *voiceStub, *graderStub, *variantStub) {
	voice := &voiceStub{transcription: transcription.Transcription{ID: "transcription-1", Text: "ответ"}}
	grader := &graderStub{results: results}
	learning_variants := &variantStub{value: testVariant()}
	service := New(memory.New(), learning_variants, voice, grader, &idStub{}, func() time.Time { return time.Unix(10, 0) })
	return service.WithOrder(func() *rand.Rand { return nil }), voice, grader, learning_variants
}

func testVariant() variant.Variant {
	value := variant.Variant{ID: "variant-1", OwnerID: "owner-1", MapRevision: 7, AlgorithmVersion: "v1", IncludedCompetencyCount: 2}
	for competency := 0; competency < 2; competency++ {
		selection := variant.CompetencySelection{Position: competency + 1, Competency: variant.CompetencyProfile{ID: fmt.Sprintf("comp-%d", competency), Name: "Компетенция"}}
		for task := 0; task < 3; task++ {
			instruction := "Назовите ответ и объясните"
			selection.Tasks = append(selection.Tasks, variant.VariantTask{
				ID: fmt.Sprintf("variant-task-%d-%d", competency, task), Role: []string{"main", "basic", "basic"}[task],
				Task: variant.TaskProfile{ID: fmt.Sprintf("task-%d-%d", competency, task), Question: "Вопрос", Options: []string{"Ответ A", "Ответ B"}, VoiceInstruction: &instruction, ReferenceAnswer: ptr("Ответ A"), Criteria: ptr("criterion"),
					Competency:  variant.CompetencyProfile{ID: fmt.Sprintf("comp-%d", competency), Name: "Компетенция"},
					Constituent: variant.ConstituentProfile{ID: fmt.Sprintf("constituent-%d", competency), Name: "Составляющая"},
					Outcome:     variant.OutcomeProfile{ID: fmt.Sprintf("outcome-%d-%d", competency, task), Name: "ОР", TaxonomyCode: ptr("analysis"), Importance: ptr(int16(3)), IncludeInTest: ptr(true), EducationalContent: ptr("ОС")}},
			})
		}
		value.Competencies = append(value.Competencies, selection)
	}
	return value
}

func ptr[T any](value T) *T { return &value }

func grade(score int) gradeResult {
	verdict := "incorrect"
	if score == 1 {
		verdict = "partial"
	} else if score == 2 {
		verdict = "correct"
	}
	return gradeResult{evaluation: assessment.Evaluation{Score: score, MaxScore: 2, Verdict: verdict,
		CriterionResults: []assessment.CriterionResult{{Key: "c1", Satisfied: score == 2, Explanation: "Объяснение"}},
		Feedback:         "Статус Причина Совет"}}
}

func gradeBasic(score int) gradeResult {
	verdict := "incorrect"
	if score == 1 {
		verdict = "correct"
	}
	return gradeResult{evaluation: assessment.Evaluation{Score: score, MaxScore: 1, Verdict: verdict,
		CriterionResults: []assessment.CriterionResult{{Key: "c1", Satisfied: score == 1, Explanation: "Объяснение"}},
		Feedback:         "Статус Причина Совет"}}
}

func (s *Service) start(t *testing.T) diagnostic.Progress {
	t.Helper()
	progress, _, err := s.Start(context.Background(), "owner-1", "variant-1", "start-1")
	if err != nil {
		t.Fatal(err)
	}
	return progress
}

func (s *Service) answer(t *testing.T, progress diagnostic.Progress, key string) diagnostic.Progress {
	t.Helper()
	if progress.Current == nil {
		t.Fatal("session has no current task")
	}
	result, err := s.Answer(context.Background(), "owner-1", progress.SessionID, progress.Current.ID, key, testAudio(0), "audio/wav")
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func testAudio(sample byte) []byte {
	data := make([]byte, 46)
	copy(data[0:4], "RIFF")
	binary.LittleEndian.PutUint32(data[4:8], uint32(len(data)-8))
	copy(data[8:12], "WAVE")
	copy(data[12:16], "fmt ")
	binary.LittleEndian.PutUint32(data[16:20], 16)
	binary.LittleEndian.PutUint16(data[20:22], 1)
	binary.LittleEndian.PutUint16(data[22:24], 1)
	binary.LittleEndian.PutUint32(data[24:28], 8000)
	binary.LittleEndian.PutUint32(data[28:32], 16000)
	binary.LittleEndian.PutUint16(data[32:34], 2)
	binary.LittleEndian.PutUint16(data[34:36], 16)
	copy(data[36:40], "data")
	binary.LittleEndian.PutUint32(data[40:44], 2)
	data[44] = sample
	return data
}

func TestMainPerfectSkipsBasicsAndIdempotencySurvivesCompletion(t *testing.T) {
	service, voice, grader, learning_variants := newTestService(grade(2), grade(2))
	started := service.start(t)
	replayed, reused, err := service.Start(context.Background(), "owner-1", "variant-1", "start-1")
	if err != nil || !reused || replayed.SessionID != started.SessionID || learning_variants.calls != 1 {
		t.Fatalf("start replay = (%+v, %v, %v), variant reads=%d", replayed, reused, err, learning_variants.calls)
	}
	progress := service.answer(t, started, "answer-1")
	if progress.Text != "ответ" {
		t.Fatalf("answer response lost transcription: %+v", progress)
	}
	if progress.Current == nil || progress.Current.ID != "variant-task-1-0" || progress.Skipped != 2 || progress.Completed != 1 {
		t.Fatalf("main=2 did not skip the basics: %+v", progress)
	}
	if grader.variantID != "variant-1" || grader.variantTaskID != "variant-task-0-0" || grader.transcriptionID != "transcription-1" {
		t.Fatalf("grader received wrong variant identity: variant=%q variant_task=%q transcription=%q", grader.variantID, grader.variantTaskID, grader.transcriptionID)
	}
	read, err := service.Read(context.Background(), "owner-1", started.SessionID)
	if err != nil || read.Skipped != progress.Skipped {
		t.Fatalf("GET progress skipped count = %d, answer response = %d, err=%v", read.Skipped, progress.Skipped, err)
	}
	completed := service.answer(t, progress, "answer-2")
	if completed.Status != diagnostic.StatusCompleted || completed.Current != nil {
		t.Fatalf("session did not complete: %+v", completed)
	}
	replay := service.answer(t, progress, "answer-2")
	if replay.Text != completed.Text || replay.Text != "ответ" {
		t.Fatalf("idempotent replay lost transcription: %+v", replay)
	}
	if replay.Status != diagnostic.StatusCompleted || grader.calls != 2 || voice.transcribes != 2 {
		t.Fatalf("accepted retry performed work twice: grader=%d STT=%d", grader.calls, voice.transcribes)
	}
	result, err := service.Result(context.Background(), "owner-1", completed.SessionID)
	if err != nil || result.DiagnosticScore != 4 || result.MaximumScore != 4 || len(result.UntestedBasics) != 4 {
		t.Fatalf("unexpected completed result %+v, err=%v", result, err)
	}
}

func TestSkipAdvancesWithoutSpeechOrGrading(t *testing.T) {
	service, voice, grader, _ := newTestService()
	started := service.start(t)
	progress, err := service.Skip(context.Background(), "owner-1", started.SessionID, started.Current.ID, "skip-1")
	if err != nil {
		t.Fatal(err)
	}
	if progress.Current == nil || progress.Current.ID != "variant-task-0-1" || progress.Completed != 1 || progress.Score == nil || *progress.Score != 0 || progress.Text != "Я не знаю. Пропустить" {
		t.Fatalf("skip did not advance with zero score: %+v", progress)
	}
	if voice.transcribes != 0 || grader.calls != 0 {
		t.Fatalf("skip called speech or grader: transcribes=%d grades=%d", voice.transcribes, grader.calls)
	}
	replayed, err := service.Skip(context.Background(), "owner-1", started.SessionID, started.Current.ID, "skip-1")
	if err != nil || replayed.Current == nil || replayed.Current.ID != progress.Current.ID {
		t.Fatalf("skip replay = %+v, %v", replayed, err)
	}
	basic, err := service.Skip(context.Background(), "owner-1", started.SessionID, progress.Current.ID, "skip-2")
	if err != nil || basic.GraderMaxScore == nil || *basic.GraderMaxScore != 1 || basic.Current == nil || basic.Current.ID != "variant-task-0-2" {
		t.Fatalf("basic skip = %+v, %v", basic, err)
	}
	if voice.transcribes != 0 || grader.calls != 0 {
		t.Fatalf("basic skip called speech or grader: transcribes=%d grades=%d", voice.transcribes, grader.calls)
	}
}

func TestCanceledGradingReleasesReservationAndRetryReusesTranscription(t *testing.T) {
	service, voice, grader, _ := newTestService(grade(2))
	started := service.start(t)
	grader.started = make(chan struct{}, 1)
	grader.continueCall = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() {
		_, err := service.Answer(ctx, "owner-1", started.SessionID, started.Current.ID, "answer-1", testAudio(0), "audio/wav")
		finished <- err
	}()
	<-grader.started
	cancel()
	if err := <-finished; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled answer error = %v, want context canceled", err)
	}
	grader.continueCall = nil
	progress, err := service.Answer(context.Background(), "owner-1", started.SessionID, started.Current.ID, "answer-1", testAudio(0), "audio/wav")
	if err != nil || progress.Current == nil || progress.Current.ID != "variant-task-1-0" || progress.Skipped != 2 {
		t.Fatalf("retry after cancellation = (%+v, %v)", progress, err)
	}
	if voice.transcribes != 1 || grader.calls != 2 {
		t.Fatalf("retry work counts: STT=%d grader=%d, want 1 and 2", voice.transcribes, grader.calls)
	}
}

func TestRoleSpecificScoresAndBasicsContributeToDiagnostic(t *testing.T) {
	service, _, _, _ := newTestService(grade(1), gradeBasic(0), gradeBasic(1), grade(2))
	progress := service.start(t)
	progress = service.answer(t, progress, "main")
	progress = service.answer(t, progress, "basic-zero")
	if progress.Score == nil || *progress.Score != 0 || progress.GraderScore == nil || *progress.GraderScore != 0 || progress.Current.Role != "basic" {
		t.Fatalf("basic score 0 was not retained: %+v", progress)
	}
	progress = service.answer(t, progress, "basic-one")
	if progress.Score == nil || *progress.Score != 1 || progress.GraderScore == nil || *progress.GraderScore != 1 || progress.Current.ID != "variant-task-1-0" {
		t.Fatalf("basic score 1 was not preserved: %+v", progress)
	}
	if progress.GraderMaxScore == nil || *progress.GraderMaxScore != 1 {
		t.Fatalf("basic grader max score = %v, want 1", progress.GraderMaxScore)
	}
	progress = service.answer(t, progress, "next-main")
	result, err := service.Result(context.Background(), "owner-1", progress.SessionID)
	if err != nil || result.DiagnosticScore != 4 || result.MaximumScore != 6 {
		t.Fatalf("unexpected diagnostic sum including basics: %+v, err=%v", result, err)
	}
	if result.Answers[2].GraderScore != 1 || result.Answers[2].GraderMaxScore != 1 || result.Answers[2].Score != 1 || result.Answers[2].Verdict != "correct" {
		t.Fatalf("source grader result was not preserved: %+v", result.Answers[2])
	}
}

func TestGraderFailureRetriesStoredTranscriptionWithoutRegradingAcceptedAnswer(t *testing.T) {
	service, voice, grader, _ := newTestService(gradeResult{err: errors.New("model unavailable")}, grade(2), grade(0))
	progress := service.start(t)
	taskID := progress.Current.ID
	if _, err := service.Answer(context.Background(), "owner-1", progress.SessionID, taskID, "same-key", testAudio(0), "audio/wav"); err == nil {
		t.Fatal("first grader call should fail")
	}
	progress, err := service.Answer(context.Background(), "owner-1", progress.SessionID, taskID, "same-key", testAudio(0), "audio/wav")
	if err != nil || progress.Score == nil || *progress.Score != 2 || voice.transcribes != 1 || grader.calls != 2 {
		t.Fatalf("retry did not reuse transcription: progress=%+v STT=%d grader=%d err=%v", progress, voice.transcribes, grader.calls, err)
	}
	if _, err = service.Answer(context.Background(), "owner-1", progress.SessionID, taskID, "same-key", testAudio(1), "audio/wav"); err == nil {
		t.Fatal("reusing key with different audio should fail")
	}
}

func TestConcurrentAnswerIsRejectedWithoutHoldingLockAcrossGrader(t *testing.T) {
	grader := &graderStub{results: []gradeResult{grade(1)}, started: make(chan struct{}, 1), continueCall: make(chan struct{})}
	voice := &voiceStub{transcription: transcription.Transcription{ID: "transcription-1", Text: "ответ"}}
	service := New(memory.New(), &variantStub{value: testVariant()}, voice, grader, &idStub{}, nil).WithOrder(func() *rand.Rand { return nil })
	progress := service.start(t)
	firstDone := make(chan error, 1)
	go func() {
		_, err := service.Answer(context.Background(), "owner-1", progress.SessionID, progress.Current.ID, "first", testAudio(0), "audio/wav")
		firstDone <- err
	}()
	<-grader.started
	if _, err := service.Answer(context.Background(), "owner-1", progress.SessionID, progress.Current.ID, "second", testAudio(1), "audio/wav"); err == nil {
		t.Fatal("overlapping answer was accepted")
	}
	close(grader.continueCall)
	if err := <-firstDone; err != nil {
		t.Fatalf("first request failed: %v", err)
	}
}

func TestVariableSizeCompetenciesAdvanceAndCountActualTasks(t *testing.T) {
	for _, perfect := range []bool{false, true} {
		t.Run(fmt.Sprintf("perfect=%v", perfect), func(t *testing.T) {
			// Constituents covering more tasks are asked first: the full one,
			// then the one with a single basic, then the main alone.
			results := []gradeResult{grade(0), gradeBasic(0), gradeBasic(1), grade(1), gradeBasic(1), grade(0)}
			if perfect {
				results = []gradeResult{grade(2), grade(2), grade(2)}
			}
			service, _, _, learning_variants := newTestService(results...)
			value := testVariant()
			third := testVariant().Competencies[0]
			third.Competency.ID = "comp-2"
			third.Position = 3
			for i := range third.Tasks {
				third.Tasks[i].ID = fmt.Sprintf("variant-task-2-%d", i)
				third.Tasks[i].Task.Competency = third.Competency
				third.Tasks[i].Task.Outcome.ID = fmt.Sprintf("outcome-2-%d", i)
				third.Tasks[i].Task.Constituent.ID = "constituent-2"
			}
			value.Competencies[0].Tasks = value.Competencies[0].Tasks[:1]
			value.Competencies[1].Tasks = value.Competencies[1].Tasks[:2]
			value.Competencies = append(value.Competencies, third)
			value.IncludedCompetencyCount = 3
			learning_variants.value = value
			progress := service.start(t)
			if progress.Total != 6 {
				t.Fatalf("initial total = %d", progress.Total)
			}
			wantIDs := []string{"variant-task-2-0", "variant-task-2-1", "variant-task-2-2", "variant-task-1-0", "variant-task-1-1", "variant-task-0-0"}
			if perfect {
				wantIDs = []string{"variant-task-2-0", "variant-task-1-0", "variant-task-0-0"}
			}
			for i, want := range wantIDs {
				if progress.Current == nil || progress.Current.ID != want {
					t.Fatalf("step %d: %+v", i, progress)
				}
				restored, err := service.Read(context.Background(), "owner-1", progress.SessionID)
				if err != nil || restored.Total != 6 || restored.Current.ID != want {
					t.Fatalf("restore: %+v / %v", restored, err)
				}
				progress = service.answer(t, progress, fmt.Sprintf("answer-%d", i))
			}
			if progress.Status != diagnostic.StatusCompleted || progress.Completed+progress.Skipped != 6 {
				t.Fatalf("final: %+v", progress)
			}
			result, err := service.Result(context.Background(), "owner-1", progress.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			wantScore, wantMaximum, wantSkipped := 3, 9, 0
			if perfect {
				wantScore, wantMaximum, wantSkipped = 6, 6, 3
			}
			if result.TotalTasks != 6 || result.MaximumScore != wantMaximum || result.DiagnosticScore != wantScore || len(result.UntestedBasics) != wantSkipped {
				t.Fatalf("result: %+v", result)
			}
		})
	}
}

type transcriptionReaderStub struct {
	owner, id, text string
	calls           int
}

func (r *transcriptionReaderStub) TextByOwner(_ context.Context, owner, id string) (string, error) {
	r.calls++
	if owner != r.owner || id != r.id {
		return "", errors.New("transcription not owned")
	}
	return r.text, nil
}

func TestConfirmedTranscriptionReusesPreviewAndIdempotentResult(t *testing.T) {
	s, voice, grader, _ := newTestService(grade(2))
	reader := &transcriptionReaderStub{owner: "owner-1", id: "preview-1", text: "Показанный ответ"}
	s.WithTranscriptions(reader)
	progress, _, err := s.Start(t.Context(), "owner-1", "variant-1", "start")
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.AnswerTranscription(t.Context(), "owner-1", progress.SessionID, progress.Current.ID, "answer", "preview-1")
	if err != nil || got.Text != reader.text || voice.transcribes != 0 || grader.transcriptionID != "preview-1" {
		t.Fatalf("prepared answer: %+v %v STT=%d", got, err, voice.transcribes)
	}
	if _, err = s.AnswerTranscription(t.Context(), "owner-1", progress.SessionID, progress.Current.ID, "answer", "preview-1"); err != nil || grader.calls != 1 || reader.calls != 1 {
		t.Fatalf("repeat: %v grader=%d reader=%d", err, grader.calls, reader.calls)
	}
	if _, err = s.AnswerTranscription(t.Context(), "owner-1", progress.SessionID, progress.Current.ID, "answer", "other-id"); err == nil {
		t.Fatal("same key accepted another transcript")
	}
}

func TestForeignPreviewDoesNotGradeOrAdvanceDiagnostic(t *testing.T) {
	s, voice, grader, _ := newTestService(grade(2))
	s.WithTranscriptions(&transcriptionReaderStub{owner: "someone-else", id: "preview-1", text: "Ответ"})
	p, _, err := s.Start(t.Context(), "owner-1", "variant-1", "start")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AnswerTranscription(t.Context(), "owner-1", p.SessionID, p.Current.ID, "answer", "preview-1"); err == nil {
		t.Fatal("foreign transcript accepted")
	}
	after, err := s.Read(t.Context(), "owner-1", p.SessionID)
	if err != nil || after.Completed != 0 || grader.calls != 0 || voice.transcribes != 0 {
		t.Fatalf("advanced without owned text: %+v %v", after, err)
	}
}

func TestBasicsContributeToTheRawPointsOfAskedTasks(t *testing.T) {
	service, _, _, _ := newTestService(grade(1), gradeBasic(1), gradeBasic(1), grade(2))
	progress := service.start(t)
	for _, key := range []string{"main", "basic-one", "basic-two", "perfect"} {
		progress = service.answer(t, progress, key)
	}
	result, err := service.Result(context.Background(), "owner-1", progress.SessionID)
	// 1 + 1 + 1 of 2 + 1 + 1, then 2 of 2; the second constituent's basics are untested.
	if err != nil || result.DiagnosticScore != 5 || result.MaximumScore != 6 || len(result.UntestedBasics) != 2 {
		t.Fatalf("result: %+v, %v", result, err)
	}
}
