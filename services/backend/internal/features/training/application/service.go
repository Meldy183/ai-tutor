package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode"

	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/assessment"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/audio"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/audioasset"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/diagnostic"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/exam"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/training"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/transcription"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/shared/fault"
)

type Diagnostics interface {
	Get(context.Context, string, string) (diagnostic.Session, error)
}
type TrainingBank interface {
	Tasks(context.Context, int64) ([]training.Task, error)
}

type Voice interface {
	Transcribe(context.Context, string, []byte, string) (transcription.Transcription, error)
}
type Grader interface {
	EvaluateTraining(context.Context, string, string, training.Exercise) (assessment.Evaluation, error)
}
type IDs interface{ New(string) (string, error) }
type ExerciseProvider interface {
	Generate(context.Context, training.Target, []training.Attempt) (training.Task, string, error)
}
type Repository interface {
	Get(context.Context, string, string) (training.Session, error)
	ByDiagnostic(context.Context, string, string) (training.Session, error)
	ByStartKey(context.Context, string, string, string) (training.Session, bool, error)
	Practice(context.Context, string) (int64, []training.Task, error)
	Create(context.Context, training.Session, string, *int64) (training.Session, bool, error)
	Reserve(context.Context, string, string, string, string, string, string) (*training.Progress, *training.Reservation, training.Session, error)
	Accept(context.Context, string, training.Reservation, training.Session, training.Attempt) (training.Progress, error)
	Fail(context.Context, string, string, training.Reservation) error
	ResetReservation(context.Context, string, string) error
	History(context.Context, string, string, int64, int) ([]training.Attempt, error)
}
type Service struct {
	repo        Repository
	diagnostics Diagnostics
	voice       Voice
	grader      Grader
	provider    ExerciseProvider
	ids         IDs
	now         func() time.Time
	audio       audioasset.MetadataReader
	regenerator audioasset.Regenerator
	checkedNext CheckedNextRepository
	bank        TrainingBank
}

func (s *Service) WithBank(bank TrainingBank) *Service { s.bank = bank; return s }

func (s *Service) WithCheckedNext(repository CheckedNextRepository) *Service {
	s.checkedNext = repository
	return s
}

