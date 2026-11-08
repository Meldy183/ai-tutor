package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math/rand/v2"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/assessment"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/audio"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/audioasset"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/diagnostic"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/exam"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/transcription"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/variant"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/shared/fault"
)

type Service struct {
	store          Store
	variants       VariantReader
	voice          Voice
	transcriptions transcription.Reader
	audio          audioasset.MetadataReader
	regenerator    audioasset.Regenerator
	grader         Grader
	ids            IDGenerator
	now            func() time.Time
	// rng shuffles constituents of equal coverage; tests may fix it.
	rng func() *rand.Rand
}

func (s *Service) WithAudioReader(reader audioasset.MetadataReader) *Service {
	s.audio = reader
	return s
}

func (s *Service) WithTranscriptions(reader transcription.Reader) *Service {
	s.transcriptions = reader
	return s
}

func (s *Service) WithAudioRegenerator(regenerator audioasset.Regenerator) *Service {
	s.regenerator = regenerator
	return s
}

func New(store Store, variants VariantReader, voice Voice, grader Grader, ids IDGenerator, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{store: store, variants: variants, voice: voice, grader: grader, ids: ids, now: now,
		rng: func() *rand.Rand { return rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64())) }}
}

// WithOrder fixes the order of constituents of equal coverage; nil keeps the
// variant order.
func (s *Service) WithOrder(rng func() *rand.Rand) *Service {
	s.rng = rng
	return s
}

func (s *Service) Start(ctx context.Context, ownerID, variantID, key string) (diagnostic.Progress, bool, error) {
	if err := validKey(key); err != nil {
		return diagnostic.Progress{}, false, err
	}
	if !validID(variantID) {
		return diagnostic.Progress{}, false, fault.Validation("variant_id", "Укажите ID варианта.")
	}
	requestDigest := digest(variantID)
	if existing, found, err := s.store.FindStart(ctx, ownerID, key, requestDigest); err != nil {
		return diagnostic.Progress{}, false, err
	} else if found {
		return existing.Progress(), true, nil
	}
	value, err := s.variants.Get(ctx, ownerID, variantID)
	if err != nil {
		return diagnostic.Progress{}, false, err
	}
	if value.OwnerID != ownerID || value.ID != variantID {
		return diagnostic.Progress{}, false, fault.New(fault.NotFound, "VARIANT_NOT_FOUND", "Вариант не найден.")
	}
	snapshot, err := snapshotVariant(value)
	if err != nil {
		return diagnostic.Progress{}, false, err
	}
	id, err := s.ids.New("diagnostic")
	if err != nil {
		return diagnostic.Progress{}, false, err
	}
	first, err := newTraversal(snapshot, s.rng())
	if err != nil {
		return diagnostic.Progress{}, false, err
	}
	session := diagnostic.Session{
		ID: id, OwnerID: ownerID, VariantID: variantID, Status: diagnostic.StatusActive,
		CurrentCompetency: first.CurrentCompetency, CurrentTask: first.CurrentTask, CurrentPosition: first.CurrentPosition,
		Variant:            snapshot,
		Traversal:          first.Traversal,
		AcceptedRequests:   make(map[string]diagnostic.AcceptedRequest),
		StartRequestDigest: requestDigest,
	}
	stored, reused, err := s.store.Create(ctx, ownerID, key, session.StartRequestDigest, session)
	if err != nil {
		return diagnostic.Progress{}, false, err
	}
	return stored.Progress(), reused, nil
}

func (s *Service) Read(ctx context.Context, ownerID, sessionID string) (diagnostic.Progress, error) {
	if !validID(sessionID) {
		return diagnostic.Progress{}, fault.Validation("id", "Укажите корректный ID диагностической сессии.")
	}
	value, err := s.store.Get(ctx, ownerID, sessionID)
	if err != nil {
		return diagnostic.Progress{}, err
	}
	return value.Progress(), nil
}

func (s *Service) LatestCompleted(ctx context.Context, ownerID, subjectID string) (diagnostic.Session, bool, error) {
	return s.store.LatestCompleted(ctx, ownerID, subjectID)
}
func (s *Service) LearningState(ctx context.Context, ownerID, subjectID string) (diagnostic.LearningState, error) {
	return s.store.LearningState(ctx, ownerID, subjectID)
}

