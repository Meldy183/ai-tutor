// Package memory stores diagnostic sessions for the lifetime of this API process.
package memory

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/diagnostic"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/shared/fault"
)

type Store struct {
	mu       sync.RWMutex
	sessions map[string]diagnostic.Session
	starts   map[string]string
}

const maxSessions = 10000

func New() *Store {
	return &Store{sessions: make(map[string]diagnostic.Session), starts: make(map[string]string)}
}

func (s *Store) FindStart(ctx context.Context, ownerID, key, digest string) (diagnostic.Session, bool, error) {
	if err := ctx.Err(); err != nil {
		return diagnostic.Session{}, false, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return diagnostic.Session{}, false, err
	}
	id, ok := s.starts[ownerID+"\x00"+key]
	if !ok {
		return diagnostic.Session{}, false, nil
	}
	value := s.sessions[id]
	if value.StartRequestDigest != digest {
		return diagnostic.Session{}, false, fault.New(fault.Conflict, "IDEMPOTENCY_KEY_REUSED", "Ключ запуска уже использован с другими параметрами.")
	}
	return clone(value), true, nil
}

func (s *Store) Create(ctx context.Context, ownerID, key, digest string, value diagnostic.Session) (diagnostic.Session, bool, error) {
	if err := ctx.Err(); err != nil {
		return diagnostic.Session{}, false, err
	}
	index := ownerID + "\x00" + key
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return diagnostic.Session{}, false, err
	}
	if id, ok := s.starts[index]; ok {
		previous := s.sessions[id]
		if previous.StartRequestDigest != digest {
			return diagnostic.Session{}, false, fault.New(fault.Conflict, "IDEMPOTENCY_KEY_REUSED", "Ключ запуска уже использован с другими параметрами.")
		}
		return clone(previous), true, nil
	}
	if len(s.sessions) >= maxSessions {
		return diagnostic.Session{}, false, fault.New(fault.Unavailable, "DIAGNOSTIC_CAPACITY_REACHED", "Хранилище диагностических сессий временно заполнено.")
	}
	s.starts[index] = value.ID
	s.sessions[value.ID] = clone(value)
	return clone(value), false, nil
}