func New(repo Repository, diagnostics Diagnostics, voice Voice, grader Grader, provider ExerciseProvider, ids IDs, now func() time.Time) *Service {
	return &Service{repo: repo, diagnostics: diagnostics, voice: voice, grader: grader, provider: provider, ids: ids, now: now}
}
func (s *Service) WithAudio(reader audioasset.MetadataReader) *Service { s.audio = reader; return s }
func (s *Service) WithAudioRegenerator(regenerator audioasset.Regenerator) *Service {
	s.regenerator = regenerator
	return s
}
func ValidKey(key string) error {
	if key == "" || len(key) > 128 || strings.TrimSpace(key) != key || strings.IndexFunc(key, unicode.IsControl) >= 0 {
		return fault.Validation("Idempotency-Key", "Укажите ключ длиной от 1 до 128 символов.")
	}
	return nil
}
func (s *Service) plan(ctx context.Context, owner, id string) (training.Session, error) {
	d, err := s.diagnostics.Get(ctx, owner, id)
	if err != nil {
		return training.Session{}, err
	}
	if d.Status != diagnostic.StatusCompleted {
		return training.Session{}, fault.New(fault.Conflict, "DIAGNOSTIC_SESSION_ACTIVE", "Сначала завершите диагностику.")
	}
	state := training.Session{OwnerID: owner, DiagnosticID: id, SubjectID: d.Variant.SubjectID, SubjectName: d.Variant.SubjectNameSnapshot, Mode: "focused", PlanRevision: d.Variant.MapRevision, Round: 1, Targets: []training.Target{}}
	targets := diagnostic.TrainingTargets(d.Answers)
	for _, target := range targets {
		raw, err := json.Marshal(target.Answer.Task)
		if err != nil {
			return state, err
		}
		var task training.Task
		if err = json.Unmarshal(raw, &task); err != nil {
			return state, err
		}
		max := diagnosticAnswerMaxScore(target.Answer)
		criteria := []training.CriterionResult{}
		for _, c := range target.Answer.CriterionResults {
			criteria = append(criteria, training.CriterionResult{Key: c.Key, Satisfied: c.Satisfied, Explanation: c.Explanation})
		}
		state.Targets = append(state.Targets, training.Target{Kind: target.Kind, Source: task, OriginalScore: target.Answer.Score, OriginalMaxScore: max, OriginalFeedback: target.Answer.Feedback, OriginalCriteria: criteria})
	}
	if s.bank != nil {
		bank, err := s.bank.Tasks(ctx, d.Variant.MapRevision)
		if err != nil {
			return state, err
		}
		open := diagnostic.OpenConstituents(d.Answers)
		seen := map[string]int{}
		mastered := map[string]bool{}
		for i, target := range state.Targets {
			seen[target.Source.OutcomeID] = i
		}
		for _, a := range d.Answers {
			if a.Score >= diagnostic.AnswerMaximum(a) && !a.Skipped {
				mastered[a.Task.OutcomeID] = true
			}
		}
		for _, task := range bank {
			if !open[diagnostic.ConstituentKey(task.CompetencyID, task.ConstituentID)] || mastered[task.OutcomeID] {
				continue
			}
			if index, ok := seen[task.OutcomeID]; ok {
				state.Targets[index].Sources = append(state.Targets[index].Sources, task)
				continue
			}
			seen[task.OutcomeID] = len(state.Targets)
			state.Targets = append(state.Targets, training.Target{Kind: "partial_competency", Source: task, Sources: []training.Task{task}})
		}
	}
	perfectMains := 0
	for _, a := range d.Answers {
		state.DiagnosticScore += a.Score
		state.MaximumScore += a.GraderMaxScore
		if a.Role == "main" && a.Score >= exam.RootMaxScore {
			perfectMains++
		}
	}
	if len(state.Targets) == 0 && d.Variant.IncludedCompetencyCount > 0 && perfectMains == d.Variant.IncludedCompetencyCount {
		state.Mode = "free_practice"
		revision, tasks, err := s.repo.Practice(ctx, state.SubjectID)
		if err != nil {
			return state, err
		}
		state.PlanRevision = revision
		indices := map[string]int{}
		for _, task := range tasks {
			if index, exists := indices[task.OutcomeID]; exists {
				state.Targets[index].Sources = append(state.Targets[index].Sources, task)
				continue
			}
			indices[task.OutcomeID] = len(state.Targets)
			target := training.Target{Kind: "free_practice", Source: task, Sources: []training.Task{task}}
			for _, a := range d.Answers {
				if a.OutcomeID == task.OutcomeID {
					target.OriginalScore = a.Score
					target.OriginalMaxScore = diagnosticAnswerMaxScore(a)
					break
				}
			}
			state.Targets = append(state.Targets, target)
		}
	}
	return state, nil
}
func (s *Service) Preview(ctx context.Context, owner, id string) (training.Preview, error) {
	state, err := s.plan(ctx, owner, id)
	if err != nil {
		return training.Preview{}, err
	}
	p := training.Preview{DiagnosticID: id, SubjectID: state.SubjectID, SubjectName: state.SubjectName, Mode: state.Mode, PlanRevision: state.PlanRevision, DiagnosticScore: state.DiagnosticScore, MaximumScore: state.MaximumScore, Status: "ready", ConfirmedGaps: []training.TargetView{}, PartialCompetencies: []training.TargetView{}, Topics: []training.TargetView{}}
	for _, t := range state.Targets {
		v := training.View(t)
		p.Topics = append(p.Topics, v)
		switch t.Kind {
		case "confirmed_gap":
			p.ConfirmedGaps = append(p.ConfirmedGaps, v)
		case "partial_competency":
			p.PartialCompetencies = append(p.PartialCompetencies, v)
		}
	}
	if len(state.Targets) == 0 {
		p.Status = "no_practice_tasks"
	}
	return p, nil
}
func (s *Service) Start(ctx context.Context, owner, id, key string, revision *int64) (training.Progress, bool, error) {
	if err := ValidKey(key); err != nil {
		return training.Progress{}, false, err
	}
	if existing, found, err := s.repo.ByStartKey(ctx, owner, key, id); err != nil {
		return training.Progress{}, false, err
	} else if found {
		return existing.Progress(), true, nil
	}
	if existing, err := s.repo.ByDiagnostic(ctx, owner, id); err == nil {
		stored, _, err := s.repo.Create(ctx, existing, key, revision)
		return stored.Progress(), true, err
	} else {
		var f *fault.Error
		if !errors.As(err, &f) || f.Kind != fault.NotFound {
			return training.Progress{}, false, err
		}
	}
	state, err := s.plan(ctx, owner, id)
	if err != nil {
		return training.Progress{}, false, err
	}
	state.ID, err = s.ids.New("training")
	if err != nil {
		return training.Progress{}, false, err
	}
	if len(state.Targets) > 0 {
		state.Current, err = s.exercise(ctx, state, 0, nil)
		if err != nil {
			return training.Progress{}, false, err
		}
	}
	stored, reused, err := s.repo.Create(ctx, state, key, revision)
	return stored.Progress(), reused, err
}
func (s *Service) exercise(ctx context.Context, state training.Session, index int, attempts []training.Attempt) (training.Exercise, error) {
	target := state.Targets[index]
	if len(target.Sources) > 0 {
		target.Source = target.Sources[(state.Round-1)%int64(len(target.Sources))]
	}
	task, version, err := s.provider.Generate(ctx, target, attempts)
	if err != nil {
		return training.Exercise{}, err
	}
	id, err := s.ids.New("exercise")
	return training.Exercise{ID: id, Task: task, ProviderVersion: version, TargetIndex: index, Round: state.Round}, err
}
func (s *Service) Get(ctx context.Context, owner, id string) (training.Progress, error) {
	v, err := s.repo.Get(ctx, owner, id)
	return v.Progress(), err
}
func (s *Service) ByDiagnostic(ctx context.Context, owner, id string) (training.Progress, error) {
	v, err := s.repo.ByDiagnostic(ctx, owner, id)
	return v.Progress(), err
}
func (s *Service) Answer(ctx context.Context, owner, id, exerciseID, key string, data []byte, media string) (training.Progress, error) {
	return s.answerRequest(ctx, owner, id, exerciseID, key, data, media, false)
}