func (s *Service) CurrentAudio(ctx context.Context, ownerID, sessionID, expectedTaskID string) (audioasset.Metadata, error) {
	if !validID(sessionID) {
		return audioasset.Metadata{}, fault.Validation("id", "Укажите корректный ID диагностической сессии.")
	}
	if !validID(expectedTaskID) {
		return audioasset.Metadata{}, fault.Validation("variant_task_id", "Укажите корректный ID задания.")
	}
	value, err := s.store.Get(ctx, ownerID, sessionID)
	if err != nil {
		return audioasset.Metadata{}, err
	}
	if value.Status != diagnostic.StatusActive {
		return audioasset.Metadata{}, fault.New(fault.Conflict, "DIAGNOSTIC_SESSION_COMPLETED", "Диагностическая сессия уже завершена.")
	}
	task := value.Current()
	if task == nil || task.ID != expectedTaskID {
		return audioasset.Metadata{}, fault.New(fault.Conflict, "DIAGNOSTIC_TASK_CHANGED", "Текущее задание изменилось.")
	}
	metadata := audioasset.Metadata{VariantTaskID: task.ID, Status: audioasset.Missing}
	if task.AudioAssetID == nil || *task.AudioAssetID == "" {
		return metadata, nil
	}
	if s.audio == nil {
		return audioasset.Metadata{}, fault.New(fault.Unavailable, "AUDIO_STORAGE_UNAVAILABLE", "Аудиозапись временно недоступна.")
	}
	asset, err := s.audio.Metadata(ctx, *task.AudioAssetID)
	if errors.Is(err, audioasset.ErrNotFound) {
		return metadata, nil
	}
	if err != nil {
		return audioasset.Metadata{}, fault.New(fault.Unavailable, "AUDIO_STORAGE_UNAVAILABLE", "Аудиозапись временно недоступна.")
	}
	metadata.Status = asset.Status
	if asset.Status == audioasset.Ready {
		canonicalURL, ok := audioasset.NormalizeFileURL(*task.AudioAssetID, asset.AudioURL)
		if asset.ID != *task.AudioAssetID || !ok {
			return audioasset.Metadata{}, fault.New(fault.Unavailable, "AUDIO_STORAGE_UNAVAILABLE", "Аудиозапись временно недоступна.")
		}
		metadata.AudioURL = canonicalURL
	}
	return metadata, nil
}

func (s *Service) RegenerateCurrentAudio(ctx context.Context, ownerID, sessionID, expectedTaskID string) (audioasset.Metadata, error) {
	if !validID(sessionID) {
		return audioasset.Metadata{}, fault.Validation("id", "Укажите корректный ID диагностической сессии.")
	}
	if !validID(expectedTaskID) {
		return audioasset.Metadata{}, fault.Validation("variant_task_id", "Укажите корректный ID задания.")
	}
	value, err := s.store.Get(ctx, ownerID, sessionID)
	if err != nil {
		return audioasset.Metadata{}, err
	}
	if value.Status != diagnostic.StatusActive {
		return audioasset.Metadata{}, fault.New(fault.Conflict, "DIAGNOSTIC_SESSION_COMPLETED", "Диагностическая сессия уже завершена.")
	}
	task := value.Current()
	if task == nil || task.ID != expectedTaskID {
		return audioasset.Metadata{}, fault.New(fault.Conflict, "DIAGNOSTIC_TASK_CHANGED", "Текущее задание изменилось.")
	}
	if task.AudioAssetID == nil || *task.AudioAssetID == "" {
		return audioasset.Metadata{}, fault.New(fault.Conflict, "AUDIO_NOT_REPAIRABLE", "Для задания нет сохранённой озвучки.")
	}
	if s.regenerator == nil {
		return audioasset.Metadata{}, fault.New(fault.Unavailable, "AUDIO_STORAGE_UNAVAILABLE", "Аудиозапись временно недоступна.")
	}
	asset, err := s.regenerator.Regenerate(ctx, *task.AudioAssetID)
	if err != nil {
		switch {
		case errors.Is(err, audioasset.ErrNotRepairable):
			return audioasset.Metadata{}, fault.New(fault.Conflict, "AUDIO_NOT_REPAIRABLE", "Аудиозапись сейчас нельзя восстановить.")
		case errors.Is(err, audioasset.ErrStorageUnavailable):
			return audioasset.Metadata{}, fault.New(fault.Unavailable, "AUDIO_STORAGE_UNAVAILABLE", "Хранилище аудио временно недоступно.")
		case errors.Is(err, audioasset.ErrGenerationFailed):
			return audioasset.Metadata{}, fault.New(fault.Unavailable, "AUDIO_GENERATION_FAILED", "Не удалось восстановить аудиозапись.")
		default:
			return audioasset.Metadata{}, fault.New(fault.Unavailable, "AUDIO_STORAGE_UNAVAILABLE", "Аудиозапись временно недоступна.")
		}
	}
	canonicalURL, ok := audioasset.NormalizeFileURL(*task.AudioAssetID, asset.AudioURL)
	if asset.Status != audioasset.Ready || asset.ID != *task.AudioAssetID || !ok {
		return audioasset.Metadata{}, fault.New(fault.Unavailable, "AUDIO_STORAGE_UNAVAILABLE", "Аудиозапись временно недоступна.")
	}
	return audioasset.Metadata{VariantTaskID: task.ID, Status: asset.Status, AudioURL: canonicalURL}, nil
}