func (s *Store) Get(ctx context.Context, ownerID, id string) (diagnostic.Session, error) {
	if err := ctx.Err(); err != nil {
		return diagnostic.Session{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return diagnostic.Session{}, err
	}
	value, ok := s.sessions[id]
	if !ok || value.OwnerID != ownerID {
		return diagnostic.Session{}, fault.New(fault.NotFound, "DIAGNOSTIC_SESSION_NOT_FOUND", "Диагностическая сессия не найдена.")
	}
	return clone(value), nil
}

func (s *Store) LatestCompleted(ctx context.Context, ownerID, subjectID string) (diagnostic.Session, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var found diagnostic.Session
	for _, value := range s.sessions {
		if value.OwnerID == ownerID && value.Status == diagnostic.StatusCompleted && value.Variant.SubjectID == subjectID {
			if found.ID == "" || value.ID > found.ID {
				found = value
			}
		}
	}
	if found.ID == "" {
		return diagnostic.Session{}, false, nil
	}
	return clone(found), true, nil
}
func (s *Store) LearningState(ctx context.Context, ownerID, subjectID string) (diagnostic.LearningState, error) {
	v, found, err := s.LatestCompleted(ctx, ownerID, subjectID)
	state := diagnostic.LearningState{SubjectID: subjectID, DiagnosticStatus: "not_started"}
	if found {
		state.SubjectName = v.Variant.SubjectNameSnapshot
		state.DiagnosticSessionID = v.ID
		state.DiagnosticCompleted = true
		state.TrainingAvailable = diagnostic.HasLearningGaps(v.Answers)
		state.DiagnosticStatus = "completed"
	}
	return state, err
}

func (s *Store) Reserve(ctx context.Context, ownerID, id, key, digest, taskID, token string) (*diagnostic.AcceptedRequest, *diagnostic.Reservation, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	value, ok := s.sessions[id]
	if !ok || value.OwnerID != ownerID {
		return nil, nil, fault.New(fault.NotFound, "DIAGNOSTIC_SESSION_NOT_FOUND", "Диагностическая сессия не найдена.")
	}
	if previous, ok := value.AcceptedRequests[key]; ok {
		if previous.Fingerprint != digest {
			return nil, nil, fault.New(fault.Conflict, "IDEMPOTENCY_KEY_REUSED", "Ключ ответа уже использован с другими данными.")
		}
		previous.Response = cloneProgress(previous.Response)
		return &previous, nil, nil
	}
	if value.Status != diagnostic.StatusActive {
		return nil, nil, fault.New(fault.Conflict, "DIAGNOSTIC_SESSION_COMPLETED", "Диагностическая сессия уже завершена.")
	}
	current := value.Current()
	if current == nil || current.ID != taskID {
		return nil, nil, fault.New(fault.Conflict, "DIAGNOSTIC_TASK_NOT_CURRENT", "Укажите текущее задание сессии.")
	}
	if value.InFlight != nil {
		pending := value.InFlight
		if pending.Token != "" {
			return nil, nil, fault.New(fault.Conflict, "DIAGNOSTIC_ANSWER_IN_PROGRESS", "Ответ для текущего задания уже обрабатывается.")
		}
		// A failed attempt was never accepted, so a new recording or skip replaces it.
		if pending.Key == key && pending.Fingerprint == digest && pending.VariantTaskID == taskID {
			pending.Token = token
			s.sessions[id] = value
			copy := *pending
			return nil, &copy, nil
		}
	}
	value.InFlight = &diagnostic.Reservation{Key: key, Fingerprint: digest, VariantTaskID: taskID, Token: token}
	s.sessions[id] = value
	copy := *value.InFlight
	return nil, &copy, nil
}

func (s *Store) Accept(ctx context.Context, ownerID, id, token, key, digest string, answer diagnostic.Answer, transition diagnostic.Transition) (diagnostic.Progress, error) {
	if err := ctx.Err(); err != nil {
		return diagnostic.Progress{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return diagnostic.Progress{}, err
	}
	value, ok := s.sessions[id]
	if !ok || value.OwnerID != ownerID {
		return diagnostic.Progress{}, fault.New(fault.NotFound, "DIAGNOSTIC_SESSION_NOT_FOUND", "Диагностическая сессия не найдена.")
	}
	if value.InFlight == nil || value.InFlight.Token != token || value.InFlight.VariantTaskID != answer.VariantTaskID {
		return diagnostic.Progress{}, fault.New(fault.Conflict, "DIAGNOSTIC_ANSWER_STALE", "Обработка ответа устарела.")
	}
	answer = cloneAnswer(answer)
	value.Answers = append(value.Answers, answer)
	value.CurrentCompetency = transition.CurrentCompetency
	value.CurrentTask = transition.CurrentTask
	value.Traversal = append(json.RawMessage(nil), transition.Traversal...)
	value.CurrentPosition = transition.CurrentPosition
	value.Status = transition.Status
	value.SkippedBasics = append(value.SkippedBasics, cloneTasks(transition.SkippedBasics)...)
	value.InFlight = nil
	progress := value.Progress()
	progress.Text = answer.Text
	progress.AnswerSkipped = answer.Skipped
	score, graderScore, graderMaxScore, verdict := answer.Score, answer.GraderScore, answer.GraderMaxScore, answer.Verdict
	progress.Score, progress.GraderScore, progress.GraderMaxScore, progress.Verdict = &score, &graderScore, &graderMaxScore, verdict
	progress.CriterionResults = append([]diagnostic.CriterionResult(nil), answer.CriterionResults...)
	progress.Feedback = answer.Feedback
	value.AcceptedRequests[key] = diagnostic.AcceptedRequest{Fingerprint: digest, Response: progress}
	s.sessions[id] = value
	return cloneProgress(progress), nil
}

func (s *Store) Fail(_ context.Context, ownerID, id, token, transcriptionID, transcriptionText string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.sessions[id]
	if !ok || value.OwnerID != ownerID {
		return fault.New(fault.NotFound, "DIAGNOSTIC_SESSION_NOT_FOUND", "Диагностическая сессия не найдена.")
	}
	if value.InFlight == nil || value.InFlight.Token != token {
		return nil
	}
	if transcriptionID != "" {
		value.InFlight.TranscriptionID = transcriptionID
		value.InFlight.TranscriptionText = transcriptionText
		value.InFlight.Token = ""
	} else {
		value.InFlight = nil
	}
	s.sessions[id] = value
	return nil
}

func clone(value diagnostic.Session) diagnostic.Session {
	value.Variant.SkippedCompetencies = append([]diagnostic.SkippedCompetency{}, value.Variant.SkippedCompetencies...)
	value.Variant.Competencies = append([]diagnostic.Competency(nil), value.Variant.Competencies...)
	for i := range value.Variant.Competencies {
		value.Variant.Competencies[i].Tasks = cloneTasks(value.Variant.Competencies[i].Tasks)
	}
	value.Answers = append([]diagnostic.Answer(nil), value.Answers...)
	for i := range value.Answers {
		value.Answers[i] = cloneAnswer(value.Answers[i])
	}
	copyRequests := make(map[string]diagnostic.AcceptedRequest, len(value.AcceptedRequests))
	for key, item := range value.AcceptedRequests {
		item.Response = cloneProgress(item.Response)
		copyRequests[key] = item
	}
	value.AcceptedRequests = copyRequests
	value.SkippedBasics = cloneTasks(value.SkippedBasics)
	if value.InFlight != nil {
		reservation := *value.InFlight
		value.InFlight = &reservation
	}
	return value
}

func cloneTasks(tasks []diagnostic.TaskSnapshot) []diagnostic.TaskSnapshot {
	result := append([]diagnostic.TaskSnapshot(nil), tasks...)
	for i := range result {
		result[i].Options = append([]string{}, result[i].Options...)
	}
	return result
}

func cloneAnswer(answer diagnostic.Answer) diagnostic.Answer {
	answer.Task.Options = append([]string{}, answer.Task.Options...)
	answer.CriterionResults = append([]diagnostic.CriterionResult(nil), answer.CriterionResults...)
	return answer
}

func cloneProgress(value diagnostic.Progress) diagnostic.Progress {
	if value.Current != nil {
		current := *value.Current
		current.Options = append([]string{}, value.Current.Options...)
		value.Current = &current
	}
	value.CriterionResults = append([]diagnostic.CriterionResult(nil), value.CriterionResults...)
	if value.Score != nil {
		currentScore := *value.Score
		value.Score = &currentScore
	}
	if value.GraderScore != nil {
		graderScore := *value.GraderScore
		value.GraderScore = &graderScore
	}
	if value.GraderMaxScore != nil {
		maxScore := *value.GraderMaxScore
		value.GraderMaxScore = &maxScore
	}
	return value
}