func (s *Service) Skip(ctx context.Context, owner, id, exerciseID, key string) (training.Progress, error) {
	return s.answerRequest(ctx, owner, id, exerciseID, key, nil, "", true)
}

// NextChecked advances only after a current explicit chat check. It shares the
// ordinary training exercise-generation policy while the chat repository owns
// the transaction that closes the old conversation and records the attempt.
func (s *Service) NextChecked(ctx context.Context, owner, id, exerciseID, key string, revision int64) (training.Progress, error) {
	if s.checkedNext == nil {
		return training.Progress{}, fault.New(fault.Unavailable, "TRAINING_NEXT_UNAVAILABLE", "Переход сейчас недоступен.")
	}
	if err := ValidKey(key); err != nil {
		return training.Progress{}, err
	}
	if revision < 0 {
		return training.Progress{}, fault.Validation("revision", "Ревизия должна быть неотрицательным целым числом.")
	}
	token, err := s.ids.New("training_next")
	if err != nil {
		return training.Progress{}, err
	}
	progress, reservation, err := s.checkedNext.ReserveNext(ctx, owner, id, exerciseID, key, revision, token)
	if err != nil {
		return training.Progress{}, err
	}
	if progress != nil {
		return *progress, nil
	}
	if reservation == nil {
		return training.Progress{}, fault.New(fault.Unavailable, "TRAINING_NEXT_UNAVAILABLE", "Переход сейчас недоступен.")
	}
	failed := true
	defer func() {
		if failed {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			_ = s.checkedNext.FailNext(cleanupCtx, *reservation)
		}
	}()
	state := reservation.Session
	check := reservation.Check
	if state.Current.ID != exerciseID || check.Revision != revision {
		return training.Progress{}, fault.New(fault.Conflict, "CHAT_CHECK_REQUIRED", "Сначала проверьте итоговый ответ для текущего упражнения.")
	}
	attempt := training.Attempt{
		Sequence: state.AnswerCount + 1, ExerciseID: exerciseID, Round: state.Current.Round,
		TargetIndex: state.Current.TargetIndex, Text: check.FinalAnswer, Score: check.Score,
		MaxScore: 2, Verdict: check.Verdict, CriterionResults: append([]training.CriterionResult(nil), check.CriterionResults...),
		Feedback: check.Feedback, CreatedAt: s.now().Unix(), AnswerSource: check.Source,
	}
	if check.TranscriptionID != nil {
		attempt.TranscriptionID = *check.TranscriptionID
	}
	score := attempt.Score
	state.Targets[state.Current.TargetIndex].LastScore = &score
	state.AnswerCount++
	next := state.Current.TargetIndex + 1
	if next == len(state.Targets) {
		next = 0
		state.Round++
	}
	state.Current, err = s.exercise(ctx, state, next, []training.Attempt{attempt})
	if err != nil {
		return training.Progress{}, err
	}
	result, err := s.checkedNext.AcceptNext(ctx, *reservation, state, attempt)
	if err != nil {
		return training.Progress{}, err
	}
	failed = false
	return result, nil
}