func (s *Service) Answer(ctx context.Context, ownerID, sessionID, taskID, key string, data []byte, media string) (diagnostic.Progress, error) {
	return s.answerRequest(ctx, ownerID, sessionID, taskID, key, data, media, false, "")
}

func (s *Service) AnswerTranscription(ctx context.Context, ownerID, sessionID, taskID, key, transcriptionID string) (diagnostic.Progress, error) {
	if !validID(transcriptionID) {
		return diagnostic.Progress{}, fault.Validation("transcription_id", "Укажите сохранённую расшифровку.")
	}
	return s.answerRequest(ctx, ownerID, sessionID, taskID, key, nil, "transcription:"+transcriptionID, false, transcriptionID)
}

func (s *Service) Skip(ctx context.Context, ownerID, sessionID, taskID, key string) (diagnostic.Progress, error) {
	return s.answerRequest(ctx, ownerID, sessionID, taskID, key, nil, "", true, "")
}

func (s *Service) answerRequest(ctx context.Context, ownerID, sessionID, taskID, key string, data []byte, media string, skip bool, preparedID string) (diagnostic.Progress, error) {
	if err := validKey(key); err != nil {
		return diagnostic.Progress{}, err
	}
	if !validID(sessionID) {
		return diagnostic.Progress{}, fault.Validation("id", "Укажите корректный ID диагностической сессии.")
	}
	if !validID(taskID) {
		return diagnostic.Progress{}, fault.Validation("variant_task_id", "Укажите ID текущей позиции варианта.")
	}
	if !skip && preparedID == "" {
		if err := audio.Check(data, media); err != nil {
			return diagnostic.Progress{}, err
		}
	}
	value, err := s.store.Get(ctx, ownerID, sessionID)
	if err != nil {
		return diagnostic.Progress{}, err
	}
	// Bind retries to the task and exact audio payload, independent of multipart boundaries.
	h := sha256.New()
	if skip {
		media = "skip"
	}
	_, _ = h.Write([]byte(taskID + "\x00" + media + "\x00"))
	_, _ = h.Write(data)
	fingerprint := hex.EncodeToString(h.Sum(nil))
	token, err := s.ids.New("attempt")
	if err != nil {
		return diagnostic.Progress{}, err
	}
	accepted, reservation, err := s.store.Reserve(ctx, ownerID, sessionID, key, fingerprint, taskID, token)
	if err != nil {
		return diagnostic.Progress{}, err
	}
	if accepted != nil {
		return accepted.Response, nil
	}
	transcriptionID, text := reservation.TranscriptionID, reservation.TranscriptionText
	defer func() {
		_ = s.store.Fail(context.WithoutCancel(ctx), ownerID, sessionID, token, transcriptionID, text)
	}()
	current := value.Current()
	if current == nil || current.ID != taskID {
		return diagnostic.Progress{}, fault.New(fault.Conflict, "DIAGNOSTIC_TASK_NOT_CURRENT", "Укажите текущее задание сессии.")
	}
	if skip {
		id, err := s.ids.New("skip")
		if err != nil {
			return diagnostic.Progress{}, err
		}
		maxScore := exam.NestedMaxScore
		if current.Role == "main" {
			maxScore = exam.RootMaxScore
		}
		answer := diagnostic.Answer{
			Skipped:       true,
			VariantTaskID: current.ID, SourceTaskID: current.SourceTaskID,
			CompetencyID: current.CompetencyID, OutcomeID: current.OutcomeID, Role: current.Role,
			Task: *current, TranscriptionID: id, Text: "Я не знаю. Пропустить",
			GraderMaxScore: maxScore, Verdict: "incorrect",
			CriterionResults: []diagnostic.CriterionResult{{Key: "skipped", Satisfied: false, Explanation: "Вопрос пропущен."}},
			Feedback:         "Вопрос пропущен. Ответ оценён в 0 баллов.",
			CreatedAt:        s.now().Unix(),
		}
		transition, err := advance(value, answer)
		if err != nil {
			return diagnostic.Progress{}, err
		}
		return s.store.Accept(ctx, ownerID, sessionID, token, key, fingerprint, answer, transition)
	}
	if transcriptionID == "" {
		var transcribed transcription.Transcription
		var transcribeErr error
		if preparedID != "" {
			if s.transcriptions == nil {
				return diagnostic.Progress{}, fault.New(fault.Unavailable, "TRANSCRIPTION_UNAVAILABLE", "Расшифровка временно недоступна.")
			}
			text, readErr := s.transcriptions.TextByOwner(ctx, ownerID, preparedID)
			transcribeErr = readErr
			transcribed = transcription.Transcription{ID: preparedID, OwnerID: ownerID, Text: text}
		} else {
			transcribed, transcribeErr = s.voice.Transcribe(ctx, ownerID, data, media)
		}
		if transcribeErr != nil {
			return diagnostic.Progress{}, transcribeErr
		}
		if !validTranscription(transcribed, ownerID) {
			return diagnostic.Progress{}, fault.New(fault.Upstream, "TRANSCRIPTION_INVALID_RESPONSE", "Сервис распознавания вернул некорректную расшифровку.")
		}
		transcriptionID, text = transcribed.ID, transcribed.Text
	}
	evaluation, err := s.grader.EvaluateVariant(ctx, ownerID, transcriptionID, value.Variant.ID, current.ID)
	if err != nil {
		return diagnostic.Progress{}, err
	}
	if err = validateEvaluation(evaluation, current.Role); err != nil {
		return diagnostic.Progress{}, err
	}
	score := evaluation.Score
	criteria := make([]diagnostic.CriterionResult, 0, len(evaluation.CriterionResults))
	for _, item := range evaluation.CriterionResults {
		criteria = append(criteria, diagnostic.CriterionResult{Key: item.Key, Satisfied: item.Satisfied, Explanation: item.Explanation})
	}
	answer := diagnostic.Answer{
		VariantTaskID: current.ID, SourceTaskID: current.SourceTaskID,
		CompetencyID: current.CompetencyID, OutcomeID: current.OutcomeID, Role: current.Role,
		Task:            *current,
		TranscriptionID: transcriptionID, Text: text, GraderScore: evaluation.Score, GraderMaxScore: evaluation.MaxScore,
		Score: score, Verdict: evaluation.Verdict,
		CriterionResults: criteria, Feedback: evaluation.Feedback,
		CreatedAt: s.now().Unix(),
	}
	transition, err := advance(value, answer)
	if err != nil {
		return diagnostic.Progress{}, err
	}
	return s.store.Accept(ctx, ownerID, sessionID, token, key, fingerprint, answer, transition)
}