func (s *Service) answerRequest(ctx context.Context, owner, id, exerciseID, key string, data []byte, media string, skip bool) (training.Progress, error) {
	if err := ValidKey(key); err != nil {
		return training.Progress{}, err
	}
	if !skip {
		if err := audio.Check(data, media); err != nil {
			return training.Progress{}, err
		}
	}
	if skip {
		media = "skip"
	}
	h := sha256.New()
	h.Write([]byte(exerciseID + "\x00" + media + "\x00"))
	h.Write(data)
	fingerprint := hex.EncodeToString(h.Sum(nil))
	token, err := s.ids.New("attempt")
	if err != nil {
		return training.Progress{}, err
	}
	replay, reserved, state, err := s.repo.Reserve(ctx, owner, id, key, fingerprint, exerciseID, token)
	if err != nil {
		return training.Progress{}, err
	}
	if replay != nil {
		return *replay, nil
	}
	defer func() { _ = s.repo.Fail(context.WithoutCancel(ctx), owner, id, *reserved) }()
	if skip {
		reserved.TranscriptionID, err = s.ids.New("skip")
		if err != nil {
			return training.Progress{}, err
		}
		reserved.Text = "Я не знаю. Пропустить"
	} else if reserved.TranscriptionID == "" {
		tr, err := s.voice.Transcribe(ctx, owner, data, media)
		if err != nil {
			return training.Progress{}, err
		}
		if tr.OwnerID != owner || tr.ID == "" || strings.TrimSpace(tr.Text) == "" {
			return training.Progress{}, fault.New(fault.Upstream, "TRANSCRIPTION_INVALID_RESPONSE", "Некорректная расшифровка.")
		}
		reserved.TranscriptionID = tr.ID
		reserved.Text = tr.Text
	}
	evaluation := assessment.Evaluation{}
	if skip {
		evaluation = assessment.Evaluation{Score: 0, MaxScore: 2, Verdict: "incorrect", Feedback: "Вопрос пропущен. Ответ оценён в 0 баллов."}
	} else {
		evaluation, err = s.grader.EvaluateTraining(ctx, owner, reserved.TranscriptionID, state.Current)
		if err != nil {
			return training.Progress{}, err
		}
		if evaluation.MaxScore != 2 || evaluation.Score < 0 || evaluation.Score > 2 {
			return training.Progress{}, fault.New(fault.Upstream, "INVALID_MODEL_RESPONSE", "Некорректная оценка тренировки.")
		}
	}
	attempt := training.Attempt{Sequence: state.AnswerCount + 1, ExerciseID: state.Current.ID, Round: state.Round, TargetIndex: state.Current.TargetIndex, TranscriptionID: reserved.TranscriptionID, Text: reserved.Text, Score: evaluation.Score, MaxScore: 2, Verdict: evaluation.Verdict, Feedback: evaluation.Feedback, CreatedAt: s.now().Unix(), CriterionResults: []training.CriterionResult{}, Skipped: skip}
	for _, c := range evaluation.CriterionResults {
		attempt.CriterionResults = append(attempt.CriterionResults, training.CriterionResult{Key: c.Key, Satisfied: c.Satisfied, Explanation: c.Explanation})
	}
	score := attempt.Score
	state.Targets[state.Current.TargetIndex].LastScore = &score
	state.AnswerCount++
	next := state.Current.TargetIndex + 1
	if next == len(state.Targets) {
		next = 0
		state.Round++
	}
	state.Current, err = s.exercise(ctx, state, next, []training.Attempt{attempt})
	if err != nil {
		return training.Progress{}, err
	}
	return s.repo.Accept(ctx, owner, *reserved, state, attempt)
}
func (s *Service) History(ctx context.Context, owner, id string, before int64, limit int) ([]training.Attempt, error) {
	return s.repo.History(ctx, owner, id, before, limit)
}

func (s *Service) ResetReservation(ctx context.Context, owner, id string) error {
	return s.repo.ResetReservation(ctx, owner, id)
}
func (s *Service) CurrentAudio(ctx context.Context, owner, id, exerciseID string) (audioasset.Metadata, error) {
	state, err := s.repo.Get(ctx, owner, id)
	if err != nil {
		return audioasset.Metadata{}, err
	}
	if state.Current.ID != exerciseID {
		return audioasset.Metadata{}, fault.New(fault.Conflict, "TRAINING_EXERCISE_NOT_CURRENT", "Упражнение изменилось.")
	}
	m := audioasset.Metadata{Status: audioasset.Missing}
	if state.Current.Task.AudioAssetID == nil {
		return m, nil
	}
	if s.audio == nil {
		return m, fault.New(fault.Unavailable, "AUDIO_STORAGE_UNAVAILABLE", "Озвучка временно недоступна.")
	}
	asset, err := s.audio.Metadata(ctx, *state.Current.Task.AudioAssetID)
	if errors.Is(err, audioasset.ErrNotFound) {
		return m, nil
	}
	if err != nil {
		return m, err
	}
	m.Status = asset.Status
	if asset.Status == audioasset.Ready {
		canonicalURL, ok := audioasset.NormalizeFileURL(*state.Current.Task.AudioAssetID, asset.AudioURL)
		if asset.ID != *state.Current.Task.AudioAssetID || !ok {
			return m, fault.New(fault.Unavailable, "AUDIO_STORAGE_UNAVAILABLE", "Озвучка временно недоступна.")
		}
		m.AudioURL = canonicalURL
	}
	return m, nil
}

func (s *Service) RegenerateCurrentAudio(ctx context.Context, owner, id, exerciseID string) (audioasset.Metadata, error) {
	state, err := s.repo.Get(ctx, owner, id)
	if err != nil {
		return audioasset.Metadata{}, err
	}
	if state.Current.ID != exerciseID {
		return audioasset.Metadata{}, fault.New(fault.Conflict, "TRAINING_EXERCISE_NOT_CURRENT", "Упражнение изменилось.")
	}
	if state.Current.Task.AudioAssetID == nil || *state.Current.Task.AudioAssetID == "" || s.regenerator == nil {
		return audioasset.Metadata{}, fault.New(fault.Conflict, "AUDIO_NOT_REPAIRABLE", "Озвучку сейчас нельзя восстановить.")
	}
	asset, err := s.regenerator.Regenerate(ctx, *state.Current.Task.AudioAssetID)
	if err != nil {
		return audioasset.Metadata{}, fault.New(fault.Unavailable, "AUDIO_STORAGE_UNAVAILABLE", "Озвучка временно недоступна.")
	}
	canonicalURL, ok := audioasset.NormalizeFileURL(*state.Current.Task.AudioAssetID, asset.AudioURL)
	if asset.Status != audioasset.Ready || asset.ID != *state.Current.Task.AudioAssetID || !ok {
		return audioasset.Metadata{}, fault.New(fault.Unavailable, "AUDIO_STORAGE_UNAVAILABLE", "Озвучка временно недоступна.")
	}
	return audioasset.Metadata{Status: asset.Status, AudioURL: canonicalURL}, nil
}

// Retain the original scale for historical diagnostic answers.
func diagnosticAnswerMaxScore(answer diagnostic.Answer) int {
	if answer.GraderMaxScore > 0 {
		return answer.GraderMaxScore
	}
	if answer.Role == "basic" {
		return 1
	}
	if answer.Score == 2 {
		return 2
	}
	return 3
}