func (s *Service) Result(ctx context.Context, ownerID, sessionID string) (diagnostic.Result, error) {
	if !validID(sessionID) {
		return diagnostic.Result{}, fault.Validation("id", "Укажите корректный ID диагностической сессии.")
	}
	value, err := s.store.Get(ctx, ownerID, sessionID)
	if err != nil {
		return diagnostic.Result{}, err
	}
	if value.Status != diagnostic.StatusCompleted {
		return diagnostic.Result{}, fault.New(fault.Conflict, "DIAGNOSTIC_SESSION_ACTIVE", "Диагностическая сессия ещё не завершена.")
	}
	result := diagnostic.Result{
		SessionID: value.ID, Status: value.Status, VariantID: value.VariantID,
		MapRevision: value.Variant.MapRevision, IncludedCompetencyCount: value.Variant.IncludedCompetencyCount,
		SkippedCompetencies: append([]diagnostic.SkippedCompetency{}, value.Variant.SkippedCompetencies...), CompletedTasks: len(value.Answers),
		TotalTasks: value.Variant.TaskCount(),
		Answers:    append([]diagnostic.Answer(nil), value.Answers...), UntestedBasics: append([]diagnostic.TaskSnapshot{}, value.SkippedBasics...),
	}
	for _, answer := range value.Answers {
		// The raw points are those of the asked tasks on their scales; the
		// course result normalizes them by constituent and competency.
		result.DiagnosticScore += answer.Score
		result.MaximumScore += answer.GraderMaxScore
	}
	return result, nil
}

func (s *Service) ResultForTraining(ctx context.Context, ownerID, sessionID string) (diagnostic.Result, error) {
	return s.Result(ctx, ownerID, sessionID)
}

func validateEvaluation(value assessment.Evaluation, role string) error {
	maxScore := exam.RootMaxScore
	if role == "basic" {
		maxScore = exam.NestedMaxScore
	} else if role != "main" {
		return fault.New(fault.Upstream, "ASSESSMENT_INVALID_RESPONSE", "Задание содержит неизвестную роль.")
	}
	if value.MaxScore != maxScore || value.Score < 0 || value.Score > maxScore {
		return fault.New(fault.Upstream, "ASSESSMENT_INVALID_RESPONSE", "Грейдер вернул оценку вне диапазона роли задания.")
	}
	want := "incorrect"
	if value.Score == maxScore {
		want = "correct"
	} else if value.Score > 0 {
		want = "partial"
	}
	if value.Verdict != want || len(value.CriterionResults) == 0 {
		return fault.New(fault.Upstream, "ASSESSMENT_INVALID_RESPONSE", "Грейдер вернул некорректную структуру оценки.")
	}
	for _, item := range value.CriterionResults {
		if strings.TrimSpace(item.Key) == "" || strings.TrimSpace(item.Explanation) == "" {
			return fault.New(fault.Upstream, "ASSESSMENT_INVALID_RESPONSE", "Грейдер вернул некорректные результаты критериев.")
		}
	}
	if strings.TrimSpace(value.Feedback) == "" || strings.ContainsAny(value.Feedback, "\r\n") {
		return fault.New(fault.Upstream, "ASSESSMENT_INVALID_RESPONSE", "Грейдер вернул некорректный фидбэк.")
	}
	return nil
}

func snapshotVariant(value variant.Variant) (diagnostic.VariantSnapshot, error) {
	if !validID(value.ID) || len(value.Competencies) == 0 || value.IncludedCompetencyCount != len(value.Competencies) {
		return diagnostic.VariantSnapshot{}, fault.New(fault.Invalid, "VARIANT_INVALID", "Вариант не содержит заданий для диагностики.")
	}
	snapshot := diagnostic.VariantSnapshot{
		ID: value.ID, SubjectID: value.SubjectID, SubjectNameSnapshot: value.SubjectNameSnapshot, MapRevision: value.MapRevision, AlgorithmVersion: value.AlgorithmVersion,
		IncludedCompetencyCount: value.IncludedCompetencyCount,
		SkippedCompetencies:     make([]diagnostic.SkippedCompetency, 0, len(value.SkippedCompetencies)),
		Competencies:            make([]diagnostic.Competency, 0, len(value.Competencies)),
	}
	for _, skipped := range value.SkippedCompetencies {
		snapshot.SkippedCompetencies = append(snapshot.SkippedCompetencies, diagnostic.SkippedCompetency{ID: skipped.CompetencyID, Name: skipped.CompetencyName, Code: skipped.Code})
	}
	for _, selection := range value.Competencies {
		if !validID(selection.Competency.ID) || strings.TrimSpace(selection.Competency.Name) == "" || len(selection.Tasks) < 1 || len(selection.Tasks) > 3 {
			return diagnostic.VariantSnapshot{}, fault.New(fault.Invalid, "VARIANT_INVALID", "Компетенция варианта должна содержать основной и до двух базовых заданий.")
		}
		competency := diagnostic.Competency{ID: selection.Competency.ID, Name: selection.Competency.Name, Position: selection.Position, Tasks: make([]diagnostic.TaskSnapshot, 0, 3)}
		for i, item := range selection.Tasks {
			wantRole := "basic"
			if i == 0 {
				wantRole = "main"
			}
			if !validID(item.ID) || !validID(item.Task.ID) || item.Role != wantRole ||
				!validID(item.Task.Competency.ID) || item.Task.Competency.ID != selection.Competency.ID ||
				strings.TrimSpace(item.Task.Competency.Name) == "" || !validID(item.Task.Constituent.ID) ||
				strings.TrimSpace(item.Task.Constituent.Name) == "" || !validID(item.Task.Outcome.ID) ||
				strings.TrimSpace(item.Task.Outcome.Name) == "" ||
				item.Task.ReferenceAnswer == nil || item.Task.VoiceInstruction == nil ||
				!variant.ValidTaskContent(item.Task.Question, item.Task.Options, *item.Task.VoiceInstruction, *item.Task.ReferenceAnswer) {
				return diagnostic.VariantSnapshot{}, fault.New(fault.Invalid, "VARIANT_INVALID", "Вариант содержит неполное или некорректное задание.")
			}
			voiceInstruction, reference, criteria := "", "", ""
			if item.Task.VoiceInstruction != nil {
				voiceInstruction = *item.Task.VoiceInstruction
			}
			if item.Task.ReferenceAnswer != nil {
				reference = *item.Task.ReferenceAnswer
			}
			if item.Task.Criteria != nil {
				criteria = *item.Task.Criteria
			}
			taxonomy, level, content := "", "", ""
			importance, included := int16(0), false
			if item.Task.Outcome.TaxonomyCode != nil {
				taxonomy = *item.Task.Outcome.TaxonomyCode
			}
			if item.Task.Outcome.ALDLevelCode != nil {
				level = *item.Task.Outcome.ALDLevelCode
			}
			if item.Task.Outcome.EducationalContent != nil {
				content = *item.Task.Outcome.EducationalContent
			}
			if item.Task.Outcome.Importance != nil {
				importance = *item.Task.Outcome.Importance
			}
			if item.Task.Outcome.IncludeInTest != nil {
				included = *item.Task.Outcome.IncludeInTest
			}
			options := append([]string{}, item.Task.Options...)
			competency.Tasks = append(competency.Tasks, diagnostic.TaskSnapshot{
				ID: item.ID, SourceTaskID: item.Task.ID, Role: item.Role,
				CompetencyID: item.Task.Competency.ID, CompetencyName: item.Task.Competency.Name,
				ConstituentID: item.Task.Constituent.ID, ConstituentName: item.Task.Constituent.Name,
				OutcomeID: item.Task.Outcome.ID, OutcomeName: item.Task.Outcome.Name,
				Question: item.Task.Question, Options: options,
				VoiceInstruction: voiceInstruction, AudioAssetID: item.Task.AudioAssetID, ReferenceAnswer: reference, Criteria: criteria,
				TaxonomyCode: taxonomy, ALDLevelCode: level, Importance: importance,
				IncludeInTest: included, EducationalContent: content,
			})
		}
		snapshot.Competencies = append(snapshot.Competencies, competency)
	}
	return snapshot, nil
}

func validKey(key string) error {
	if len(key) < 1 || len(key) > 128 || strings.TrimSpace(key) != key || strings.IndexFunc(key, func(r rune) bool { return r < 0x21 || r > 0x7e }) >= 0 {
		return fault.Validation("Idempotency-Key", "Укажите ключ длиной от 1 до 128 печатных ASCII-символов без пробелов.")
	}
	return nil
}

func validID(value string) bool {
	if len(value) == 0 || len(value) > 128 || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.') {
			return false
		}
	}
	return true
}

func validTaskText(value string, maxRunes int) bool {
	return utf8.ValidString(value) && strings.TrimSpace(value) != "" && strings.IndexByte(value, 0) < 0 && utf8.RuneCountInString(value) <= maxRunes
}

func validTranscription(value transcription.Transcription, ownerID string) bool {
	return validID(value.ID) && value.OwnerID == ownerID && validTaskText(value.Text, 20000)
}

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
